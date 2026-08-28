package services

func init() {
	register(Definition{
		Kind:     KindMailcow,
		Label:    "Mailcow (mail server)",
		ReadOnly: true,
		Fields: []Field{
			{
				Key:         "MAILCOW_HTTP_PORT",
				Label:       "HTTP port",
				Type:        FieldString,
				Placeholder: "8080",
				Hints:       "public port; config URL is http://localhost:<port>",
			},
		},
		Render: func(values map[string]string) (RenderResult, error) {
			return RenderResult{}, nil
		},
		ConfigURL: func(values map[string]string) string {
			if p := values["MAILCOW_HTTP_PORT"]; p != "" {
				return "http://localhost:" + p
			}
			return ""
		},
	})
}
