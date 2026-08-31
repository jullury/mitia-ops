package services

import "strings"

func init() {
	register(Definition{
		Kind:  KindCaddy,
		Label: "Caddy (reverse proxy + TLS)",
		Fields: []Field{
			{Key: "CADDY_DOMAIN", Label: "Primary domain", Type: FieldString, Placeholder: "example.com"},
			{Key: "CADDY_EMAIL", Label: "ACME contact email", Type: FieldString, Placeholder: "admin@example.com"},
		},
		Render: func(values map[string]string) (RenderResult, error) {
			return RenderResult{DotEnv: caddyEnv(values), ComposeYAML: caddyCompose(values)}, nil
		},
	})
}

func caddyEnv(v map[string]string) string {
	var b strings.Builder
	for _, k := range []string{"CADDY_DOMAIN", "CADDY_EMAIL"} {
		if val, ok := v[k]; ok {
			b.WriteString(k + "=" + val + "\n")
		}
	}
	return b.String()
}

func caddyCompose(v map[string]string) string {
	return `services:
  caddy:
    image: ` + caddyImage + `
    restart: unless-stopped
    ports:
      - "80:80"
      - "443:443"
    environment:
      DOMAIN: ${CADDY_DOMAIN}
      ACME_EMAIL: ${CADDY_EMAIL}
    volumes:
      - caddy_data:/data
      - caddy_config:/config
volumes:
  caddy_data:
  caddy_config:
`
}
