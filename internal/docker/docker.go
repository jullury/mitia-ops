package docker

import (
	"fmt"
	"os/exec"
	"strings"
)

type Runner interface {
	Run(dir string, args ...string) (string, error)
}

type CLI struct{}

func NewCLI() Runner { return &CLI{} }

func (c *CLI) Run(dir string, args ...string) (string, error) {
	full := append([]string{"compose"}, args...)
	cmd := exec.Command("docker", full...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return strings.TrimSpace(string(out)), fmt.Errorf("docker compose: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func composeCmd(fn func(Runner, string, ...string) (string, error), dir string, r Runner, composeArgs ...string) (string, error) {
	return fn(r, dir, composeArgs...)
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