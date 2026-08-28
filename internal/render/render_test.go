package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jullury/mitia-ops/internal/services"
)

func TestDotEnvSorted(t *testing.T) {
	got := DotEnv(map[string]string{
		"B": "2",
		"A": "1",
		"C": "3",
	})
	want := "A=1\nB=2\nC=3"
	if strings.TrimSpace(got) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestWriteComposeWritesOnlyCompose(t *testing.T) {
	dir := t.TempDir()
	res := services.RenderResult{
		DotEnv:      "A=1\nSECRET=x\n",
		ComposeYAML: "services:\n  x:\n    image: nginx\n",
	}
	if err := WriteCompose(dir, res); err != nil {
		t.Fatal(err)
	}
	// .env must NOT be persisted by WriteCompose
	if _, err := os.Stat(filepath.Join(dir, ".env")); !os.IsNotExist(err) {
		t.Fatal("WriteCompose must not write a persistent .env")
	}
	cB, err := os.ReadFile(filepath.Join(dir, "docker-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cB), "image: nginx") {
		t.Fatalf("unexpected compose: %q", string(cB))
	}
}

func TestWriteEnvFileAndRemove(t *testing.T) {
	dir := t.TempDir()
	path, err := WriteEnvFile(dir, map[string]string{"A": "1", "SECRET": "x"})
	if err != nil {
		t.Fatal(err)
	}
	envB, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(envB), "A=1\n") || !strings.Contains(string(envB), "SECRET=x\n") {
		t.Fatalf("unexpected .env: %q", string(envB))
	}
	if err := RemoveEnvFile(dir); err != nil {
		t.Fatal(err)
	}
	// idempotent: removing twice is fine
	if err := RemoveEnvFile(dir); err != nil {
		t.Fatalf("RemoveEnvFile should be idempotent: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("expected .env to be removed")
	}
}