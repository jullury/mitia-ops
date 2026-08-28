package docker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type recordingRunner struct {
	compose []string   // last compose invocation
	rawSeq  [][]string // all raw invocations
	exist   bool       // volume existence seen by VolumeExists
}

func (r *recordingRunner) Run(dir string, args ...string) (string, error) {
	r.compose = args
	return "ok", nil
}

func (r *recordingRunner) RunRaw(args ...string) (string, error) {
	r.rawSeq = append(r.rawSeq, args)
	// satisfy VolumeExists-backed flows
	if len(args) == 3 && args[0] == "volume" && args[1] == "inspect" {
		if !r.exist {
			return "", &exitError{}
		}
	}
	return "ok", nil
}

type exitError struct{}

func (*exitError) Error() string { return "not found" }

func anyContains(args []string, substr string) bool {
	for _, a := range args {
		if sub(a, substr) {
			return true
		}
	}
	return false
}

func sub(s, substr string) bool {
	return strings.Contains(s, substr)
}

func TestVolumeName(t *testing.T) {
	if got := VolumeName("/srv/deployments/12", "minio_data"); got != "12_minio_data" {
		t.Fatalf("VolumeName = %q, want 12_minio_data", got)
	}
}

func TestEnsureVolumeDoesNotRecreate(t *testing.T) {
	r := &recordingRunner{exist: true}
	if err := EnsureVolume(r, "12_minio_data", "100G"); err != nil {
		t.Fatal(err)
	}
	if len(r.rawSeq) != 1 {
		t.Fatalf("existing volume should only be inspected, got %d calls", len(r.rawSeq))
	}
	if r.rawSeq[0][1] != "inspect" {
		t.Fatalf("expected inspect call, got %v", r.rawSeq[0])
	}
}

func TestEnsureVolumeCreatesWhenMissing(t *testing.T) {
	r := &recordingRunner{exist: false}
	if err := EnsureVolume(r, "12_minio_data", "100G"); err != nil {
		t.Fatal(err)
	}
	if len(r.rawSeq) != 2 {
		t.Fatalf("missing volume should be inspected then created, got %d calls", len(r.rawSeq))
	}
	if r.rawSeq[1][0] != "volume" || r.rawSeq[1][1] != "create" {
		t.Fatalf("expected volume create, got %v", r.rawSeq[1])
	}
	if !anyContains(r.rawSeq[1], "size=100G") {
		t.Fatalf("create should carry the size opt, got %v", r.rawSeq[1])
	}
}

func TestCreateVolumeCarriesSizeOpt(t *testing.T) {
	r := &recordingRunner{}
	if err := CreateVolume(r, "12_minio_data", "200G"); err != nil {
		t.Fatal(err)
	}
	last := r.rawSeq[len(r.rawSeq)-1]
	if !anyContains(last, "size=200G") {
		t.Fatalf("create should carry size=200G, got %v", last)
	}
	if !anyContains(last, "volumes/12_minio_data/_data") {
		t.Fatalf("create device should reference the volume mountpoint, got %v", last)
	}
}

func TestParseSize(t *testing.T) {
	cases := map[string]int64{
		"100G": 100 << 30,
		"512M": 512 << 20,
		"1T":   int64(1) << 40,
		"2K":   int64(2 << 10),
		"100":  100 << 20,
	}
	for in, want := range cases {
		got, err := ParseSize(in)
		if err != nil {
			t.Fatalf("ParseSize(%q): %v", in, err)
		}
		if got != want {
			t.Fatalf("ParseSize(%q) = %d, want %d", in, got, want)
		}
	}
	if _, err := ParseSize("abc"); err == nil {
		t.Fatal("expected error for invalid size")
	}
}

func TestBytesAvailableWalksUpToExistingDir(t *testing.T) {
	base := t.TempDir()
	// A non-existent leaf whose parent exists: statfs must resolve by walking up
	// (the deploy dir is not created until the first compose write).
	missing := filepath.Join(base, "12", "depth2", "depth3")
	n, err := BytesAvailable(missing)
	if err != nil {
		t.Fatalf("BytesAvailable(%q): %v", missing, err)
	}
	if n <= 0 {
		t.Fatalf("BytesAvailable returned non-positive %d", n)
	}
	// Confirm the bare leaf really does not exist, so success proves walk-up.
	if _, err := os.Stat(base); err != nil {
		t.Fatalf("temp dir missing: %v", err)
	}
}
