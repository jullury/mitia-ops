package docker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeRunner struct {
	dir  string
	args []string
	out  string
	err  error
}

func (f *fakeRunner) Run(dir string, args ...string) (string, error) {
	f.dir = dir
	f.args = args
	return f.out, f.err
}

func TestUpCallsCompose(t *testing.T) {
	f := &fakeRunner{out: "started"}
	got, err := Up("/srv/minio", f)
	if err != nil {
		t.Fatal(err)
	}
	if got != "started" {
		t.Fatalf("unexpected output: %q", got)
	}
	if f.dir != "/srv/minio" {
		t.Fatalf("unexpected dir: %q", f.dir)
	}
	if !contains(f.args, "up") || !contains(f.args, "-d") {
		t.Fatalf("unexpected args: %v", f.args)
	}
}

func contains(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

type fakeRaw struct {
	args []string
	out  string
	err  error
}

func (f *fakeRaw) RunRaw(args ...string) (string, error) {
	f.args = append(f.args, args...)
	return f.out, f.err
}

func TestRemoveDeployDirPlainSuccess(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw := &fakeRaw{}
	if err := RemoveDeployDir(dir, "", raw); err != nil {
		t.Fatal(err)
	}
	if len(raw.args) != 0 {
		t.Fatalf("plain removal should not invoke docker, got %v", raw.args)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("dir should be removed, stat err = %v", err)
	}
}

func TestRemoveDeployDirFallsBackToRootContainer(t *testing.T) {
	dir := t.TempDir()
	// Make RemoveAll fail as the user: a file whose parent dir is un-writable
	// (mode 0) so its entry cannot be unlinked.
	block := filepath.Join(dir, "assets")
	if err := os.Mkdir(block, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(block, "cert.pem"), []byte("c"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(block, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(block, 0o755) })

	raw := &fakeRaw{}
	if err := RemoveDeployDir(dir, "", raw); err != nil {
		t.Fatal(err)
	}
	if len(raw.args) == 0 {
		t.Fatal("fallback must run a root container when plain removal fails")
	}
	// First arg should be "run" and the -v mount must use an absolute host path.
	if raw.args[0] != "run" {
		t.Fatalf("expected docker run, got %v", raw.args)
	}
	var volume string
	for i, a := range raw.args {
		if a == "-v" && i+1 < len(raw.args) {
			volume = raw.args[i+1]
		}
	}
	if !strings.HasPrefix(volume, "/") {
		t.Fatalf("-v host path should be absolute, got %q", volume)
	}
	if !strings.HasSuffix(volume, ":/__p") {
		t.Fatalf("-v should mount the parent at /__p, got %q", volume)
	}
	// The rm target must be the child dir by name under the parent mount (not
	// the mountpoint itself, which cannot be removed and would hit EBUSY).
	base := filepath.Base(dir)
	want := "/__p/" + base
	found := false
	for _, a := range raw.args {
		if a == "rm" {
			continue
		}
		if a == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected rm target %q in args %v", want, raw.args)
	}
}
