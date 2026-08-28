package services

func init() {
	register(Definition{
		Kind:  KindCloudflared,
		Label: "Cloudflare Tunnel",
		Fields: []Field{
			{Key: "CF_TUNNEL_TOKEN", Label: "Tunnel token", Type: FieldSecret, Hints: "cloudflared tunnel token"},
		},
		Render: func(values map[string]string) (RenderResult, error) {
			return RenderResult{
				DotEnv:      "CF_TUNNEL_TOKEN=" + values["CF_TUNNEL_TOKEN"] + "\n",
				ComposeYAML: cloudflaredCompose(values),
			}, nil
		},
	})
}

func cloudflaredCompose(v map[string]string) string {
	return `services:
  cloudflared:
    image: cloudflare/cloudflared:latest
    restart: unless-stopped
    command: tunnel run --token ${CF_TUNNEL_TOKEN}
`
}
