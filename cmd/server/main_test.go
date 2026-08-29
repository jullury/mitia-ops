package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDotEnv(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".env")
	os.WriteFile(p, []byte("# comment\n\nFOO=bar\nEMPTY=\n"), 0o600)
	os.Setenv("EMPTY", "already-set")

	loadDotEnv(p)

	if got := os.Getenv("FOO"); got != "bar" {
		t.Errorf("FOO = %q, want %q", got, "bar")
	}
	if got := os.Getenv("EMPTY"); got != "already-set" {
		t.Errorf("EMPTY overridden to %q; real env must win", got)
	}
}
