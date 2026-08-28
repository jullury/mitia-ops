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
	FieldSize
)

// SizeUnit describes one selectable unit in a FieldSize picker. Label is shown
// in the dropdown (e.g. "GB"), Suffix is the Docker size suffix stored in the
// value (e.g. "G"), and Base is the multiplier in bytes used for
// canonicalisation and free-space checks.
type SizeUnit struct {
	Label  string
	Suffix string
	Base   int64
}

type Field struct {
	Key         string
	Label       string
	Type        FieldType
	Placeholder string
	Hints       string
	Units       []SizeUnit
}

// SplitSize splits a canonical Docker size value ("100G") into its numeric and
// unit-suffix parts for pre-filling a FieldSize picker.
func SplitSize(v string) (num string, suffix string) {
	if v == "" {
		return "", ""
	}
	i := 0
	for i < len(v) && v[i] >= '0' && v[i] <= '9' {
		i++
	}
	return v[:i], v[i:]
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
