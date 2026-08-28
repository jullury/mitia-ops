package services

import "strings"

func init() {
	register(Definition{
		Kind:  KindMailcow,
		Label: "Mailcow (mail server)",
		Fields: []Field{
			{Key: "MAILCOW_HOSTNAME", Label: "Hostname (FQDN)", Type: FieldString, Placeholder: "mail.example.com"},
			{Key: "MAILCOW_TZ", Label: "Timezone", Type: FieldString, Placeholder: "Europe/Paris", Hints: "IANA tz name"},
			{Key: "MAILCOW_HTTP_PORT", Label: "HTTP port", Type: FieldString, Placeholder: "8080"},
			{Key: "MAILCOW_HTTPS_PORT", Label: "HTTPS port", Type: FieldString, Placeholder: "8443"},
		},
		Render: func(values map[string]string) (RenderResult, error) {
			return RenderResult{DotEnv: mailcowEnv(values)}, nil
		},
	})
}

func mailcowEnv(v map[string]string) string {
	var b strings.Builder
	for _, k := range []string{"MAILCOW_HOSTNAME", "MAILCOW_TZ", "MAILCOW_HTTP_PORT", "MAILCOW_HTTPS_PORT"} {
		if val, ok := v[k]; ok {
			b.WriteString(k + "=" + val + "\n")
		}
	}
	return b.String()
}
