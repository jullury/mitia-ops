package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jullury/mitia-ops/internal/db"
)

func TestLoadDotEnv(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".env")
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

func TestRegenerateComposePinsMigratedVolume(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	id, err := d.CreateService("garage", "main")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetConfigItems(id, []db.ConfigItem{
		{Key: "GARAGE_HOSTNAME", Value: "s3.example.com"},
	}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "deployments", id)

	if err := regenerateCompose(d, dir, id); err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(filepath.Join(dir, "docker-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "name: "+id+"_garage_data") {
		t.Fatalf("compose should mount the id-scoped volume name %s: %s", id+"_garage_data", out)
	}
	if !strings.Contains(string(out), "garage_data:/srv/garage") {
		t.Fatalf("compose should mount garage_data to /srv/garage: %s", out)
	}

	// A stored volume name wins over the derived one (post-resize tracking).
	if err := d.SetConfigItems(id, []db.ConfigItem{{Key: "GARAGE_VOLUME_NAME", Value: "custom_data"}}); err != nil {
		t.Fatal(err)
	}
	if err := regenerateCompose(d, dir, id); err != nil {
		t.Fatal(err)
	}
	out, err = os.ReadFile(filepath.Join(dir, "docker-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "name: custom_data") {
		t.Fatalf("stored GARAGE_VOLUME_NAME should be referenced: %s", out)
	}
}
