package services

import (
	"strings"
	"testing"
)

func TestRegistryContainsServices(t *testing.T) {
	defs := All()
	byKind := map[Kind]Definition{}
	for _, d := range defs {
		byKind[d.Kind] = d
	}
	for _, k := range []Kind{KindGarage, KindMailcow, KindCaddy, KindCloudflared, KindPostgres, KindVault, KindGlitchTip} {
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

func TestGarageFields(t *testing.T) {
	def, ok := Get(KindGarage)
	if !ok {
		t.Fatal("garage definition missing")
	}
	keys := map[string]bool{}
	var size *Field
	for i := range def.Fields {
		f := &def.Fields[i]
		keys[f.Key] = true
		if f.Key == "GARAGE_VOLUME_SIZE" {
			size = f
		}
	}
	for _, want := range []string{"GARAGE_HOSTNAME", "GARAGE_VOLUME_SIZE"} {
		if !keys[want] {
			t.Fatalf("garage missing field %s (have %v)", want, keys)
		}
	}
	// No credential fields: the S3 access key is auto-generated on launch.
	if keys["GARAGE_ACCESS_KEY_ID"] || keys["GARAGE_SECRET_ACCESS_KEY"] {
		t.Fatalf("garage must not expose credential form fields: %v", keys)
	}
	if size == nil || size.Type != FieldSize || len(size.Units) == 0 {
		t.Fatalf("garage must declare a FieldSize volume (resize preflight), got %+v", size)
	}
}

func TestRegistryReadOnlyAndConfigURL(t *testing.T) {
	defs := All()
	for _, d := range defs {
		if d.ReadOnly && d.ConfigURL == nil {
			t.Fatalf("read-only kind %s must provide a ConfigURL func", d.Kind)
		}
	}
}

func TestCloudflaredFields(t *testing.T) {
	def, ok := Get(KindCloudflared)
	if !ok {
		t.Fatal("cloudflared definition missing")
	}
	if def.ReadOnly {
		t.Fatal("cloudflared must be lifecycle-controlled")
	}
	var name, ingress *Field
	for i := range def.Fields {
		switch def.Fields[i].Key {
		case "CF_TUNNEL":
			if def.Fields[i].Type != FieldString {
				t.Fatalf("CF_TUNNEL must be a string field, got %+v", def.Fields[i])
			}
			name = &def.Fields[i]
		case "CF_INGRESS":
			if def.Fields[i].Type != FieldList {
				t.Fatalf("CF_INGRESS must be a list field, got %+v", def.Fields[i])
			}
			if len(def.Fields[i].Columns) != 2 {
				t.Fatalf("CF_INGRESS must have two columns, got %+v", def.Fields[i].Columns)
			}
			if def.Fields[i].Columns[0].Suffix != "HOST" || def.Fields[i].Columns[1].Suffix != "SERVICE" {
				t.Fatalf("CF_INGRESS columns wrong: %+v", def.Fields[i].Columns)
			}
			ingress = &def.Fields[i]
		default:
			t.Fatalf("unexpected cloudflared field %q (only CF_TUNNEL + CF_INGRESS, no stored credentials)", def.Fields[i].Key)
		}
	}
	if name == nil || ingress == nil {
		t.Fatalf("cloudflared definition missing expected fields: %+v", def.Fields)
	}
}

func TestMailcowDefinition(t *testing.T) {
	def, ok := Get(KindMailcow)
	if !ok {
		t.Fatal("mailcow definition missing")
	}
	if def.ReadOnly {
		t.Fatal("mailcow must be lifecycle-controlled")
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
	keys := map[string]bool{}
	for _, f := range def.Fields {
		keys[f.Key] = true
	}
	for _, want := range []string{"MAILCOW_HOSTNAME", "MAILCOW_HTTP_PORT", "MAILCOW_HTTPS_PORT", "MAILCOW_TZ"} {
		if !keys[want] {
			t.Fatalf("mailcow missing field %s (have %v)", want, keys)
		}
	}
	if def.Fields[0].Key != "MAILCOW_HOSTNAME" || def.Fields[0].Type != FieldString {
		t.Fatalf("mailcow first field must be the required hostname: %+v", def.Fields[0])
	}

	res, err := def.Render(map[string]string{"MAILCOW_HTTP_PORT": "8080"})
	if err != nil {
		t.Fatal(err)
	}
	if res.DotEnv != "" || res.ComposeYAML != "" {
		t.Fatalf("mailcow render must be empty (config is written by the launch prepare step), got %+v", res)
	}
}

func TestPostgresDefinition(t *testing.T) {
	def, ok := Get(KindPostgres)
	if !ok {
		t.Fatal("postgres definition missing")
	}
	if def.ReadOnly {
		t.Fatal("postgres must be lifecycle-controlled")
	}
	keys := map[string]bool{}
	var size *Field
	for i := range def.Fields {
		f := &def.Fields[i]
		keys[f.Key] = true
		if f.Key == "POSTGRES_PASSWORD" && f.Type != FieldSecret {
			t.Fatalf("POSTGRES_PASSWORD must be a secret field, got %+v", f)
		}
		if f.Key == "POSTGRES_VOLUME_SIZE" {
			size = f
		}
	}
	for _, want := range []string{"POSTGRES_DB", "POSTGRES_USER", "POSTGRES_PASSWORD", "POSTGRES_PORT", "POSTGRES_VOLUME_SIZE"} {
		if !keys[want] {
			t.Fatalf("postgres missing field %s (have %v)", want, keys)
		}
	}
	if size == nil || size.Type != FieldSize || len(size.Units) == 0 {
		t.Fatalf("postgres must declare a FieldSize volume (initiation guard), got %+v", size)
	}
}

func TestGlitchTipDefinition(t *testing.T) {
	def, ok := Get(KindGlitchTip)
	if !ok {
		t.Fatal("glitchtip definition missing")
	}
	if def.ReadOnly {
		t.Fatal("glitchtip must be lifecycle-controlled")
	}
	if def.ConfigURL == nil {
		t.Fatal("glitchtip must provide a ConfigURL func")
	}
	keys := map[string]bool{}
	var size *Field
	for i := range def.Fields {
		f := &def.Fields[i]
		keys[f.Key] = true
		if f.Key == "EMAIL_URL" && f.Type != FieldSecret {
			t.Fatalf("EMAIL_URL must be a secret field, got %+v", f)
		}
		if f.Key == "GLITCHTIP_VOLUME_SIZE" {
			size = f
		}
	}
	for _, want := range []string{"GLITCHTIP_DOMAIN", "GLITCHTIP_PORT", "EMAIL_URL", "DEFAULT_FROM_EMAIL", "GLITCHTIP_VOLUME_SIZE"} {
		if !keys[want] {
			t.Fatalf("glitchtip missing field %s (have %v)", want, keys)
		}
	}
	// The Django SECRET_KEY and the bundled postgres password are
	// auto-generated at launch (garage pattern), never operator-entered.
	if keys["GLITCHTIP_SECRET_KEY"] || keys["GLITCHTIP_DB_PASSWORD"] {
		t.Fatalf("glitchtip must not expose credential form fields: %v", keys)
	}
	if size == nil || size.Type != FieldSize || len(size.Units) == 0 {
		t.Fatalf("glitchtip must declare a FieldSize volume (initiation guard), got %+v", size)
	}
}

func TestMailcowConf(t *testing.T) {
	conf := MailcowConf(map[string]string{"MAILCOW_HOSTNAME": "mail.example.com", "MAILCOW_HTTP_PORT": "8080"})
	for _, want := range []string{"MAILCOW_HOSTNAME=mail.example.com", "HTTP_PORT=8080", "HTTPS_PORT=443", "DBPASS=", "DBROOT=", "REDISPASS=", "API_KEY=", "TZ=Etc/UTC"} {
		if !strings.Contains(conf, want) {
			t.Fatalf("mailcow.conf missing %q:\n%s", want, conf)
		}
	}
	// DB/API secrets must be generated and be non-trivial in length.
	for _, key := range []string{"DBPASS", "DBROOT", "REDISPASS", "API_KEY"} {
		val := confValueOf(conf, key)
		if len(val) < 20 {
			t.Fatalf("mailcow %s secret too short: %q", key, val)
		}
	}
}

func TestMailcowSecrets(t *testing.T) {
	// No stored values: every credential must be freshly generated and be
	// non-trivial in length.
	sec := MailcowSecrets(nil)
	for _, key := range MailcowSecretKeys() {
		if len(sec[key]) < 20 {
			t.Fatalf("%s freshly generated too short: %q", key, sec[key])
		}
	}
	// Stored values are reused verbatim; missing ones are still generated.
	sec = MailcowSecrets(map[string]string{MailcowSecretDBPass: "keepme"})
	if got := sec[MailcowSecretDBPass]; got != "keepme" {
		t.Fatalf("stored DBPASS must be preserved, got %q", got)
	}
	if len(sec[MailcowSecretDBRoot]) < 20 {
		t.Fatalf("fresh DBROOT too short: %q", sec[MailcowSecretDBRoot])
	}
}

func TestMailcowConfPreservesStoredSecrets(t *testing.T) {
	values := map[string]string{
		MailcowSecretDBPass:    "preset-db-pass",
		MailcowSecretDBRoot:    "preset-db-root",
		MailcowSecretRedisPass: "preset-redis-pass",
		MailcowSecretAPIKey:    "preset-api-key",
	}
	conf := MailcowConf(values)
	for _, tc := range []struct{ key, want string }{
		{"DBPASS", "preset-db-pass"},
		{"DBROOT", "preset-db-root"},
		{"REDISPASS", "preset-redis-pass"},
		{"API_KEY", "preset-api-key"},
	} {
		if got := confValueOf(conf, tc.key); got != tc.want {
			t.Fatalf("mailcow.conf %s = %q, want the reused stored value %q:\n%s", tc.key, got, tc.want, conf)
		}
	}
}

func TestMailcowConfValue(t *testing.T) {
	conf := "A=1\nB=2\nDBPASS=secret\n"
	for key, want := range map[string]string{"A": "1", "B": "2", "DBPASS": "secret", "Z": ""} {
		if got := MailcowConfValue(conf, key); got != want {
			t.Fatalf("MailcowConfValue(%q) = %q, want %q", key, got, want)
		}
	}
}

// TestReconcileMailcowConfAppliesManagedKeys guards the "port config is not
// picked up" bug: when the operator saves a new port/hostname, the app must
// reconcile those app-managed lines into an existing mailcow.conf (the file
// compose reads for port mappings) — while leaving every other line, including
// operator tweaks such as ADDITIONAL_SAN, byte-for-byte untouched.
func TestReconcileMailcowConfAppliesManagedKeys(t *testing.T) {
	original := "# my header comment\n" +
		"MAILCOW_HOSTNAME=old.example.com\n" +
		"DBPASS=oldpass\n" +
		"HTTP_PORT=80\n" +
		"TZ=Etc/UTC\n" +
		"ADDITIONAL_SAN=extra.example.com\n"
	values := map[string]string{
		"MAILCOW_HOSTNAME":     "mail.example.com",
		"MAILCOW_HTTP_PORT":    "2111",
		MailcowSecretDBPass:    "newpass",
		MailcowSecretDBRoot:    "newroot",
		MailcowSecretRedisPass: "newredis",
		MailcowSecretAPIKey:    "newapikey",
	}
	got := ReconcileMailcowConf(original, values)
	for _, want := range []string{
		"# my header comment",
		"MAILCOW_HOSTNAME=mail.example.com",
		"HTTP_PORT=2111",
		"DBPASS=newpass",
		"DBROOT=newroot", // missing from the file, appended by the reconciler
		"REDISPASS=newredis",
		"API_KEY=newapikey",
		"ADDITIONAL_SAN=extra.example.com", // operator tweaks must survive
		"TZ=Etc/UTC",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("reconciled conf missing %q:\n%s", want, got)
		}
	}
	for _, banned := range []string{"old.example.com", "HTTP_PORT=80", "oldpass"} {
		if strings.Contains(got, banned) {
			t.Fatalf("reconciled conf still carries %q:\n%s", banned, got)
		}
	}
	// Reconciling the reconciled conf is a stable no-op.
	if again := ReconcileMailcowConf(got, values); again != got {
		t.Fatalf("reconcile must be idempotent\nfirst:\n%s\nsecond:\n%s", got, again)
	}
}

func TestReconcileMailcowConfEmpty(t *testing.T) {
	got := ReconcileMailcowConf("", map[string]string{
		"MAILCOW_HOSTNAME":     "mail.example.com",
		"MAILCOW_HTTP_PORT":    "2111",
		MailcowSecretDBPass:    "coldstart-db-pass",
		MailcowSecretDBRoot:    "coldstart-db-root",
		MailcowSecretRedisPass: "coldstart-redis-pass",
		MailcowSecretAPIKey:    "coldstart-api-key",
	})
	for _, want := range []string{
		"MAILCOW_HOSTNAME=mail.example.com",
		"HTTP_PORT=2111",
		"DBNAME=mailcow",
		"DBPASS=coldstart-db-pass",
		"TZ=Etc/UTC",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("reconciling an empty conf must render the managed keys, missing %q:\n%s", want, got)
		}
	}
}

// confValueOf returns the bare value of key in a line `key=value` (no trailing
// CR); a helper for the tests only.
func confValueOf(conf, key string) string {
	for _, line := range strings.Split(conf, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if v, ok := strings.CutPrefix(line, key+"="); ok {
			return v
		}
	}
	return ""
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
