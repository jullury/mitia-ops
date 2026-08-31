package services

import (
	"errors"
	"fmt"
	"strings"

	"github.com/jullury/mitia-ops/internal/cloudflared"
)

// ingressColumns are the per-row inputs of the CF_INGRESS traffic-routing list.
var ingressColumns = []ListColumn{
	{Suffix: "HOST", Label: "Hostname", Placeholder: "app.example.com"},
	{Suffix: "SERVICE", Label: "Target service", Placeholder: "http://localhost:8080"},
}

func init() {
	register(Definition{
		Kind:  KindCloudflared,
		Label: "Cloudflare Tunnel",
		Fields: []Field{
			{
				Key:         "CF_TUNNEL",
				Label:       "Tunnel name",
				Type:        FieldString,
				Placeholder: "my-tunnel",
				Hints:       "created automatically on first start (the app runs cloudflared through a cloudflare/cloudflared container); if a login is needed it prompts you with the command to run",
			},
			{
				Key:     "CF_INGRESS",
				Label:   "Traffic routing",
				Type:    FieldList,
				Hints:   "send a hostname's traffic to a local service; each hostname needs a DNS CNAME to <tunnel-id>.cfargotunnel.com in the Cloudflare dashboard",
				Columns: ingressColumns,
			},
		},
		Render: cloudflaredRender,
	})
}

func cloudflaredRender(values map[string]string) (RenderResult, error) {
	if strings.TrimSpace(values["CF_TUNNEL"]) == "" {
		return RenderResult{}, errors.New("tunnel name is required")
	}
	// Validate the ingress rules at save time so mistakes surface inline. The
	// real config.yml (with the resolved tunnel id) is written at launch, once
	// the tunnel has been created.
	if _, err := CloudflaredConfig("00000000-0000-0000-0000-000000000000", values); err != nil {
		return RenderResult{}, err
	}
	return RenderResult{
		DotEnv:      "CF_TUNNEL=" + strings.TrimSpace(values["CF_TUNNEL"]) + "\n",
		ComposeYAML: cloudflaredCompose(),
	}, nil
}

// CloudflaredConfig renders a cloudflared config.yml for a locally-managed
// tunnel with the given resolved tunnel id. Every rule must carry both a
// hostname and a target service; cloudflared requires the final ingress rule
// to be a catch-all (no hostname), so one (http_status:404) is always appended.
func CloudflaredConfig(tunnelID string, values map[string]string) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "tunnel: %s\n", tunnelID)
	b.WriteString("credentials-file: /etc/cloudflared/creds.json\n")
	b.WriteString("ingress:\n")
	rows := ListRows(values, "CF_INGRESS", ingressColumns)
	for i, row := range rows {
		host, svc := row["HOST"], row["SERVICE"]
		if (host == "") != (svc == "") {
			return "", fmt.Errorf("ingress rule %d: hostname and target service must both be set", i+1)
		}
		fmt.Fprintf(&b, "  - hostname: %s\n    service: %s\n", host, svc)
	}
	b.WriteString("  - service: http_status:404\n")
	return b.String(), nil
}

// CloudflaredIngressHosts returns the ordered, non-empty hostnames of the
// saved ingress rules — the DNS entries the app must route to the tunnel.
func CloudflaredIngressHosts(values map[string]string) []string {
	var hosts []string
	for _, row := range ListRows(values, "CF_INGRESS", ingressColumns) {
		if h := strings.TrimSpace(row["HOST"]); h != "" {
			hosts = append(hosts, h)
		}
	}
	return hosts
}

func cloudflaredCompose() string {
	return `services:
  cloudflared:
    image: ` + cloudflared.CloudflaredImage + `
    restart: unless-stopped
    command: tunnel run
    network_mode: host
    volumes:
      - ./config.yml:/etc/cloudflared/config.yml:ro
      - ./creds.json:/etc/cloudflared/creds.json:ro
`
}
