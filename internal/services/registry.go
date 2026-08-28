package services

const (
	KindMinio       Kind = "minio"
	KindMailcow     Kind = "mailcow"
	KindCaddy       Kind = "caddy"
	KindCloudflared Kind = "cloudflared"
)

type Kind string

type FieldType int

const (
	FieldString FieldType = iota
	FieldSecret
	FieldBool
)

type Field struct {
	Key         string
	Label       string
	Type        FieldType
	Placeholder string
	Hints       string
}

type RenderResult struct {
	DotEnv      string
	ComposeYAML string // may be empty if template is static/external
}

type Definition struct {
	Kind   Kind
	Label  string
	Fields []Field
	Render func(values map[string]string) (RenderResult, error)
	// ReadOnly kinds expose a config URL and read-only status; the app does
	// not author config, write .env/compose, or control lifecycle for them.
	ReadOnly  bool
	ConfigURL func(values map[string]string) string
}

func (d Definition) Defaults() map[string]string {
	out := map[string]string{}
	for _, f := range d.Fields {
		switch f.Type {
		case FieldBool:
			out[f.Key] = "true"
		}
	}
	return out
}

func All() []Definition { return registry }

func Get(k Kind) (Definition, bool) {
	for _, d := range registry {
		if d.Kind == k {
			return d, true
		}
	}
	return Definition{}, false
}

var registry []Definition

func register(d Definition) { registry = append(registry, d) }
