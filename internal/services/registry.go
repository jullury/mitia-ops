package services

import (
	"fmt"
	"strings"
)

const (
	KindMinio       Kind = "minio"
	KindMailcow     Kind = "mailcow"
	KindCaddy       Kind = "caddy"
	KindCloudflared Kind = "cloudflared"
	KindPostgres    Kind = "postgres"
)

type Kind string

type FieldType int

const (
	FieldString FieldType = iota
	FieldSecret
	FieldBool
	FieldSize
	FieldList
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

// ListColumn describes one input column of a FieldList row.
type ListColumn struct {
	Suffix      string // key suffix; row values live under "<listKey>_<n>_<suffix>"
	Label       string
	Placeholder string
}

type Field struct {
	Key         string
	Label       string
	Type        FieldType
	Placeholder string
	Hints       string
	Units       []SizeUnit
	Columns     []ListColumn // FieldList only
}

// ListItemKey names the config item holding one cell of a FieldList row.
func ListItemKey(listKey string, n int, suffix string) string {
	return fmt.Sprintf("%s_%d_%s", listKey, n, suffix)
}

// ListRows reads the rows stored for a FieldList key from a flat key/value map
// (form values, config items, render values). Row cells live under
// "<listKey>_<n>_<suffix>" keys; scanning stops at the first fully-empty row,
// so the UI keeps rows contiguous (it renumbers on remove/add).
func ListRows(values map[string]string, listKey string, columns []ListColumn) []map[string]string {
	var rows []map[string]string
	for n := 0; ; n++ {
		row := make(map[string]string, len(columns))
		any := false
		for _, c := range columns {
			v := ""
			if values != nil {
				v = strings.TrimSpace(values[ListItemKey(listKey, n, c.Suffix)])
			}
			row[c.Suffix] = v
			if v != "" {
				any = true
			}
		}
		if !any {
			return rows
		}
		rows = append(rows, row)
	}
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
