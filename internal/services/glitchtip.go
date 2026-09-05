package services

import "strings"

// GlitchTip bundle: the web service owns an internal postgres (its database)
// and valkey (task queue, cache, sessions). The Django SECRET_KEY and the
// postgres password are auto-generated at first launch (stored encrypted in
// the app's SQLite config) and reach the containers as ${…} placeholders
// resolved from the ephemeral launch-time .env — never persisted in compose.
func init() {
	register(Definition{
		Kind:  KindGlitchTip,
		Label: "GlitchTip (error tracking)",
		Fields: []Field{
			{Key: "GLITCHTIP_DOMAIN", Label: "Domain", Type: FieldString, Placeholder: "https://glitchtip.example.com", Hints: "public URL incl. scheme; defaults to http://localhost:<port> when blank"},
			{Key: "GLITCHTIP_PORT", Label: "Host port", Type: FieldString, Placeholder: "8000"},
			{Key: "EMAIL_URL", Label: "SMTP URL", Type: FieldSecret, Placeholder: "consolemail://", Hints: "e.g. smtp://user:pass@host:port; blank uses consolemail:// (mail goes to the logs)"},
			{Key: "DEFAULT_FROM_EMAIL", Label: "From email", Type: FieldString, Placeholder: "errors@example.com"},
			{
				Key:   "GLITCHTIP_VOLUME_SIZE",
				Label: "Volume size limit",
				Type:  FieldSize,
				Hints: "soft upper bound; guards launch only — refuse to start when the disk can't hold it together with every other service's declared size (Docker local volumes cannot enforce a size)",
				Units: []SizeUnit{
					{Label: "MiB", Suffix: "M", Base: 1 << 20},
					{Label: "GiB", Suffix: "G", Base: 1 << 30},
					{Label: "TiB", Suffix: "T", Base: 1 << 40},
				},
			},
		},
		Render: func(values map[string]string) (RenderResult, error) {
			return RenderResult{ComposeYAML: glitchTipCompose(values)}, nil
		},
		ConfigURL: func(values map[string]string) string {
			if domain := strings.TrimSpace(values["GLITCHTIP_DOMAIN"]); domain != "" {
				return domain
			}
			port := strings.TrimSpace(values["GLITCHTIP_PORT"])
			if port == "" {
				port = "8000"
			}
			return "http://localhost:" + port
		},
	})
}

// GlitchTipSecretKeyKey and GlitchTipDBPasswordKey are the app-persisted
// GlitchTip secrets (generated at first launch, stored encrypted) whose
// plaintext must be present in the launch-time values so the compose
// placeholders resolve. Exposed for the web layer's prepare hook.
const (
	GlitchTipSecretKeyKey  = "GLITCHTIP_SECRET_KEY"
	GlitchTipDBPasswordKey = "GLITCHTIP_DB_PASSWORD"
)

func glitchTipCompose(v map[string]string) string {
	port := strings.TrimSpace(v["GLITCHTIP_PORT"])
	if port == "" {
		port = "8000"
	}
	domain := strings.TrimSpace(v["GLITCHTIP_DOMAIN"])
	if domain == "" {
		domain = "http://localhost:" + port
	}
	fromEmail := strings.TrimSpace(v["DEFAULT_FROM_EMAIL"])

	var b strings.Builder
	b.WriteString(`services:
  web:
    image: ` + glitchTipImage + `
    restart: unless-stopped
    depends_on:
      - postgres
      - valkey
    ports:
      - "` + port + `:8000"
    environment:
      SERVER_ROLE: all_in_one
      SECRET_KEY: ${GLITCHTIP_SECRET_KEY}
      EMAIL_URL: ${EMAIL_URL}
      GLITCHTIP_DOMAIN: ` + domain + "\n")
	if fromEmail != "" {
		b.WriteString("      DEFAULT_FROM_EMAIL: " + fromEmail + "\n")
	}
	b.WriteString(`      DATABASE_URL: postgres://postgres:${GLITCHTIP_DB_PASSWORD}@postgres:5432/glitchtip
      VALKEY_URL: redis://valkey:6379
    volumes:
      - uploads:/code/uploads
  postgres:
    image: ` + postgresImage + `
    restart: unless-stopped
    environment:
      POSTGRES_DB: glitchtip
      POSTGRES_PASSWORD: ${GLITCHTIP_DB_PASSWORD}
    volumes:
      - pg-data:/var/lib/postgresql
  valkey:
    image: ` + valkeyImage + `
    restart: unless-stopped
volumes:
  pg-data:
  uploads:
`)
	return b.String()
}
