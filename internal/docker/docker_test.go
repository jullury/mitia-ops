package docker

import (
	"testing"
)

type fakeRunner struct {
	dir string
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