package releasepack

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ErrSignature deliberately omits paths, key material, and untrusted contents.
var ErrSignature = errors.New("release signature verification or signing failed")

const signatureName = "SHA256SUMS.sig"

// Sign validates every release checksum before signing the exact SHA256SUMS
// bytes. keyFile must contain a 32-byte Ed25519 seed as lowercase hex, optionally
// followed by one newline, in a regular nonsymlink file with mode 0400 or 0600.
// It exclusively creates SHA256SUMS.sig; existing signatures are never replaced.
// This deliberately does not accept SSH keys or discover any signing identity.
func Sign(dir, keyFile string) error {
	seed, err := signingKeyFile(keyFile, true)
	if err != nil {
		return ErrSignature
	}
	defer clear(seed)
	root, sums, err := checkedRelease(dir, false)
	if err != nil {
		return ErrSignature
	}
	defer root.Close()
	private := ed25519.NewKeyFromSeed(seed)
	defer clear(private)
	signature := ed25519.Sign(private, sums)
	f, err := root.OpenFile(signatureName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return ErrSignature
	}
	_, writeErr := io.WriteString(f, hex.EncodeToString(signature)+"\n")
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		// Leave any partial file intact so an uncertain write cannot be silently
		// retried or mistaken for permission to overwrite an existing signature.
		return ErrSignature
	}
	return nil
}

// Verify authenticates SHA256SUMS using only the explicitly supplied trusted
// public-key file, then verifies all covered regular files. It never extracts,
// executes, installs, or trusts a public key bundled in the release directory.
// publicKeyFile uses the same hex encoding as the seed, without secret modes.
func Verify(dir, publicKeyFile string) error {
	key, err := signingKeyFile(publicKeyFile, false)
	if err != nil {
		return ErrSignature
	}
	return verifyWithKey(dir, key)
}

func verifyWithKey(dir string, key ed25519.PublicKey) error {
	root, err := releaseRoot(dir)
	if err != nil {
		return ErrSignature
	}
	defer root.Close()
	sums, err := readReleaseFile(root, "SHA256SUMS", 64<<10)
	if err != nil {
		return ErrSignature
	}
	encoded, err := readReleaseFile(root, signatureName, 129)
	if err != nil {
		return ErrSignature
	}
	signature, err := decodeSigningHex(encoded, ed25519.SignatureSize)
	if err != nil || !ed25519.Verify(key, sums, signature) {
		return ErrSignature
	}
	if err = checkReleaseFiles(root, sums, true); err != nil {
		return ErrSignature
	}
	return nil
}

func checkedRelease(dir string, signed bool) (*os.Root, []byte, error) {
	root, err := releaseRoot(dir)
	if err != nil {
		return nil, nil, ErrSignature
	}
	sums, err := readReleaseFile(root, "SHA256SUMS", 64<<10)
	if err != nil || checkReleaseFiles(root, sums, signed) != nil {
		root.Close()
		return nil, nil, ErrSignature
	}
	return root, sums, nil
}

func releaseRoot(dir string) (*os.Root, error) {
	if dir == "" {
		return nil, ErrSignature
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrSignature
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, ErrSignature
	}
	actual, err := root.Stat(".")
	if err != nil || !os.SameFile(info, actual) {
		root.Close()
		return nil, ErrSignature
	}
	return root, nil
}

func signingKeyFile(path string, secret bool) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || (secret && info.Mode().Perm() != 0600 && info.Mode().Perm() != 0400) {
		return nil, ErrSignature
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, ErrSignature
	}
	defer f.Close()
	actual, err := f.Stat()
	if err != nil || !os.SameFile(info, actual) || (secret && actual.Mode().Perm() != 0600 && actual.Mode().Perm() != 0400) {
		return nil, ErrSignature
	}
	body, err := io.ReadAll(io.LimitReader(f, 66))
	if err != nil {
		return nil, ErrSignature
	}
	defer clear(body)
	return decodeSigningHex(body, 32)
}

func decodeSigningHex(body []byte, size int) ([]byte, error) {
	body = bytes.TrimSuffix(body, []byte("\n"))
	if len(body) != 2*size || string(body) != strings.ToLower(string(body)) {
		return nil, ErrSignature
	}
	out := make([]byte, size)
	if _, err := hex.Decode(out, body); err != nil {
		return nil, ErrSignature
	}
	return out, nil
}

func openReleaseFile(root *os.Root, name string) (*os.File, error) {
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() {
		return nil, ErrSignature
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, ErrSignature
	}
	actual, err := f.Stat()
	if err != nil || !os.SameFile(info, actual) || !actual.Mode().IsRegular() {
		f.Close()
		return nil, ErrSignature
	}
	return f, nil
}

func readReleaseFile(root *os.Root, name string, max int64) ([]byte, error) {
	f, err := openReleaseFile(root, name)
	if err != nil {
		return nil, ErrSignature
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil || int64(len(body)) > max {
		return nil, ErrSignature
	}
	return body, nil
}

func checkReleaseFiles(root *os.Root, sums []byte, signed bool) error {
	if len(sums) == 0 || sums[len(sums)-1] != '\n' {
		return ErrSignature
	}
	lines := strings.Split(string(sums[:len(sums)-1]), "\n")
	if len(lines) > 128 {
		return ErrSignature
	}
	covered := map[string]bool{"SHA256SUMS": true}
	digests := make(map[string]string)
	if signed {
		covered[signatureName] = true
	}
	previous := ""
	for _, line := range lines {
		if len(line) < 67 || line[64:66] != "  " {
			return ErrSignature
		}
		name := line[66:]
		if !signingBasename(name) || name <= previous || covered[name] || name == signatureName {
			return ErrSignature
		}
		want, err := decodeSigningHex([]byte(line[:64]), sha256.Size)
		if err != nil {
			return ErrSignature
		}
		f, err := openReleaseFile(root, name)
		if err != nil {
			return ErrSignature
		}
		hash := sha256.New()
		n, copyErr := io.Copy(hash, io.LimitReader(f, (4<<30)+1))
		closeErr := f.Close()
		if copyErr != nil || closeErr != nil || n > 4<<30 || !bytes.Equal(hash.Sum(nil), want) {
			return ErrSignature
		}
		covered[name], previous = true, name
		digests[name] = line[:64]
	}
	if validateSignedManifest(root, digests) != nil {
		return ErrSignature
	}
	f, err := root.Open(".")
	if err != nil {
		return ErrSignature
	}
	defer f.Close()
	entries, err := f.ReadDir(131)
	if err != nil && !errors.Is(err, io.EOF) {
		return ErrSignature
	}
	if len(entries) != len(covered) {
		return ErrSignature
	}
	for _, entry := range entries {
		if !covered[entry.Name()] || !entry.Type().IsRegular() {
			return ErrSignature
		}
	}
	return nil
}

func signingBasename(name string) bool {
	if len(name) == 0 || len(name) > 255 || name[0] == '.' || filepath.Base(name) != name {
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
