package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// updatePaths mirror the locations scripts/install.sh writes, so `update`
// replaces exactly what a release install placed and a real install restarted.
// They are vars so tests can point them at a temp dir.
var (
	updateBinPath = "/usr/local/bin/mitia-ops"
	updateUnit    = "/etc/systemd/system/mitia-ops.service"
	releasesURL   = "https://github.com/jullury/mitia-ops/releases/latest/download/"
)

// restartFunc is `systemctl restart mitia-ops`; a var so tests can stub it.
var restartFunc = func(unit string) {
	if _, err := os.Stat(unit); errors.Is(err, os.ErrNotExist) {
		fmt.Println("note: no systemd unit found; new binary in place for next start")
		return
	}
	cmd := exec.Command("systemctl", "restart", "mitia-ops")
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Printf("note: systemctl restart mitia-ops: %v: %s\n", err, strings.TrimSpace(string(out)))
	}
}

// updateCheckRoot enforces the root requirement; a var so tests can bypass it.
var updateCheckRoot = func() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("update must run as root (e.g. 'sudo %s update')", binName())
	}
	return nil
}

// isUpdate reports whether the binary was invoked as `mitia-ops update`.
func isUpdate(args []string) bool {
	return len(args) > 0 && args[0] == "update"
}

// fetchFunc downloads a URL to a temp file. It is a var so tests can stub it.
var fetchFunc = func(url, dest string) error {
	// curl(1) is used when present because it is preinstalled on the hosts this
	// targets; fall back to Go's http client otherwise (e.g. a stripped image).
	if p, err := exec.LookPath("curl"); err == nil {
		cmd := exec.Command(p, "-fsSL", "-o", dest, url)
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}
	return fetchHTTP(url, dest)
}

// fetchHTTP downloads url to dest with the standard library. KeepAlive is
// disabled so the process can exit promptly after a single fetch.
func fetchHTTP(url, dest string) error {
	client := &http.Client{Timeout: 2 * time.Minute}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, resp.Body)
	return err
}

// releaseAsset returns the latest-release asset name for the running OS/arch,
// matching the naming in .github/workflows/release.yml.
func releaseAsset() (string, error) {
	switch runtime.GOOS + ":" + runtime.GOARCH {
	case "linux:amd64":
		return "mitia-ops-linux-amd64", nil
	case "linux:arm64":
		return "mitia-ops-linux-arm64", nil
	case "linux:arm":
		return "mitia-ops-linux-arm", nil
	case "darwin:amd64":
		return "mitia-ops-darwin-amd64", nil
	case "darwin:arm64":
		return "mitia-ops-darwin-arm64", nil
	}
	return "", fmt.Errorf("unsupported platform: %s/%s", runtime.GOOS, runtime.GOARCH)
}

// update re-runs the same operation the FORCE_DOWNLOAD one-liner performs: it
// fetches the latest release binary, replaces /usr/local/bin/mitia-ops, and
// restarts the systemd unit so the new version takes effect. Must run as root
// (sudo), like `uninstall`. On a non-systemd host it still swaps the binary.
func update(args []string) error {
	if err := updateCheckRoot(); err != nil {
		return err
	}

	asset, err := releaseAsset()
	if err != nil {
		return err
	}
	url := releasesURL + asset

	if _, err := os.Stat(updateBinPath); err != nil {
		return fmt.Errorf("%s not found — install mitia-ops first (not a deployment)", updateBinPath)
	}
	old := currentVersion()

	// Stage the download in a temp file NEXT TO the target and rename within
	// that same directory. Renaming across mounts (e.g. the tmpfs /tmp to the
	// on-disk /usr/local/bin) fails with "invalid cross-device link"; staging
	// here keeps the rename on one filesystem.
	tmp, err := os.CreateTemp(filepath.Dir(updateBinPath), ".mitia-ops-update-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Close(); err != nil {
		return err
	}

	fmt.Printf("downloading %s\n", url)
	if err := fetchFunc(url, tmpPath); err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, updateBinPath); err != nil {
		return err
	}
	// The daemon is still running the previous, now-unlinked inode until we
	// restart it, so bring the new binary up.
	restartFunc(updateUnit)

	ver := currentVersion()
	fmt.Printf("updated mitia-ops %s -> %s\n", old, ver)
	fmt.Printf("rerun with 'sudo %s update' to check again; version now %s\n", binName(), ver)
	return nil
}

// currentVersion reports the version the installed binary claims via
// `--version` (empty if it cannot run or reports nothing, e.g. pre-v1.4.0).
func currentVersion() string {
	cmd := exec.Command(updateBinPath, "--version")
	out, err := cmd.Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}
