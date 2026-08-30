package services

import (
	"strings"
	"testing"
)

func TestPostgresRender(t *testing.T) {
	def, _ := Get(KindPostgres)
	res, err := def.Render(map[string]string{
		"POSTGRES_DB":          "app",
		"POSTGRES_USER":        "admin",
		"POSTGRES_PASSWORD":    "supersecret",
		"POSTGRES_PORT":        "5433",
		"POSTGRES_VOLUME_SIZE": "100G",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.ComposeYAML, "postgres:16-alpine") {
		t.Fatalf("expected postgres image: %q", res.ComposeYAML)
	}
	if !strings.Contains(res.ComposeYAML, `- "5433:5432"`) {
		t.Fatalf("expected configured host port: %q", res.ComposeYAML)
	}
	for _, want := range []string{"POSTGRES_DB: app", "POSTGRES_USER: admin", "POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}"} {
		if !strings.Contains(res.ComposeYAML, want) {
			t.Fatalf("compose missing %q: %q", want, res.ComposeYAML)
		}
	}
	if !strings.Contains(res.ComposeYAML, "pg_data:/var/lib/postgresql/data") {
		t.Fatalf("expected data volume mount: %q", res.ComposeYAML)
	}
	if !strings.Contains(res.DotEnv, "POSTGRES_PASSWORD=supersecret") {
		t.Fatalf("expected secret in dotenv: %q", res.DotEnv)
	}
	if !strings.Contains(res.DotEnv, "POSTGRES_VOLUME_SIZE=100G") {
		t.Fatalf("expected volume size in dotenv: %q", res.DotEnv)
	}
}

func TestPostgresRenderDefaults(t *testing.T) {
	def, _ := Get(KindPostgres)
	res, err := def.Render(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`- "5432:5432"`, "POSTGRES_DB: postgres", "POSTGRES_USER: postgres"} {
		if !strings.Contains(res.ComposeYAML, want) {
			t.Fatalf("default missing %q: %q", want, res.ComposeYAML)
		}
	}
}

func TestMinioRender(t *testing.T) {
	def, _ := Get(KindMinio)
	res, err := def.Render(map[string]string{
		"MINIO_HOSTNAME":      "s3.example.com",
		"MINIO_CONSOLE_URL":   "https://console.example.com",
		"MINIO_ROOT_USER":     "admin",
		"MINIO_ROOT_PASSWORD": "superSecret",
		"MINIO_VOLUME_SIZE":   "100G",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.DotEnv, "MINIO_ROOT_USER=admin") {
		t.Fatalf("missing env: %q", res.DotEnv)
	}
	if !strings.Contains(res.DotEnv, "MINIO_VOLUME_SIZE=100G") {
		t.Fatalf("missing volume size env: %q", res.DotEnv)
	}
	if !strings.Contains(res.ComposeYAML, "minio/minio") {
		t.Fatalf("expected minio image in compose: %q", res.ComposeYAML)
	}
	if !strings.Contains(res.ComposeYAML, "external: true") {
		t.Fatalf("expected minio data volume to be external: %q", res.ComposeYAML)
	}
	if !strings.Contains(res.ComposeYAML, "name: "+"minio_data") {
		t.Fatalf("expected external volume name in compose: %q", res.ComposeYAML)
	}
	if !strings.Contains(res.ComposeYAML, "MINIO_SERVER_URL: https://s3.example.com") {
		t.Fatalf("expected minio server url advertised from hostname: %q", res.ComposeYAML)
	}
	if !strings.Contains(res.ComposeYAML, "MINIO_BROWSER_REDIRECT_URL: https://console.example.com") {
		t.Fatalf("expected minio console url redirected from console url: %q", res.ComposeYAML)
	}
}

func TestMinioRenderNoHostname(t *testing.T) {
	def, _ := Get(KindMinio)
	res, err := def.Render(map[string]string{
		"MINIO_ROOT_USER":     "admin",
		"MINIO_ROOT_PASSWORD": "superSecret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.ComposeYAML, "MINIO_SERVER_URL") {
		t.Fatalf("no MINIO_SERVER_URL expected without a hostname: %q", res.ComposeYAML)
	}
	if strings.Contains(res.ComposeYAML, "MINIO_BROWSER_REDIRECT_URL") {
		t.Fatalf("no MINIO_BROWSER_REDIRECT_URL expected without a console url: %q", res.ComposeYAML)
	}
}

func TestCloudflaredRender(t *testing.T) {
	def, _ := Get(KindCloudflared)
	_, err := def.Render(map[string]string{
		"CF_TUNNEL":            "my-tunnel",
		"CF_INGRESS_0_HOST":    "s3.example.com",
		"CF_INGRESS_0_SERVICE": "",
	})
	if err == nil {
		t.Fatal("expected error for ingress rule missing target service")
	}
	if _, err := def.Render(map[string]string{}); err == nil {
		t.Fatal("expected error when tunnel name is missing")
	}
	res, err := def.Render(map[string]string{
		"CF_TUNNEL":            "my-tunnel",
		"CF_INGRESS_0_HOST":    "s3.example.com",
		"CF_INGRESS_0_SERVICE": "http://localhost:9000",
		"CF_INGRESS_1_HOST":    "mail.example.com",
		"CF_INGRESS_1_SERVICE": "http://localhost:8080",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.ComposeYAML, "cloudflare/cloudflared") {
		t.Fatalf("expected cloudflared image: %q", res.ComposeYAML)
	}
	if !strings.Contains(res.ComposeYAML, "command: tunnel run") {
		t.Fatalf("expected locally-managed tunnel run: %q", res.ComposeYAML)
	}
	if !strings.Contains(res.ComposeYAML, "network_mode: host") {
		t.Fatalf("expected host networking: %q", res.ComposeYAML)
	}
	for _, m := range []string{"./config.yml:/etc/cloudflared/config.yml:ro", "./creds.json:/etc/cloudflared/creds.json:ro"} {
		if !strings.Contains(res.ComposeYAML, m) {
			t.Fatalf("expected mount %q in compose: %q", m, res.ComposeYAML)
		}
	}
	// No extra files are persisted at save: the resolved config.yml + creds are
	// materialized at launch, once the tunnel id exists.
	if res.ComposeYAML == "" {
		t.Fatalf("save-time render must produce compose, got %+v", res)
	}
	if !strings.Contains(res.DotEnv, "CF_TUNNEL=my-tunnel") {
		t.Fatalf("expected tunnel name in dotenv: %q", res.DotEnv)
	}
}

func TestCloudflaredConfig(t *testing.T) {
	const id = "6ff42ae9-29c3-4b8f-93b2-5b2acd3e737d"
	cfg, err := CloudflaredConfig(id, map[string]string{
		"CF_INGRESS_0_HOST":    "s3.example.com",
		"CF_INGRESS_0_SERVICE": "http://localhost:9000",
		"CF_INGRESS_1_HOST":    "mail.example.com",
		"CF_INGRESS_1_SERVICE": "http://localhost:8080",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"tunnel: " + id,
		"credentials-file: /etc/cloudflared/creds.json",
		"- hostname: s3.example.com",
		"service: http://localhost:9000",
		"- hostname: mail.example.com",
		"service: http://localhost:8080",
		"- service: http_status:404",
	} {
		if !strings.Contains(cfg, want) {
			t.Fatalf("config.yml missing %q:\n%s", want, cfg)
		}
	}
	if strings.Index(cfg, "- hostname: s3.example.com") > strings.Index(cfg, "- hostname: mail.example.com") {
		t.Fatalf("ingress rules out of order:\n%s", cfg)
	}
	if strings.Index(cfg, "- service: http_status:404") < strings.Index(cfg, "mail.example.com") {
		t.Fatalf("catch-all rule must come last:\n%s", cfg)
	}
	if got := strings.Count(cfg, "service: http_status:404"); got != 1 {
		t.Fatalf("exactly one catch-all rule expected, got %d:\n%s", got, cfg)
	}
	if _, err := CloudflaredConfig(id, map[string]string{"CF_INGRESS_0_HOST": "only-host.example.com"}); err == nil {
		t.Fatal("expected error for ingress rule missing target service")
	}
}

func TestListRows(t *testing.T) {
	columns := []ListColumn{{Suffix: "HOST"}, {Suffix: "SERVICE"}}
	rows := ListRows(map[string]string{
		"CF_INGRESS_0_HOST":    "a.example.com",
		"CF_INGRESS_0_SERVICE": "http://localhost:80",
		"CF_INGRESS_2_HOST":    "c.example.com", // hole: scan stops at the first empty row
	}, "CF_INGRESS", columns)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d: %+v", len(rows), rows)
	}
	if rows[0]["HOST"] != "a.example.com" || rows[0]["SERVICE"] != "http://localhost:80" {
		t.Fatalf("unexpected row: %+v", rows[0])
	}
	if got := ListRows(nil, "CF_INGRESS", columns); len(got) != 0 {
		t.Fatalf("nil map should yield no rows, got %+v", got)
	}
}
