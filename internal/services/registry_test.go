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
