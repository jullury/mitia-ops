package services

import "strings"

func init() {
	register(Definition{
		Kind:  KindGarage,
		Label: "Garage (S3 object storage)",
		Fields: []Field{
			{Key: "GARAGE_HOSTNAME", Label: "S3 hostname", Type: FieldString, Placeholder: "s3.example.com"},
			{Key: "GARAGE_S3_REGION", Label: "S3 region", Type: FieldString, Placeholder: "eu-west-1", Hints: "cosmetic only; Garage accepts any region label"},
			{
				Key:   "GARAGE_VOLUME_SIZE",
				Label: "Volume size limit",
				Type:  FieldSize,
				Hints: "soft upper bound; used for the free-space preflight (Docker local volumes cannot enforce a size)",
				Units: []SizeUnit{
					{Label: "MiB", Suffix: "M", Base: 1 << 20},
					{Label: "GiB", Suffix: "G", Base: 1 << 30},
					{Label: "TiB", Suffix: "T", Base: 1 << 40},
				},
			},
		},
		Render: func(values map[string]string) (RenderResult, error) {
			return RenderResult{DotEnv: garageEnv(values), ComposeYAML: garageCompose(values)}, nil
		},
	})
}

func garageEnv(v map[string]string) string {
	var b strings.Builder
	for _, k := range []string{"GARAGE_HOSTNAME", "GARAGE_S3_REGION", "GARAGE_VOLUME_SIZE"} {
		if val, ok := v[k]; ok {
			b.WriteString(k + "=" + val + "\n")
		}
	}
	return b.String()
}

func garageCompose(v map[string]string) string {
	volName := v["GARAGE_VOLUME_NAME"]
	if volName == "" {
		volName = "garage_data"
	}
	var b strings.Builder
	b.WriteString(`services:
  garage:
    image: ` + garageImage + `
    restart: unless-stopped
    ports:
      - "3900:3900"
    volumes:
      - garage_data:/srv/garage
      - ./garage.toml:/etc/garage.toml:ro
volumes:
  garage_data:
    external: true
    name: ` + volName + "\n")
	return b.String()
}
