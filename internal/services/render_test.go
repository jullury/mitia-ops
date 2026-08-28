package services

import (
	"strings"
	"testing"
)

func TestMinioRender(t *testing.T) {
	def, _ := Get(KindMinio)
	res, err := def.Render(map[string]string{
		"MINIO_HOSTNAME":      "s3.example.com",
		"MINIO_ROOT_USER":     "admin",
		"MINIO_ROOT_PASSWORD": "superSecret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.DotEnv, "MINIO_ROOT_USER=admin") {
		t.Fatalf("missing env: %q", res.DotEnv)
	}
	if !strings.Contains(res.ComposeYAML, "minio/minio") {
		t.Fatalf("expected minio image in compose: %q", res.ComposeYAML)
	}
}

func TestCloudflaredRender(t *testing.T) {
	def, _ := Get(KindCloudflared)
	res, err := def.Render(map[string]string{"CF_TUNNEL_TOKEN": "tok123"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.DotEnv, "CF_TUNNEL_TOKEN=tok123") {
		t.Fatalf("missing tunnel token env: %q", res.DotEnv)
	}
	if !strings.Contains(res.ComposeYAML, "cloudflare/cloudflared") {
		t.Fatalf("expected cloudflared image: %q", res.ComposeYAML)
	}
}
