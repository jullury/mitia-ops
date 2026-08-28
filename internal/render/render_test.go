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

func TestDotEnvQuotesUnsafeValues(t *testing.T) {
	got := DotEnv(map[string]string{
		"SECRET": `pa$$ "x" #hash \tick`,
		"NAME":   "plain_value",
	})
	if !strings.Contains(got, "NAME=plain_value\n") {
		t.Fatalf("safe value must stay unquoted: %q", got)
	}
	// unsafe value must be double-quoted with \, ", ` and $ escaped
	want := "SECRET=\"pa\\$\\$ \\\"x\\\" #hash \\\\tick\"\n"
	if !strings.Contains(got, want) {
		t.Fatalf("unsafe value must be quoted and escaped:\ngot:  %q\nwant: %q", got, want)
	}
	if strings.Contains(got, "SECRET=pa$$") {
		t.Fatalf("secret must not be emitted raw: %q", got)
	}
}

func TestDotEnvRefusesNewlineValues(t *testing.T) {
	got := DotEnv(map[string]string{
		"A": "line1\nline2",
		"B": "a\rb",
		"C": "ok",
	})
	if strings.Contains(got, "\nline2") || strings.Contains(got, "\r") {
		t.Fatalf("newline/carriage-return value must be omitted, not emitted raw: %q", got)
	}
	if strings.Contains(got, "A=") || strings.Contains(got, "B=") {
		t.Fatalf("keys with multiline values must be omitted entirely: %q", got)
	}
	if !strings.Contains(got, "C=ok\n") {
		t.Fatalf("valid values must still be emitted: %q", got)
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

func TestBuildRenderResultEmptyForReadOnlyMailcow(t *testing.T) {
	res, err := BuildRenderResult(services.KindMailcow, map[string]string{"MAILCOW_HTTP_PORT": "8080"})
	if err != nil {
		t.Fatal(err)
	}
	if res.ComposeYAML != "" {
		t.Fatalf("mailcow must not emit compose, got %q", res.ComposeYAML)
	}
	if res.DotEnv != "" {
		t.Fatalf("mailcow must not emit a dotenv (no compose payload), got %q", res.DotEnv)
	}
}

func TestBuildRenderResultDotEnvFallbackWhenRendererEmitsNone(t *testing.T) {
	res, err := BuildRenderResult(services.KindMinio, map[string]string{"EXTRA": "1"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(res.ComposeYAML) == "" {
		t.Fatal("minio must carry a compose payload for the fallback to engage")
	}
	if !strings.Contains(res.DotEnv, "EXTRA=1") {
		t.Fatalf("fallback must fill DotEnv from values even when the renderer emits none, got %q", res.DotEnv)
	}
}
