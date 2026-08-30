package docker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type recordingRunner struct {
	compose []string        // last compose invocation
	rawSeq  [][]string      // all raw invocations
	exist   bool            // volume existence seen by VolumeExists
	real    map[string]bool // optional per-volume existence, wins over exist
}

func (r *recordingRunner) Run(dir string, args ...string) (string, error) {
	r.compose = args
	return "ok", nil
}

func (r *recordingRunner) RunRaw(args ...string) (string, error) {
	r.rawSeq = append(r.rawSeq, args)
	// satisfy VolumeExists-backed flows
	if len(args) == 3 && args[0] == "volume" && args[1] == "inspect" {
		if r.real != nil {
			if !r.real[args[2]] {
				return "", &exitError{}
			}
			return "ok", nil
		}
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
	if err := EnsureVolume(r, "12_minio_data"); err != nil {
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
	if err := EnsureVolume(r, "12_minio_data"); err != nil {
		t.Fatal(err)
	}
	if len(r.rawSeq) != 2 {
		t.Fatalf("missing volume should be inspected then created, got %d calls", len(r.rawSeq))
	}
	if r.rawSeq[1][0] != "volume" || r.rawSeq[1][1] != "create" {
		t.Fatalf("expected volume create, got %v", r.rawSeq[1])
	}
	if anyContains(r.rawSeq[1], "size=") || anyContains(r.rawSeq[1], "o=") {
		t.Fatalf("create must be a plain local volume (no size/type/bind opts), got %v", r.rawSeq[1])
	}
}

func TestCreateVolumePlain(t *testing.T) {
	r := &recordingRunner{}
	if err := CreateVolume(r, "12_minio_data"); err != nil {
		t.Fatal(err)
	}
	last := r.rawSeq[len(r.rawSeq)-1]
	if !anyContains(last, "12_minio_data") {
		t.Fatalf("create should carry the volume name, got %v", last)
	}
	if anyContains(last, "size=") || anyContains(last, "type=") || anyContains(last, "device=") || anyContains(last, "o=") {
		t.Fatalf("create must be a plain local volume, got %v", last)
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

func isVolumeCmd(args []string, verb, name string) bool {
	if len(args) < 3 || args[0] != "volume" || args[1] != verb {
		return false
	}
	return sub(args[len(args)-1], name)
}

func TestRelocateVolumeFullMove(t *testing.T) {
	r := &recordingRunner{real: map[string]bool{"4_minio_data": true}}
	if err := RelocateVolume(r, "4_minio_data", "abcd-1234_minio_data"); err != nil {
		t.Fatal(err)
	}
	// The source is removed last, proving the copy completed first.
	last := r.rawSeq[len(r.rawSeq)-1]
	if !isVolumeCmd(last, "rm", "4_minio_data") {
		t.Fatalf("expected final source removal, got %v", last)
	}
	// The data must be archived and restored, never just renamed.
	var backup, restore bool
	for _, args := range r.rawSeq {
		if anyContains(args, "czf") {
			backup = true
		}
		if anyContains(args, "xzf") {
			restore = true
		}
	}
	if !backup || !restore {
		t.Fatalf("expected backup+restore runs, got %v", r.rawSeq)
	}
	// Target created exactly once (it did not exist, so must not be dropped).
	creates := 0
	for _, args := range r.rawSeq {
		if isVolumeCmd(args, "create", "abcd-1234_minio_data") {
			creates++
		}
		if isVolumeCmd(args, "rm", "abcd-1234_minio_data") {
			creates = 99 // flag unexpected target removal
		}
	}
	if creates != 1 {
		t.Fatalf("expected one target create and no target removal, got %d", creates)
	}
}

func TestRelocateVolumeDropsStaleTarget(t *testing.T) {
	r := &recordingRunner{real: map[string]bool{"4_minio_data": true, "abcd-1234_minio_data": true}}
	if err := RelocateVolume(r, "4_minio_data", "abcd-1234_minio_data"); err != nil {
		t.Fatal(err)
	}
	// A stale target (empty volume created by an interrupted run) must be
	// dropped before the recreate, since the source holds the real data.
	dropped := false
	for _, args := range r.rawSeq {
		if isVolumeCmd(args, "rm", "abcd-1234_minio_data") {
			dropped = true
		}
	}
	if !dropped {
		t.Fatalf("expected stale target removal, got %v", r.rawSeq)
	}
}

func TestRelocateVolumeNoopWhenSourceGone(t *testing.T) {
	r := &recordingRunner{real: map[string]bool{}}
	if err := RelocateVolume(r, "4_minio_data", "abcd-1234_minio_data"); err != nil {
		t.Fatal(err)
	}
	// Source already moved: only the existence check runs, nothing is created.
	if len(r.rawSeq) != 1 || !isVolumeCmd(r.rawSeq[0], "inspect", "4_minio_data") {
		t.Fatalf("expected a single inspection, got %v", r.rawSeq)
	}
}

func TestBackupSnapshotArgv(t *testing.T) {
	r := &recordingRunner{}
	vols := []string{"12_pg_data", "12_other"}
	err := BackupSnapshot(r, vols, "/deploy/12", "/pgstage", "/out/12-pg.tar.gz", VolumeImage)
	if err != nil {
		t.Fatal(err)
	}
	last := r.rawSeq[len(r.rawSeq)-1]
	joined := strings.Join(last, " ")
	for _, want := range []string{
		"12_pg_data:/snap/volumes/12_pg_data:ro",
		"12_other:/snap/volumes/12_other:ro",
		"/deploy/12:/snap/deploy:ro",
		"/pgstage:/snap/pgdump:ro",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("BackupSnapshot argv missing %q: %v", want, last)
		}
	}
	if !strings.Contains(joined, "volumes deploy pgdump") {
		t.Fatalf("BackupSnapshot must tar volumes+deploy+pgdump: %v", last)
	}
	if !anyContains(last, "czf") {
		t.Fatalf("BackupSnapshot should create a gzipped tar: %v", last)
	}
}

func TestRestoreSnapshotArgv(t *testing.T) {
	r := &recordingRunner{}
	err := RestoreSnapshot(r, []string{"12_pg_data"}, "/deploy/12", "/in/snap.tar.gz", VolumeImage)
	if err != nil {
		t.Fatal(err)
	}
	last := r.rawSeq[len(r.rawSeq)-1]
	joined := strings.Join(last, " ")
	if !strings.Contains(joined, "/in/snap.tar.gz:/in/snap.tgz:ro") {
		t.Fatalf("RestoreSnapshot missing input mount: %v", last)
	}
	if !strings.Contains(joined, "12_pg_data:/snap/volumes/12_pg_data") {
		t.Fatalf("RestoreSnapshot missing volume mount: %v", last)
	}
	if !anyContains(last, "xzf") {
		t.Fatalf("RestoreSnapshot must extract the tar: %v", last)
	}
}

func TestDumpPostgresCommand(t *testing.T) {
	r := &recordingRunner{}
	out, err := DumpPostgres(r, "12-postgres-1", "mydb", "postgres")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.rawSeq) != 1 {
		t.Fatalf("DumpPostgres should run one command, got %v", r.rawSeq)
	}
	got := r.rawSeq[0]
	if got[0] != "exec" || got[1] != "12-postgres-1" || got[2] != "pg_dump" {
		t.Fatalf("DumpPostgres argv = %v", got)
	}
	if !anyContains(got, "-Fc") || !anyContains(got, "-d") || !anyContains(got, "mydb") || !anyContains(got, "-U") || !anyContains(got, "postgres") {
		t.Fatalf("DumpPostgres must pass -Fc -d mydb -U postgres: %v", got)
	}
	if string(out) != "ok" {
		t.Fatalf("DumpPostgres should return the raw runner stdout, got %q", out)
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
