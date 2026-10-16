package maintenance

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/scripts"
)

func TestCleanupRejectsUnqualifiedScriptAndArbitraryActions(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := Service{Home: home}
	for _, action := range []string{"status", "preview", "run", "shell"} {
		if _, err := s.Invoke(context.Background(), action, ""); err == nil {
			t.Fatal("unqualified utility accepted", action)
		}
	}
	path := filepath.Join(home, "tools/maintenance/cleanup-nexus-home.py")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("raise Exception('must not execute')"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Invoke(context.Background(), "preview", ""); err == nil {
		t.Fatal("changed script accepted")
	}
	if err := os.WriteFile(path, scripts.CleanupSource, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Invoke(context.Background(), "run", strings.Repeat("z", 64)); err == nil {
		t.Fatal("invalid token")
	}
	if runtime.GOOS == "darwin" {
		out, err := s.Invoke(context.Background(), "status", "")
		if err != nil || out.Version != 1 || out.LastRun != nil {
			t.Fatal(out, err)
		}
	}
}
func TestCleanupOutputLimit(t *testing.T) {
	var b limitedBuffer
	if _, err := b.Write(make([]byte, (2<<20)+1)); err == nil {
		t.Fatal("unbounded output")
	}
}

func TestCleanupRealPreviewApplyAndStatus(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("commander cleanup currently supports macOS")
	}
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(home, "tools/maintenance/cleanup-nexus-home.py")
	if err := os.MkdirAll(filepath.Dir(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, scripts.CleanupSource, 0700); err != nil {
		t.Fatal(err)
	}
	setup := `import pathlib,sqlite3,os,sys,time
r=pathlib.Path(sys.argv[1]);(r/'resources/qa').mkdir(parents=True)
(r/'data/uploads').mkdir(parents=True);(r/'data/uploads/original.txt').write_text('preserve')
for i in range(1,6):
 p=r/'bin/backups'/('202601%02dT010101Z-test'%i);(p/'config').mkdir(parents=True);(p/'commander').mkdir()
 (p/'nexus.previous').write_bytes(b'\x7fELF');db=sqlite3.connect(str(p/'commander/tasks.db'));db.execute('create table test (id integer)');db.close();os.utime(str(p),(time.time()-864000,)*2)
p=r/'resources/qa/nexus-fixture'
with p.open('wb') as f:f.write(b'\x7fELF');f.truncate(21*1024*1024)
os.utime(str(p),(time.time()-864000,)*2)
`
	if out, err := exec.Command("/usr/bin/python3", "-B", "-c", setup, home).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if out, err := exec.Command("/usr/bin/python3", "-B", script, "--home", home, "--register-qa", "resources/qa/nexus-fixture").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	s := Service{Home: home}
	ctx := context.Background()
	opened, err := os.Open(filepath.Join(home, "resources/qa/nexus-fixture"))
	if err != nil {
		t.Fatal(err)
	}
	preview, err := s.Invoke(ctx, "preview", "")
	if err != nil {
		opened.Close()
		t.Fatal(err)
	}
	if len(preview.Preview.Items) != 2 {
		opened.Close()
		t.Fatal("open build was not protected", preview.Preview)
	}
	opened.Close()
	preview, err = s.Invoke(ctx, "preview", "")
	if err != nil || len(preview.Preview.Items) != 3 {
		t.Fatal(preview, err)
	}
	result, err := s.Invoke(ctx, "run", preview.Preview.Token)
	if err != nil || result.Result == nil || !result.Result.Complete || len(result.Result.Deleted) != 3 {
		t.Fatal(result, err)
	}
	status, err := s.Invoke(ctx, "status", "")
	if err != nil || status.LastRun == nil || len(status.LastRun.Deleted) != 3 {
		t.Fatal(status, err)
	}
	if b, err := os.ReadFile(filepath.Join(home, "data/uploads/original.txt")); err != nil || string(b) != "preserve" {
		t.Fatal("upload modified", err)
	}
}
