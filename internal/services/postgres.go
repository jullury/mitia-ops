package services

import "strings"

func init() {
	register(Definition{
		Kind:  KindPostgres,
		Label: "Postgres",
		Fields: []Field{
			{Key: "POSTGRES_DB", Label: "Database", Type: FieldString, Placeholder: "postgres", Hints: "the default database, created on first init; add more users/databases via psql"},
			{Key: "POSTGRES_USER", Label: "User", Type: FieldString, Placeholder: "postgres"},
			{Key: "POSTGRES_PASSWORD", Label: "Password", Type: FieldSecret, Hints: ">= 8 chars"},
			{Key: "POSTGRES_PORT", Label: "Host port", Type: FieldString, Placeholder: "5432"},
			{
				Key:   "POSTGRES_VOLUME_SIZE",
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
			return RenderResult{DotEnv: postgresEnv(values), ComposeYAML: postgresCompose(values)}, nil
		},
	})
}

func postgresEnv(v map[string]string) string {
	var b strings.Builder
	for _, k := range []string{"POSTGRES_DB", "POSTGRES_USER", "POSTGRES_PASSWORD", "POSTGRES_PORT", "POSTGRES_VOLUME_SIZE"} {
		if val, ok := v[k]; ok {
			b.WriteString(k + "=" + val + "\n")
		}
	}
	return b.String()
}

func postgresCompose(v map[string]string) string {
	db := strings.TrimSpace(v["POSTGRES_DB"])
	if db == "" {
		db = "postgres"
	}
	user := strings.TrimSpace(v["POSTGRES_USER"])
	if user == "" {
		user = "postgres"
	}
	port := strings.TrimSpace(v["POSTGRES_PORT"])
	if port == "" {
		port = "5432"
	}
	return `services:
  postgres:
    image: postgres:16-alpine
    restart: unless-stopped
    ports:
      - "` + port + `:5432"
    environment:
      POSTGRES_DB: ` + db + `
      POSTGRES_USER: ` + user + `
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}
    volumes:
      - pg_data:/var/lib/postgresql/data
volumes:
  pg_data:
`
}
