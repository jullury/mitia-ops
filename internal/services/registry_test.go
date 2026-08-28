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

func TestSplitSize(t *testing.T) {
	cases := map[string][2]string{
		"100G": {"100", "G"},
		"512M": {"512", "M"},
		"1T":   {"1", "T"},
		"100":  {"100", ""},
		"":     {"", ""},
	}
	for in, want := range cases {
		n, s := SplitSize(in)
		if n != want[0] || s != want[1] {
			t.Errorf("SplitSize(%q) = %q,%q; want %q,%q", in, n, s, want[0], want[1])
		}
	}
}
