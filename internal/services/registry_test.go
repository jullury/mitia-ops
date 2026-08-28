package services

import "testing"

func TestRegistryContainsServices(t *testing.T) {
	defs := All()
	byKind := map[Kind]Definition{}
	for _, d := range defs {
		byKind[d.Kind] = d
	}
	for _, k := range []Kind{KindMinio, KindMailcow, KindCaddy, KindCloudflared} {
		if _, ok := byKind[k]; !ok {
			t.Fatalf("missing definition for %s", k)
		}
	}
}

func TestDefaultValuesPresent(t *testing.T) {
	for _, d := range All() {
		defaults := d.Defaults()
		for _, f := range d.Fields {
			if f.Type == FieldBool {
				if _, ok := defaults[f.Key]; !ok {
					t.Fatalf("bool field %s.%s missing default", d.Kind, f.Key)
				}
			}
		}
	}
}

func TestSecretFieldMarked(t *testing.T) {
	def, ok := Get(KindMinio)
	if !ok {
		t.Fatal("minio definition missing")
	}
	found := false
	for _, f := range def.Fields {
		if f.Key == "MINIO_ROOT_PASSWORD" && f.Type == FieldSecret {
			found = true
		}
	}
	if !found {
		t.Fatal("MINIO_ROOT_PASSWORD should be a secret field")
	}
}

func TestRegistryReadOnlyAndConfigURL(t *testing.T) {
	defs := All()
	for _, d := range defs {
		if !d.ReadOnly {
			if d.ConfigURL != nil {
				t.Fatalf("config url set on non-read-only kind %s; only read-only kinds expose a config url", d.Kind)
			}
			continue
		}
		if d.ConfigURL == nil {
			t.Fatalf("read-only kind %s must provide a ConfigURL func", d.Kind)
		}
	}
}

func TestMailcowReadOnlyDefinition(t *testing.T) {
	def, ok := Get(KindMailcow)
	if !ok {
		t.Fatal("mailcow definition missing")
	}
	if !def.ReadOnly {
		t.Fatal("mailcow must be read-only")
	}
	if def.ConfigURL == nil {
		t.Fatal("mailcow must provide a ConfigURL func")
	}
	if got := def.ConfigURL(map[string]string{"MAILCOW_HTTP_PORT": "8080"}); got != "http://localhost:8080" {
		t.Fatalf("config url: got %q want %q", got, "http://localhost:8080")
	}
	if got := def.ConfigURL(map[string]string{}); got != "" {
		t.Fatalf("config url with no port should be empty, got %q", got)
	}
	if len(def.Fields) != 1 {
		t.Fatalf("mailcow must expose exactly one field, got %d", len(def.Fields))
	}
	if def.Fields[0].Key != "MAILCOW_HTTP_PORT" || def.Fields[0].Type != FieldString {
		t.Fatalf("mailcow field wrong: %+v", def.Fields[0])
	}

	res, err := def.Render(map[string]string{"MAILCOW_HTTP_PORT": "8080"})
	if err != nil {
		t.Fatal(err)
	}
	if res.DotEnv != "" || res.ComposeYAML != "" {
		t.Fatalf("mailcow render must be empty, got %+v", res)
	}
}
