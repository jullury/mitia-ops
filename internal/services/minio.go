package services

import "strings"

func init() {
	register(Definition{
		Kind:  KindMinio,
		Label: "Minio (S3 object storage)",
		Fields: []Field{
			{Key: "MINIO_HOSTNAME", Label: "Hostname", Type: FieldString, Placeholder: "s3.example.com"},
			{Key: "MINIO_ROOT_USER", Label: "Root user", Type: FieldString, Placeholder: "minioadmin"},
			{Key: "MINIO_ROOT_PASSWORD", Label: "Root password", Type: FieldSecret, Hints: ">= 8 chars"},
			{Key: "MINIO_BROWSER", Label: "Enable web console", Type: FieldBool, Hints: "default true"},
		},
		Render: func(values map[string]string) (RenderResult, error) {
			return RenderResult{DotEnv: minioEnv(values), ComposeYAML: minioCompose(values)}, nil
		},
	})
}

func minioEnv(v map[string]string) string {
	var b strings.Builder
	for _, k := range []string{"MINIO_HOSTNAME", "MINIO_ROOT_USER", "MINIO_ROOT_PASSWORD"} {
		if val, ok := v[k]; ok {
			b.WriteString(k + "=" + val + "\n")
		}
	}
	return b.String()
}

func minioCompose(v map[string]string) string {
	return `services:
  minio:
    image: minio/minio:latest
    command: server /data --console-address ":9001"
    restart: unless-stopped
    ports:
      - "9000:9000"
      - "9001:9001"
    environment:
      MINIO_ROOT_USER: ${MINIO_ROOT_USER}
      MINIO_ROOT_PASSWORD: ${MINIO_ROOT_PASSWORD}
    volumes:
      - minio_data:/data
volumes:
  minio_data:
`
}
