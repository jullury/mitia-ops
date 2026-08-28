package services

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

func caddyEnv(v map[string]string) string { return "" }

func caddyCompose(v map[string]string) string { return "" }
