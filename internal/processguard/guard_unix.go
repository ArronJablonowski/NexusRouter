//go:build darwin || linux

package processguard

import (
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/unix"
)

type holder struct {
	reference Reference
	root      *os.Root
	file      *os.File
}

// Never expose, close, finalize or evict the owning descriptors. There is one
// guard per process, shared across all databases and short-lived Store handles.
// OS process exit releases it; Store.Close and GC cannot do so.
var lifetime struct {
	sync.Mutex
	owner *holder
}

// Current creates ownership lazily, then verifies it before every lease use.
// Damage to an established guard is not repaired by silently creating another.
func Current(ctx context.Context) (Reference, error) {
	if err := checkContext(ctx); err != nil {
		return Reference{}, err
	}
	lifetime.Lock()
	defer lifetime.Unlock()
	if err := checkContext(ctx); err != nil {
		return Reference{}, err
	}
	if lifetime.owner == nil {
		owner, err := newHolder()
		if err != nil {
			return Reference{}, ErrUnavailable
		}
		lifetime.owner = owner
	}
	if err := verify(lifetime.owner); err != nil {
		return Reference{}, ErrUnavailable
	}
	if err := checkContext(ctx); err != nil {
		return Reference{}, err
	}
	return lifetime.owner.reference, nil
}

func newHolder() (_ *holder, err error) {
	directory, err := os.MkdirTemp("", "darwin-owner-")
	if err != nil {
		return nil, err
	}
	// Persist canonical spelling, not a /var or TMPDIR symlink alias. Never
	// delete guard paths: losing a path makes ownership unknown, not dead.
	directory, err = filepath.EvalSymlinks(directory)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	var file *os.File
	defer func() {
		if err != nil {
			if file != nil {
				_ = file.Close()
			}
			_ = root.Close()
		}
	}()
	if !localFilesystem(root) {
		return nil, ErrUnavailable
	}
	file, err = root.OpenFile("owner.lock", os.O_CREATE|os.O_EXCL|os.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return nil, err
	}
	id := rand.Text()
	if n, writeErr := file.WriteString(id); writeErr != nil || n != len(id) {
		return nil, ErrUnavailable
	}
	dirInfo, err := root.Stat(".")
	if err != nil {
		return nil, err
	}
	fileInfo, err := file.Stat()
	if err != nil {
		return nil, err
	}
	owner := &holder{Reference{1, id, directory, identity(dirInfo), identity(fileInfo)}, root, file}
	if err = verify(owner); err != nil {
		return nil, err
	}
	if err = file.Sync(); err != nil {
		return nil, err
	}
	// Missing entries after a power loss remain unknown. These syncs are not
	// a claim of power-loss qualification for the filesystem or temp parent.
	dirFile, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	err = dirFile.Sync()
	closeErr := dirFile.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	return owner, nil
}

func verify(h *holder) error {
	if h == nil || h.reference.Validate() != nil {
		return ErrUnavailable
	}
	dirPath, err := os.Lstat(h.reference.Directory)
	if err != nil || !privateNode(dirPath, true) || identity(dirPath) != h.reference.DirectoryIdentity {
		return ErrUnavailable
	}
	dirPinned, err := h.root.Stat(".")
	if err != nil || !privateNode(dirPinned, true) || identity(dirPinned) != h.reference.DirectoryIdentity {
		return ErrUnavailable
	}
	filePath, err := h.root.Lstat("owner.lock")
	if err != nil || !privateNode(filePath, false) || identity(filePath) != h.reference.FileIdentity {
		return ErrUnavailable
	}
	filePinned, err := h.file.Stat()
	if err != nil || !privateNode(filePinned, false) || identity(filePinned) != h.reference.FileIdentity {
		return ErrUnavailable
	}
	var id [26]byte
	if n, err := h.file.ReadAt(id[:], 0); err != nil || n != len(id) || string(id[:]) != h.reference.ID {
		return ErrUnavailable
	}
	flags, err := unix.FcntlInt(h.file.Fd(), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		return ErrUnavailable
	}
	return nil
}

// Observation keeps an independently acquired probe lock until Close. Unlocked
// is an OS-lock observation, not by itself authority to reclaim or retry work.
// Any future reclaimer must keep the verified lock through its fencing commit.
type Observation struct {
	State State
	mu    sync.Mutex
	owner *holder
}

func (o *Observation) Close() error {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.owner == nil {
		return nil
	}
	fileErr, rootErr := o.owner.file.Close(), o.owner.root.Close()
	o.owner = nil
	if fileErr != nil || rootErr != nil {
		return ErrUnavailable
	}
	return nil
}

// Probe never creates paths, changes file contents, or repairs references. A
// missing, substituted, insecure or unsupported file always yields an error.
func Probe(ctx context.Context, ref Reference) (_ *Observation, err error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if ref.Validate() != nil {
		return nil, ErrUnavailable
	}
	dirInfo, err := os.Lstat(ref.Directory)
	if err != nil || !privateNode(dirInfo, true) || identity(dirInfo) != ref.DirectoryIdentity {
		return nil, ErrUnavailable
	}
	root, err := os.OpenRoot(ref.Directory)
	if err != nil {
		return nil, ErrUnavailable
	}
	var file *os.File
	defer func() {
		if err != nil {
			if file != nil {
				_ = file.Close()
			}
			_ = root.Close()
		}
	}()
	if !localFilesystem(root) {
		return nil, ErrUnavailable
	}
	fileInfo, err := root.Lstat("owner.lock")
	if err != nil || !privateNode(fileInfo, false) || identity(fileInfo) != ref.FileIdentity {
		return nil, ErrUnavailable
	}
	// Reject special files before opening, and also use NONBLOCK so a raced
	// substitution with a FIFO cannot turn a probe into an unbounded open.
	file, err = root.OpenFile("owner.lock", os.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, ErrUnavailable
	}
	h := &holder{ref, root, file}
	if verify(h) != nil {
		return nil, ErrUnavailable
	}
	state := Unlocked
	if lockErr := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); lockErr == unix.EWOULDBLOCK || lockErr == unix.EAGAIN {
		state = Held
	} else if lockErr != nil {
		return nil, ErrUnavailable
	}
	if verify(h) != nil {
		return nil, ErrUnavailable
	}
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	return &Observation{State: state, owner: h}, nil
}
