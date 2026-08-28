package services

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
			return RenderResult{DotEnv: minioEnv(values)}, nil
		},
	})
}

func minioEnv(v map[string]string) string {
	return "" // filled in Task 6
}
