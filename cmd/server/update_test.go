package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestIsUpdate(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{"update"}, true},
		{[]string{"update", "--yes"}, true},
		{[]string{}, false},
		{[]string{"serve"}, false},
		{[]string{"uninstall"}, false},
		{[]string{"--version"}, false},
	}
	for _, c := range cases {
		if got := isUpdate(c.args); got != c.want {
			t.Errorf("isUpdate(%q) = %v, want %v", c.args, got, c.want)
		}
	}
}

func TestReleaseAsset(t *testing.T) {
	asset, err := releaseAsset()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(asset, "mitia-ops-") {
		t.Fatalf("releaseAsset() = %q, want a mitia-ops-* asset", asset)
	}
}

// TestUpdateAcrossFilesystems guards the same-filesystem rename invariant:
// /tmp (tmpfs) and the real install dir (/usr/local/bin) are often on different
// mounts, so os.Rename across them fails with EXDEV ("invalid cross-device
// link"). update() must therefore stage the download next to the target and
// rename within that SAME directory. We can only exercise the cross-device
// path on a host that actually has a separate mount; use /dev/shm (tmpfs,
// distinct from /) when present and skip otherwise.
func TestUpdateAcrossFilesystems(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("rename semantics differ on Windows")
	}
	shm := "/dev/shm"
	if fi, err := os.Stat(shm); err != nil || !fi.IsDir() {
		t.Skipf("no separate mount (%s missing); nothing to cross", shm)
	}
	shmDev, err := os.Stat(shm)
	if err != nil {
		t.Skip("cannot stat /dev/shm")
	}
	if dir := t.TempDir(); os.SameFile(shmDev, mustStat(t, dir)) {
		t.Skipf("%s is on the same filesystem as t.TempDir(); nothing to cross", shm)
	}

	// Target is on the foreign mount; the old code staged in os.TempDir() (the
	// root filesystem's TMPDIR), so renaming into /dev/shm crossed devices and
	// failed. The fix stages inside the target dir, which stays on /dev/shm.
	foreignDir, err := os.MkdirTemp(shm, "mitia-ops-update-test-")
	if err != nil {
		t.Skip("cannot create dir on foreign mount")
	}
	t.Cleanup(func() { os.RemoveAll(foreignDir) })
	t.Setenv("TMPDIR", t.TempDir()) // keep os.TempDir() on the root fs, NOT /dev/shm

	old := updateBinPath
	updateBinPath = filepath.Join(foreignDir, "mitia-ops")
	defer func() { updateBinPath = old }()

	os.WriteFile(updateBinPath, []byte("#!/bin/sh\nprintf 'v1.4.1'\n"), 0o755)

	oldRoot := updateCheckRoot
	updateCheckRoot = func() error { return nil }
	defer func() { updateCheckRoot = oldRoot }()

	oldFetch := fetchFunc
	fetchFunc = fakeFetch
	defer func() { fetchFunc = oldFetch }()

	oldRestart := restartFunc
	restartFunc = func(string) {}
	defer func() { restartFunc = oldRestart }()

	if err := update([]string{"update"}); err != nil {
		t.Fatalf("update across filesystems failed: %v", err)
	}
	if got := currentVersion(); got != "vTest-1.9.9" {
		t.Fatalf("installed version = %q, want %q", got, "vTest-1.9.9")
	}
}

func mustStat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi
}

// fakeFetch simulates the network by writing a sentinel binary that records its
// own "version" (so currentVersion can read it back), then lets update() install
// and (stubbed) restart it.
func fakeFetch(url, dest string) error {
	body := "#!/bin/sh\nprintf 'vTest-1.9.9'\n"
	tmp := dest + ".src"
	if err := os.WriteFile(tmp, []byte(body), 0o755); err != nil {
		return err
	}
	// update() chmods and renames `dest`, which must already exist for rename on
	// some filesystems only; copy to dest so the rename target is valid.
	return os.Rename(tmp, dest)
}

func TestUpdateInstallsLatest(t *testing.T) {
	dir := t.TempDir()
	old := updateBinPath
	updateBinPath = filepath.Join(dir, "mitia-ops")
	defer func() { updateBinPath = old }()

	// A pre-existing "installed" binary reporting v1.4.1.
	os.WriteFile(updateBinPath, []byte("#!/bin/sh\nprintf 'v1.4.1'\n"), 0o755)

	oldRoot := updateCheckRoot
	updateCheckRoot = func() error { return nil }
	defer func() { updateCheckRoot = oldRoot }()

	oldFetch := fetchFunc
	fetchFunc = fakeFetch
	defer func() { fetchFunc = oldFetch }()

	oldRestart := restartFunc
	restartCalled := false
	restartFunc = func(string) { restartCalled = true }
	defer func() { restartFunc = oldRestart }()

	if err := update([]string{"update"}); err != nil {
		t.Fatal(err)
	}
	if !restartCalled {
		t.Fatal("expected the service restart stub to be called")
	}
	if got := currentVersion(); got != "vTest-1.9.9" {
		t.Fatalf("installed version = %q, want %q", got, "vTest-1.9.9")
	}
}

func TestUpdateMissingBinary(t *testing.T) {
	dir := t.TempDir()
	old := updateBinPath
	updateBinPath = filepath.Join(dir, "missing")
	defer func() { updateBinPath = old }()

	if err := update([]string{"update"}); err == nil {
		t.Fatal("expected an error when the installed binary is missing")
	}
}
