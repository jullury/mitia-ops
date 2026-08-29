package docker

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type Runner interface {
	Run(dir string, args ...string) (string, error)
}

// RawRunner runs plain `docker ...` commands (volume/run) that are outside the
// compose subcommand surface, used for volume backup/restore/removal.
type RawRunner interface {
	RunRaw(args ...string) (string, error)
}

type CLI struct{}

func NewCLI() *CLI { return &CLI{} }

func (c *CLI) Run(dir string, args ...string) (string, error) {
	full := append([]string{"compose"}, args...)
	return runDocker(dir, full, "docker compose")
}

func (c *CLI) RunRaw(args ...string) (string, error) {
	return runDocker("", args, "docker")
}

func runDocker(dir string, args []string, prefix string) (string, error) {
	cmd := exec.Command("docker", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	msg := strings.TrimSpace(string(out))
	if err != nil {
		return msg, fmt.Errorf("%s: %w: %s", prefix, err, msg)
	}
	return msg, nil
}

func Status(dir string, r Runner) (string, error) {
	return r.Run(dir, "ps", "--format", "json")
}

// Up ensures the service is running with the current on-disk config. The
// deployment files (e.g. cloudflared's config.yml) are rewritten just before
// `up` and are bind-mounted, so an unchanged-spec container must be recreated
// for those content changes to take effect ("Start" must apply saved edits).
func Up(dir string, r Runner) (string, error) {
	return r.Run(dir, "up", "-d", "--force-recreate")
}

func Down(dir string, r Runner) (string, error) {
	return r.Run(dir, "down")
}

func Restart(dir string, r Runner) (string, error) {
	return r.Run(dir, "restart")
}

// RemoveDeployDir removes a deploy directory that may contain root-owned
// artifacts (e.g. files created by docker compose mounts launched as root). If
// a plain host-side removal as the current user fails (permission denied on
// those files), it falls back to deleting through a disposable container
// running as root, which can unlink files regardless of owner. image must be a
// locally-available (or pullable) image with a `rm` binary; when empty it
// defaults to "alpine". raw is used only for the fallback and may be nil when a
// plain removal succeeds.
func RemoveDeployDir(dir, image string, raw RawRunner) error {
	if err := os.RemoveAll(dir); err == nil || raw == nil {
		return err
	}
	if image == "" {
		image = "alpine"
	}
	// `docker run -v` requires an absolute host path; resolve relative deploy
	// dirs (which are cwd-relative in this app) before mounting.
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	// Bind-mount the PARENT dir, then rm -rf the child by name. Removing the
	// child is a normal unlink (not the bind mountpoint, which would hit EBUSY),
	// and root inside the container can delete files regardless of ownership.
	parent := filepath.Dir(abs)
	base := filepath.Base(abs)
	_, err = raw.RunRaw("run", "--rm", "-v", parent+":/__p", image, "rm", "-rf", "/__p/"+base)
	if err != nil {
		return err
	}
	// The dir is now gone (removed through the parent mount); clear the empty
	// path entry best-effort in case anything remained.
	_ = os.Remove(dir)
	return nil
}
