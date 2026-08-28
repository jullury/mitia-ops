package services

func init() {
	register(Definition{
		Kind:  KindCloudflared,
		Label: "Cloudflare Tunnel",
		Fields: []Field{
			{Key: "CF_TUNNEL_TOKEN", Label: "Tunnel token", Type: FieldSecret, Hints: "cloudflared tunnel token"},
		},
		Render: func(values map[string]string) (RenderResult, error) {
			return RenderResult{ComposeYAML: cloudflaredCompose(values)}, nil
		},
	})
}

func cloudflaredCompose(v map[string]string) string { return "" }
