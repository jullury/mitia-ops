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
	msg := strings.TrimSpace(string(out))
	if err != nil {
		return msg, fmt.Errorf("docker compose: %w: %s", err, msg)
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
