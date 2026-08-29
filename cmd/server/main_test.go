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

	id, err := d.CreateService("minio", "main")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetConfigItems(id, []db.ConfigItem{
		{Key: "MINIO_HOSTNAME", Value: "s3.example.com"},
		{Key: "MINIO_ROOT_USER", Value: "admin"},
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
	if !strings.Contains(string(out), "name: "+id+"_minio_data") {
		t.Fatalf("compose should mount the id-scoped volume name %s: %s", id+"_minio_data", out)
	}
	if !strings.Contains(string(out), "s3.example.com") {
		t.Fatalf("hostname from config should be preserved: %s", out)
	}

	// A stored volume name wins over the derived one (post-resize tracking).
	if err := d.SetConfigItems(id, []db.ConfigItem{{Key: "MINIO_VOLUME_NAME", Value: "custom_data"}}); err != nil {
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
		t.Fatalf("stored MINIO_VOLUME_NAME should be referenced: %s", out)
	}
}
