package docker

import (
	"fmt"
	"os/exec"
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

func Up(dir string, r Runner) (string, error) {
	return r.Run(dir, "up", "-d")
}

func Down(dir string, r Runner) (string, error) {
	return r.Run(dir, "down")
}

func Restart(dir string, r Runner) (string, error) {
	return r.Run(dir, "restart")
}
