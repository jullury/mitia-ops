package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jullury/mitia-ops/internal/crypto"
	"github.com/jullury/mitia-ops/internal/db"
	"github.com/jullury/mitia-ops/internal/services"
)

type fakeRunner struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeRunner) Run(dir string, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, strings.Join(append([]string{"compose"}, args...), " "))
	return "ok", nil
}

func (f *fakeRunner) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	copy(out, f.calls)
	return out
}

type fakeRawRunner struct {
	ran []string
}

func (f *fakeRawRunner) RunRaw(args ...string) (string, error) {
	f.ran = append(f.ran, args...)
	return "ok", nil
}

func testServer(t *testing.T) (*db.DB, http.Handler) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	c, _ := crypto.New("master")
	cfg := Config{
		DB:        d,
		Cipher:    c,
		DeployDir: t.TempDir(),
		Docker:    &fakeRunner{},
	}
	return d, New(cfg)
}

func TestDashboard(t *testing.T) {
	d, h := testServer(t)
	id, err := d.CreateService("garage", "main")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	// assert a service-row-specific marker, not just "garage" (always present
	// as a dropdown option)
	if !strings.Contains(body, `href="/service/`+id+`"`) {
		t.Fatalf("dashboard should link to the service row: %s", body)
	}
	if !strings.Contains(body, "main") {
		t.Fatalf("dashboard should show the service name in its row: %s", body)
	}
}

func TestStatusClass(t *testing.T) {
	cases := []struct{ in, class, title string }{
		{`{"State":"running","Status":"Up 2 hours"}`, "running", "Running"},
		{`{"State":"running","Status":"Up About a minute"}`, "running", "Running"},
		{`{"State":"restarting"}`, "warning", "Warning"},
		{`{"State":"paused"}`, "warning", "Warning"},
		{`{"State":"exited"}`, "stopped", "Stopped"},
		{`{"State":"dead"}`, "stopped", "Stopped"},
		{`{"State":"stopped"}`, "stopped", "Stopped"},
		{`{"State":"created"}`, "stopped", "Stopped"},
		{``, "unknown", "Unknown"},
		{`{"foo":"bar"}`, "unknown", "Unknown"},
		// stderr warnings prepended to the JSON (the real bug): status must
		// still resolve from the embedded "State" field.
		{`time="2026-08-28T22:24:34+03:00" level=warning msg="The \"GARAGE_ACCESS_KEY_ID\" variable is not set. Defaulting to a blank string."` + "\n" + `{"State":"running","Status":"Up 4 minutes"}`, "running", "Running"},
		{`warning line 1` + "\n" + `{"State":"exited","Status":"Exited (1)"}`, "stopped", "Stopped"},
	}
	for _, c := range cases {
		if got := statusClass(c.in); got != c.class {
			t.Errorf("statusClass(%q) = %q, want %q", c.in, got, c.class)
		}
		if got := statusTitle(c.in); got != c.title {
			t.Errorf("statusTitle(%q) = %q, want %q", c.in, got, c.title)
		}
	}
}

func TestActionFlash(t *testing.T) {
	d, h := testServer(t)
	id, err := d.CreateService("garage", "main")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/service/"+id+"/action?op=up", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/?msg=up" {
		t.Fatalf("redirect location = %q, want /?msg=up", loc)
	}
	// follow the redirect and assert the flash banner renders
	req2 := httptest.NewRequest("GET", "/?msg=up", nil)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if !strings.Contains(rec2.Body.String(), "Service started") {
		t.Fatalf("dashboard should show flash: %s", rec2.Body.String())
	}
}

func TestUnknownActionOpRejected(t *testing.T) {
	d, h := testServer(t)
	id, err := d.CreateService("garage", "main")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/service/"+id+"/action?op=explode", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown op must be rejected with 400, got %d", rec.Code)
	}
}

func TestSaveAndShowService(t *testing.T) {
	d, h := testServer(t)
	id, _ := d.CreateService("garage", "main")
	form := "GARAGE_ACCESS_KEY_ID=admin&GARAGE_SECRET_ACCESS_KEY=supersecret&GARAGE_HOSTNAME=s3.example.com"
	req := httptest.NewRequest("POST", "/service/"+id, strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect, got %d", rec.Code)
	}
	// stored value must be encrypted
	items, _ := d.ConfigItems(id)
	if items["GARAGE_SECRET_ACCESS_KEY"] == "supersecret" {
		t.Fatal("password should be stored encrypted")
	}
}

func TestUpActionRemovesEphemeralEnv(t *testing.T) {
	dbDir := filepath.Join(t.TempDir(), "test.db")
	d, err := db.Open(dbDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	c, _ := crypto.New("master")
	deployDir := t.TempDir()

	save := func(id string, form string) {
		req := httptest.NewRequest("POST", "/service/"+id, strings.NewReader(form))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		New(Config{DB: d, Cipher: c, DeployDir: deployDir, Docker: &fakeRunner{}}).ServeHTTP(rec, req)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("save status %d", rec.Code)
		}
	}

	id, _ := d.CreateService("garage", "main")
	save(id, "GARAGE_SECRET_ACCESS_KEY=supersecret&GARAGE_HOSTNAME=s3.example.com&GARAGE_ACCESS_KEY_ID=admin")

	envPath := filepath.Join(deployDir, id, ".env")
	if _, err := os.Stat(envPath); !os.IsNotExist(err) {
		t.Fatal("saveService must not write a .env (secrets stay in SQLite)")
	}

	req := httptest.NewRequest("POST", "/service/"+id+"/action?op=up", nil)
	rec := httptest.NewRecorder()
	New(Config{DB: d, Cipher: c, DeployDir: deployDir, Docker: &fakeRunner{}}).ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("up status %d", rec.Code)
	}
	if _, err := os.Stat(envPath); !os.IsNotExist(err) {
		t.Fatalf("ephemeral .env must be removed after up, still exists: %v", err)
	}
}

// seedMailcow service account: create a mailcow service with a saved hostname
// and a deploy dir that already contains an upstream docker-compose.yml, so the
// launch prepare step reuses the checkout instead of cloning (which needs git
// + network). Returns the service id and the deploy dir.
func seedMailcow(t *testing.T, d *db.DB, deployDir string) string {
	t.Helper()
	id, err := d.CreateService("mailcow", "mail")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(deployDir, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	items := []db.ConfigItem{
		{Key: "MAILCOW_HOSTNAME", Value: "mail.example.com"},
		{Key: "MAILCOW_HTTP_PORT", Value: "8080"},
	}
	if err := d.SetConfigItems(id, items); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestMailcowLifecycleRunsCompose(t *testing.T) {
	dbDir := filepath.Join(t.TempDir(), "test.db")
	d, err := db.Open(dbDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	c, _ := crypto.New("master")
	deployDir := t.TempDir()
	runner := &fakeRunner{}
	h := New(Config{DB: d, Cipher: c, DeployDir: deployDir, Docker: runner})

	id := seedMailcow(t, d, deployDir)
	dir := filepath.Join(deployDir, id)

	// Wait for a command to appear in the fake runner, or time out. The mailcow
	// `up` runs in a background goroutine (so the browser can show progress
	// during the long clone/pull), so it is not recorded synchronously.
	waitFor := func(expect string) []string {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			got := runner.recorded()
			for _, g := range got {
				if g == expect {
					return got
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("async step %q never ran, recorded %v", expect, runner.recorded())
		return nil
	}

	// `up` is asynchronous: the handler redirects immediately and the compose
	// run happens in the background.
	req := httptest.NewRequest("POST", "/service/"+id+"/action?op=up", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("mailcow up status %d (body %s)", rec.Code, rec.Body.String())
	}
	waitFor("compose up -d --force-recreate")

	// `restart` runs synchronously and immediately recorded.
	before := len(runner.recorded())
	req = httptest.NewRequest("POST", "/service/"+id+"/action?op=restart", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("mailcow restart status %d (body %s)", rec.Code, rec.Body.String())
	}
	got := runner.recorded()[before:]
	if len(got) != 1 || got[0] != "compose restart" {
		t.Fatalf("mailcow restart must run exactly %q this step, got %v", "compose restart", got)
	}

	// `down` runs synchronously and immediately recorded.
	before = len(runner.recorded())
	req = httptest.NewRequest("POST", "/service/"+id+"/action?op=down", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("mailcow down status %d (body %s)", rec.Code, rec.Body.String())
	}
	got = runner.recorded()[before:]
	if len(got) != 1 || got[0] != "compose down" {
		t.Fatalf("mailcow down must run exactly %q this step, got %v", "compose down", got)
	}

	// The prepare step must have written mailcow.conf and the .env symlink.
	if _, err := os.Stat(filepath.Join(dir, "mailcow.conf")); err != nil {
		t.Fatalf("mailcow.conf should be written, got %v", err)
	}
	if fi, err := os.Lstat(filepath.Join(dir, ".env")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf(".env should be a symlink to mailcow.conf, got %v %v", fi, err)
	}
}

// mailcowConfValue extracts the value of a `key=` line from the written
// mailcow.conf; a helper for the tests only.
func mailcowConfValue(t *testing.T, dir, key string) string {
	t.Helper()
	conf, err := os.ReadFile(filepath.Join(dir, "mailcow.conf"))
	if err != nil {
		t.Fatalf("read mailcow.conf: %v", err)
	}
	for _, line := range strings.Split(string(conf), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSuffix(line, "\r"), key+"="); ok {
			return v
		}
	}
	t.Fatalf("mailcow.conf missing %s:\n%s", key, conf)
	return ""
}

// TestMailcowRecloneKeepsCredentials reproduces the credential-drift bug: the
// deploy dir (and with it mailcow.conf) is re-created while the named Docker
// volumes survive. The first `up` must persist its generated DB/Redis/API
// credentials (encrypted) so the re-clone's conf reuses them instead of
// rotating secrets under the still-mounted MySQL datadir.
func TestMailcowRecloneKeepsCredentials(t *testing.T) {
	dbDir := filepath.Join(t.TempDir(), "test.db")
	d, err := db.Open(dbDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	c, _ := crypto.New("master")
	deployDir := t.TempDir()
	runner := &fakeRunner{}
	h := New(Config{DB: d, Cipher: c, DeployDir: deployDir, Docker: runner})

	id := seedMailcow(t, d, deployDir)
	dir := filepath.Join(deployDir, id)

	// up runs the async mailcow flow; return once a new compose command lands
	// in the fake runner (the conf/prepare step runs before it, so by then the
	// file is written).
	up := func(prior int) {
		t.Helper()
		req := httptest.NewRequest("POST", "/service/"+id+"/action?op=up", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("up status %d (body %s)", rec.Code, rec.Body.String())
		}
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if len(runner.recorded()) > prior {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("async up never ran (recorded %d before, %v now)", prior, runner.recorded())
	}

	up(0)
	first := map[string]string{
		"DBPASS":    mailcowConfValue(t, dir, "DBPASS"),
		"DBROOT":    mailcowConfValue(t, dir, "DBROOT"),
		"REDISPASS": mailcowConfValue(t, dir, "REDISPASS"),
		"API_KEY":   mailcowConfValue(t, dir, "API_KEY"),
	}

	// Credentials must be persisted (encrypted) so a re-clone can restore them.
	stored := map[string]string{
		"DBPASS":    "mailcow.dbpass",
		"DBROOT":    "mailcow.dbroot",
		"REDISPASS": "mailcow.redispass",
		"API_KEY":   "mailcow.apikey",
	}
	items, _ := d.ConfigItems(id)
	for confKey, itemKey := range stored {
		enc := items[itemKey]
		if enc == "" {
			t.Fatalf("first up must persist credentials under %q", itemKey)
		}
		if got, err := c.Decrypt(enc); err != nil || got != first[confKey] {
			t.Fatalf("stored %q must decrypt to the generated %s (err %v, got %q)",
				itemKey, confKey, err, got)
		}
	}

	// Simulate an out-of-band re-clone: the checkout (and mailcow.conf) is
	// gone; only the Docker named volumes survive.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	up(1)
	for _, k := range []string{"DBPASS", "DBROOT", "REDISPASS", "API_KEY"} {
		if got := mailcowConfValue(t, dir, k); got != first[k] {
			t.Fatalf("re-clone rotated %s: first %q, regenerated %q", k, first[k], got)
		}
	}
}

// TestMailcowSavedPortPropagatesToConf guards the "port config is not picked
// up" bug: after the operator saves a new HTTP port, the NEXT launch must
// re-render mailcow.conf (the file compose reads for port mappings) with the
// new port — without rotating the DB/API credentials the volumes were
// initialized with.
func TestMailcowSavedPortPropagatesToConf(t *testing.T) {
	dbDir := filepath.Join(t.TempDir(), "test.db")
	d, err := db.Open(dbDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	c, _ := crypto.New("master")
	deployDir := t.TempDir()
	runner := &fakeRunner{}
	h := New(Config{DB: d, Cipher: c, DeployDir: deployDir, Docker: runner})

	id := seedMailcow(t, d, deployDir) // MAILCOW_HTTP_PORT=8080
	dir := filepath.Join(deployDir, id)

	// up runs the async mailcow flow; return once a new compose command lands
	// in the fake runner (the conf/prepare step runs before it, so by then the
	// file is written).
	up := func(prior int) {
		t.Helper()
		req := httptest.NewRequest("POST", "/service/"+id+"/action?op=up", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("up status %d (body %s)", rec.Code, rec.Body.String())
		}
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if len(runner.recorded()) > prior {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("async up never ran (recorded %d before, %v now)", prior, runner.recorded())
	}

	up(0)
	first := map[string]string{
		"DBPASS":    mailcowConfValue(t, dir, "DBPASS"),
		"DBROOT":    mailcowConfValue(t, dir, "DBROOT"),
		"REDISPASS": mailcowConfValue(t, dir, "REDISPASS"),
		"API_KEY":   mailcowConfValue(t, dir, "API_KEY"),
	}
	if v := mailcowConfValue(t, dir, "HTTP_PORT"); v != "8080" {
		t.Fatalf("first launch must render HTTP_PORT=8080, got %q", v)
	}

	// The operator edits the port and saves. mailcow renders nothing on save
	// (config is materialized at launch), so only the stored value changes.
	form := url.Values{"MAILCOW_HOSTNAME": {"mail.example.com"}, "MAILCOW_HTTP_PORT": {"2111"}}
	req := httptest.NewRequest("POST", "/service/"+id, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save status %d (body %s)", rec.Code, rec.Body.String())
	}

	// The next launch must apply the saved edit to mailcow.conf.
	up(1)
	if v := mailcowConfValue(t, dir, "HTTP_PORT"); v != "2111" {
		t.Fatalf("saved port must propagate to mailcow.conf on the next launch: want 2111, conf has %q", v)
	}
	for _, k := range []string{"DBPASS", "DBROOT", "REDISPASS", "API_KEY"} {
		if got := mailcowConfValue(t, dir, k); got != first[k] {
			t.Fatalf("port edit rotated %s: first %q, now %q", k, first[k], got)
		}
	}
}

// TestMailcowAdoptsExistingConfSecrets covers the upgrade path of this fix: a
// checkout whose mailcow.conf already carries credentials, but whose encrypted
// store is empty (as created before credential persistence existed), must adopt
// those credentials into the store instead of minting fresh ones — otherwise
// the next launch rotates secrets underneath the still-mounted MySQL datadir.
func TestMailcowAdoptsExistingConfSecrets(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	c, _ := crypto.New("master")
	deployDir := t.TempDir()
	id := seedMailcow(t, d, deployDir) // MAILCOW_HTTP_PORT=8080, store empty
	dir := filepath.Join(deployDir, id)

	seed := "MAILCOW_HOSTNAME=mail.example.com\n" +
		"DBPASS=legacy-db-pass\nDBROOT=legacy-db-root\nREDISPASS=legacy-redis-pass\n" +
		"API_KEY=legacy-api-key\nHTTP_PORT=9443\n"
	if err := os.WriteFile(filepath.Join(dir, "mailcow.conf"), []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	s := &server{cfg: Config{DB: d, Cipher: c, DeployDir: deployDir}}
	if err := prepareMailcow(s, "", dir, map[string]string{
		"MAILCOW_HOSTNAME":  "mail.example.com",
		"MAILCOW_HTTP_PORT": "8080",
	}); err != nil {
		t.Fatal(err)
	}

	// The conf's credentials must have been adopted into the encrypted store.
	legacy := map[string]string{
		"mailcow.dbpass":    "legacy-db-pass",
		"mailcow.dbroot":    "legacy-db-root",
		"mailcow.redispass": "legacy-redis-pass",
		"mailcow.apikey":    "legacy-api-key",
	}
	items, _ := d.ConfigItems(id)
	for itemKey, want := range legacy {
		enc := items[itemKey]
		if enc == "" {
			t.Fatalf("launch must persist adopted credential under %q", itemKey)
		}
		if got, err := c.Decrypt(enc); err != nil || got != want {
			t.Fatalf("adopted %q must decrypt to %q (err %v, got %q)", itemKey, want, err, got)
		}
	}

	// The launched conf must reuse them verbatim (no rotation) while still
	// applying saved edits: HTTP_PORT was 9443 in the file, 8080 in the store.
	conf, err := os.ReadFile(filepath.Join(dir, "mailcow.conf"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"DBPASS=legacy-db-pass", "DBROOT=legacy-db-root",
		"REDISPASS=legacy-redis-pass", "API_KEY=legacy-api-key", "HTTP_PORT=8080",
	} {
		if !strings.Contains(string(conf), want) {
			t.Fatalf("mailcow.conf missing %q after adoption:\n%s", want, conf)
		}
	}
}

// TestMailcowStoredSecretCorruptionErrors guards the failure mode where the
// stored credential set can no longer be decrypted (e.g. the master key was
// rotated): the launch must abort loudly instead of silently rotating secrets
// underneath the mounted data volumes.
func TestMailcowStoredSecretCorruptionErrors(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	c, _ := crypto.New("master")
	deployDir := t.TempDir()
	id := seedMailcow(t, d, deployDir)

	if err := d.SetConfigItems(id, []db.ConfigItem{
		{Key: "mailcow.dbpass", Value: "garbage-not-ciphertext"},
	}); err != nil {
		t.Fatal(err)
	}
	s := &server{cfg: Config{DB: d, Cipher: c, DeployDir: deployDir}}
	err = prepareMailcow(s, "", filepath.Join(deployDir, id), map[string]string{"MAILCOW_HOSTNAME": "mail.example.com"})
	if err == nil {
		t.Fatal("undecryptable stored credentials must abort the launch, got nil")
	}
	if !strings.Contains(err.Error(), "mailcow.dbpass") {
		t.Fatalf("error should name the undecryptable key, got: %v", err)
	}
}

func TestMailcowUpRequiresHostname(t *testing.T) {
	dbDir := filepath.Join(t.TempDir(), "test.db")
	d, err := db.Open(dbDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	c, _ := crypto.New("master")
	deployDir := t.TempDir()
	runner := &fakeRunner{}
	h := New(Config{DB: d, Cipher: c, DeployDir: deployDir, Docker: runner})

	id, err := d.CreateService("mailcow", "mail")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/service/"+id+"/action?op=up", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d", rec.Code)
	}
	if got := runner.recorded(); len(got) != 0 {
		t.Fatalf("mailcow up without a hostname must not run docker, got %v", got)
	}
}

// TestMailcowStatusEndpointLiveProgress verifies that the async mailcow `up`
// exposes its progress through the JSON status endpoint, ending in `running`.
func TestMailcowStatusEndpointLiveProgress(t *testing.T) {
	dbDir := filepath.Join(t.TempDir(), "test.db")
	d, err := db.Open(dbDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	c, _ := crypto.New("master")
	deployDir := t.TempDir()
	h := New(Config{DB: d, Cipher: c, DeployDir: deployDir, Docker: &fakeRunner{}})

	id := seedMailcow(t, d, deployDir)

	req := httptest.NewRequest("POST", "/service/"+id+"/action?op=up", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)

	deadline := time.Now().Add(2 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		sreq := httptest.NewRequest("GET", "/service/"+id+"/status", nil)
		srec := httptest.NewRecorder()
		h.ServeHTTP(srec, sreq)
		if srec.Code == http.StatusNoContent {
			t.Fatalf("status endpoint should report a job after async up")
		}
		last = srec.Body.String()
		if strings.Contains(last, `"running"`) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("async up never reached running state; last status: %s", last)
}

// TestMailcowStatusError reports a failed deployment (missing hostname) through
// the status endpoint as an error state rather than running docker.
func TestMailcowStatusError(t *testing.T) {
	dbDir := filepath.Join(t.TempDir(), "test.db")
	d, err := db.Open(dbDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	c, _ := crypto.New("master")
	deployDir := t.TempDir()
	h := New(Config{DB: d, Cipher: c, DeployDir: deployDir, Docker: &fakeRunner{}})

	id, err := d.CreateService("mailcow", "mail") // no hostname set
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("POST", "/service/"+id+"/action?op=up", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)

	deadline := time.Now().Add(2 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		sreq := httptest.NewRequest("GET", "/service/"+id+"/status", nil)
		srec := httptest.NewRecorder()
		h.ServeHTTP(srec, sreq)
		last = srec.Body.String()
		if strings.Contains(last, `"error"`) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("async up missing hostname should report error state; last: %s", last)
}

func TestJobTrackerExpiresTerminalJobs(t *testing.T) {
	base := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	tr := newJobTracker()
	tr.now = func() time.Time { return base }

	tr.set("m", JobCloning, "cloning", "")
	if _, ok := tr.get("m"); !ok {
		t.Fatal("in-flight job should be visible")
	}
	// Non-terminal (in-flight) jobs are never pruned, however old they get.
	tr.now = func() time.Time { return base.Add(jobRetention + time.Hour) }
	if _, ok := tr.get("m"); !ok {
		t.Fatal("in-flight job must not expire")
	}

	// Terminal jobs are retained briefly (so the UI can read the final state),
	// then pruned. A lingering terminal job makes the dashboard treat a finished
	// deployment as a fresh "running" result and reload the page in a loop.
	tr.now = func() time.Time { return base }
	tr.set("m", JobRunning, "done", "")
	if j, ok := tr.get("m"); !ok || j.state != JobRunning {
		t.Fatalf("fresh terminal job should be visible, got ok=%v", ok)
	}
	tr.now = func() time.Time { return base.Add(jobRetention + time.Second) }
	if _, ok := tr.get("m"); ok {
		t.Fatal("expired terminal job must be pruned")
	}

	// Error state likewise expires.
	tr.now = func() time.Time { return base }
	tr.set("e", JobError, "boom", "err")
	if _, ok := tr.get("e"); !ok {
		t.Fatal("fresh error job should be visible")
	}
	tr.now = func() time.Time { return base.Add(jobRetention + time.Second) }
	if _, ok := tr.get("e"); ok {
		t.Fatal("expired error job must be pruned")
	}
}

func TestGarageTomlRequiredFields(t *testing.T) {
	rpcSecret := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" // 32-byte hex
	toml := garageToml("s3.example.com", "us-east-1", rpcSecret)
	for _, want := range []string{
		`metadata_dir = "/srv/garage/meta"`,
		`data_dir = "/srv/garage/data"`,
		`replication_factor = 1`,
		`rpc_bind_addr = "[::]:3901"`,
		`rpc_secret = "` + rpcSecret + `"`,
		`[s3_api]`,
		`s3_region = "us-east-1"`,
		`api_bind_addr = "[::]:3900"`,
		`[s3_web]`,
		`index = "index.html"`,
	} {
		if !strings.Contains(toml, want) {
			t.Fatalf("garage.toml missing %q:\n%s", want, toml)
		}
	}
	// root_domain is required in BOTH [s3_api] and [s3_web], and the configured
	// GARAGE_HOSTNAME must be used (this is what the user's "S3 hostname" is for).
	if strings.Count(toml, `root_domain = "s3.example.com"`) != 2 {
		t.Fatalf("both s3_api and s3_web must set root_domain from the hostname:\n%s", toml)
	}
	if !strings.Contains(garageToml("", "", "x"), `root_domain = "s3.mitia.local"`) {
		t.Fatal("missing placeholder root_domain when no hostname configured")
	}
	// s3_region is cosmetic only; leave it alone when a value is provided, and
	// fall back to eu-west-1 when empty.
	if !strings.Contains(garageToml("", "", "x"), `s3_region = "eu-west-1"`) {
		t.Fatal("missing default s3_region when none configured")
	}
}

func TestRandomHex(t *testing.T) {
	s := randomHex(32)
	if len(s) != 64 {
		t.Fatalf("randomHex(32) = %d chars, want 64 (32-byte hex)", len(s))
	}
	if randomHex(32) == s {
		t.Fatal("randomHex should not repeat")
	}
}

func TestGarageServiceShowsConnection(t *testing.T) {
	dbDir := filepath.Join(t.TempDir(), "test.db")
	d, err := db.Open(dbDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	c, _ := crypto.New("master")
	h := New(Config{DB: d, Cipher: c, DeployDir: t.TempDir(), Docker: &fakeRunner{}})

	id, err := d.CreateService("garage", "main")
	if err != nil {
		t.Fatal(err)
	}
	enc, err := c.Encrypt("verysecret")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetConfigItems(id, []db.ConfigItem{
		{Key: "GARAGE_HOSTNAME", Value: "s3.example.com"},
		{Key: "GARAGE_ACCESS_KEY_ID", Value: "access123"},
		{Key: "GARAGE_SECRET_ACCESS_KEY", Value: enc},
	}); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/service/"+id, nil))
	body := rec.Body.String()
	for _, want := range []string{
		"Connection", "s3.example.com:3900", "access123",
		"verysecret", `id="garage-secret"`, `id="garage-endpoint"`, `id="garage-access"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("service page missing %q", want)
		}
	}
}

// scriptedRaw is a RawRunner that returns canned output for a given docker
// invocation and records every call it made.
type scriptedRaw struct {
	responses map[string]string
	calls     []string
}

func (f *scriptedRaw) RunRaw(args ...string) (string, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	if out, ok := f.responses[strings.Join(args, " ")]; ok {
		return out, nil
	}
	return "ok", nil
}

func TestPostUpGarageInitializesCluster(t *testing.T) {
	dbDir := filepath.Join(t.TempDir(), "test.db")
	d, err := db.Open(dbDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	c, _ := crypto.New("master")
	id, err := d.CreateService("garage", "main")
	if err != nil {
		t.Fatal(err)
	}
	nodeID := "e29344419cf19f4f9b04b5d5e39d5bb4d165fe125bb74ea791b4771262850806"
	raw := &scriptedRaw{responses: map[string]string{
		"exec " + id + "-garage-1 /garage layout show": "No nodes currently have a role in the cluster.\n\nCurrent cluster layout version: 0\n",
		"exec " + id + "-garage-1 /garage node id":     nodeID + "\n",
	}}
	s := &server{
		cfg: Config{
			DB:        d,
			Cipher:    c,
			DockerRaw: raw,
		},
	}
	enc, _ := c.Encrypt("s3cret")
	if err := d.SetConfigItems(id, []db.ConfigItem{
		{Key: garageAccessKeyKey, Value: "mitia-access1"},
		{Key: garageSecretKeyKey, Value: enc},
		{Key: bucketBackupKey, Value: "alpha,beta"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.postUpGarage(id); err != nil {
		t.Fatalf("postUpGarage: %v", err)
	}
	joined := strings.Join(raw.calls, "\n")
	for _, want := range []string{
		"layout assign -z dc1 -c 1TB " + nodeID,
		"layout apply --version 1",
		"key import -n mitia-ops-" + id + " --yes mitia-access1 s3cret",
		"key allow mitia-access1 --create-bucket",
		"bucket allow alpha --key mitia-access1 --read --write",
		"bucket allow beta --key mitia-access1 --read --write",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("postUpGarage did not run %q\ncalls:\n%s", want, joined)
		}
	}
}

func TestPostUpGarageSkipsWhenLayoutReady(t *testing.T) {
	dbDir := filepath.Join(t.TempDir(), "test.db")
	d, err := db.Open(dbDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	c, _ := crypto.New("master")
	id, err := d.CreateService("garage", "main")
	if err != nil {
		t.Fatal(err)
	}
	raw := &scriptedRaw{responses: map[string]string{
		"exec " + id + "-garage-1 /garage layout show": "==== CURRENT CLUSTER LAYOUT ====\nNodes: 1\n",
	}}
	s := &server{cfg: Config{DB: d, Cipher: c, DockerRaw: raw}}
	enc, _ := c.Encrypt("s3cret")
	if err := d.SetConfigItems(id, []db.ConfigItem{
		{Key: garageAccessKeyKey, Value: "mitia-access1"},
		{Key: garageSecretKeyKey, Value: enc},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.postUpGarage(id); err != nil {
		t.Fatalf("postUpGarage: %v", err)
	}
	joined := strings.Join(raw.calls, "\n")
	if strings.Contains(joined, "layout assign") {
		t.Errorf("layout assign should be skipped once the layout is ready:\n%s", joined)
	}
	for _, want := range []string{
		"key import -n mitia-ops-" + id + " --yes mitia-access1 s3cret",
		"key allow mitia-access1 --create-bucket",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("postUpGarage did not run %q\ncalls:\n%s", want, joined)
		}
	}
}

func TestSaveMailcowDoesNotCreateCompose(t *testing.T) {
	dbDir := filepath.Join(t.TempDir(), "test.db")
	d, err := db.Open(dbDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	c, _ := crypto.New("master")
	deployDir := t.TempDir()
	h := New(Config{DB: d, Cipher: c, DeployDir: deployDir, Docker: &fakeRunner{}})

	id, err := d.CreateService("mailcow", "mail")
	if err != nil {
		t.Fatal(err)
	}
	form := "MAILCOW_HTTP_PORT=8080"
	req := httptest.NewRequest("POST", "/service/"+id, strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save status %d", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(deployDir, id)); !os.IsNotExist(err) {
		t.Fatalf("saving a read-only service must not create its deploy dir (got %v)", err)
	}
}

func TestShowServiceMailcowConfigLink(t *testing.T) {
	d, h := testServer(t)
	id, err := d.CreateService("mailcow", "mail")
	if err != nil {
		t.Fatal(err)
	}
	form := "MAILCOW_HTTP_PORT=8080"
	req := httptest.NewRequest("POST", "/service/"+id, strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save status %d", rec.Code)
	}

	req = httptest.NewRequest("GET", "/service/"+id, nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `href="http://localhost:8080"`) {
		t.Fatalf("service page should render the mailcow config url link: %s", body)
	}
}

func TestHardenMailcowIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	mk := func(p, content string) string {
		p = filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	mk("data/conf/unbound/unbound.conf", "server:\n  do-ip6: yes\n  do-udp: yes\n")
	mk("data/assets/ssl-example/cert.pem", "CERT")
	mk("data/assets/ssl-example/key.pem", "KEY")
	mk("data/assets/ssl-example/dhparams.pem", "DHP")

	assertFiles := func() {
		t.Helper()
		conf, err := os.ReadFile(filepath.Join(dir, "data/conf/unbound/unbound.conf"))
		if err != nil {
			t.Fatal(err)
		}
		s := string(conf)
		if !strings.Contains(s, "do-ip6: no") {
			t.Errorf("unbound should disable ipv6: %s", s)
		}
		if !strings.Contains(s, "forward-zone:") || !strings.Contains(s, "forward-addr: 1.1.1.1") {
			t.Errorf("unbound should add forward zone: %s", s)
		}
		for _, f := range []string{"cert.pem", "key.pem", "dhparams.pem"} {
			if data, err := os.ReadFile(filepath.Join(dir, "data/assets/ssl", f)); err != nil || string(data) == "" {
				t.Errorf("ssl %s should be seeded: %v", f, err)
			}
		}
	}

	if err := hardenMailcow(dir); err != nil {
		t.Fatalf("first run: %v", err)
	}
	assertFiles()
	before, err := os.ReadFile(filepath.Join(dir, "data/conf/unbound/unbound.conf"))
	if err != nil {
		t.Fatal(err)
	}
	beforeCerts, err := os.ReadFile(filepath.Join(dir, "data/assets/ssl/cert.pem"))
	if err != nil {
		t.Fatal(err)
	}

	// Re-run: must be a no-op (no error, no changes).
	if err := hardenMailcow(dir); err != nil {
		t.Fatalf("second run: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(dir, "data/conf/unbound/unbound.conf"))
	if err != nil {
		t.Fatal(err)
	}
	afterCerts, err := os.ReadFile(filepath.Join(dir, "data/assets/ssl/cert.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("second run changed unbound.conf:\nbefore %q\nafter  %q", before, after)
	}
	if string(beforeCerts) != string(afterCerts) {
		t.Errorf("second run changed seeded cert")
	}
}

func TestHardenMailcowLeavesExistingConfigAlone(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "data/conf/unbound/unbound.conf")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	original := "server:\n  do-ip6: no\n  forward-zone:\n    name: \".\"\n    forward-addr: 9.9.9.9\n"
	if err := os.WriteFile(p, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	ssl := filepath.Join(dir, "data/assets/ssl/cert.pem")
	if err := os.MkdirAll(filepath.Dir(ssl), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ssl, []byte("REALCERT"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := hardenMailcow(dir); err != nil {
		t.Fatalf("harden: %v", err)
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Errorf("custom unbound.conf should be unchanged:\nwant %q\ngot  %q", original, got)
	}
	gotCert, err := os.ReadFile(ssl)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotCert) != "REALCERT" {
		t.Errorf("existing cert should be unchanged: %q", gotCert)
	}
}

func TestHardenMailcowMissingFilesIsNoop(t *testing.T) {
	// A checkout without unbound.conf or ssl-example (e.g. a minimal test
	// fixtures dir) must not error.
	dir := t.TempDir()
	if err := hardenMailcow(dir); err != nil {
		t.Fatalf("harden on bare dir should be a no-op: %v", err)
	}
}

func TestDashboardMailcowShowsConfigLink(t *testing.T) {
	d, h := testServer(t)
	id, err := d.CreateService("mailcow", "mail")
	if err != nil {
		t.Fatal(err)
	}
	// set the http port so a config url can be derived
	form := "MAILCOW_HTTP_PORT=8080"
	req := httptest.NewRequest("POST", "/service/"+id, strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save status %d", rec.Code)
	}

	req = httptest.NewRequest("GET", "/", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `href="http://localhost:8080"`) {
		t.Fatalf("dashboard should render the mailcow config url link: %s", body)
	}
	for _, btn := range []string{"?op=up", "?op=restart", "?op=down"} {
		if !strings.Contains(body, btn) {
			t.Fatalf("dashboard must render lifecycle buttons for mailcow (missing %s): %s", btn, body)
		}
	}
}

func TestSaveCloudflaredPersistsIngressAndCompose(t *testing.T) {
	dbDir := filepath.Join(t.TempDir(), "test.db")
	d, err := db.Open(dbDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	c, _ := crypto.New("master")
	deployDir := t.TempDir()
	h := New(Config{DB: d, Cipher: c, DeployDir: deployDir, Docker: &fakeRunner{}})

	id, _ := d.CreateService("cloudflared", "tun")
	form := "CF_TUNNEL=my-tunnel" +
		"&CF_INGRESS_0_HOST=app.example.com&CF_INGRESS_0_SERVICE=http%3A%2F%2Flocalhost%3A8080" +
		"&CF_INGRESS_1_HOST=web.example.com&CF_INGRESS_1_SERVICE=http%3A%2F%2Flocalhost%3A9001"
	req := httptest.NewRequest("POST", "/service/"+id, strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save status %d: %s", rec.Code, rec.Body.String())
	}

	items, _ := d.ConfigItems(id)
	if items["CF_INGRESS_0_HOST"] != "app.example.com" || items["CF_INGRESS_1_SERVICE"] != "http://localhost:9001" {
		t.Fatalf("ingress rows not persisted: %+v", items)
	}
	if items["CF_TUNNEL"] != "my-tunnel" {
		t.Fatalf("tunnel name not persisted: %+v", items)
	}

	dir := filepath.Join(deployDir, id)
	if _, err := os.Stat(filepath.Join(dir, "docker-compose.yml")); err != nil {
		t.Fatalf("save must write docker-compose.yml: %v", err)
	}
	// The resolved config.yml + creds are only materialized at launch (once the
	// tunnel id is known), not at save time.
	for _, file := range []string{"config.yml", "creds.json"} {
		if _, err := os.Stat(filepath.Join(dir, file)); err == nil {
			t.Fatalf("save must NOT write %s (written at launch instead): %v", file, err)
		}
	}
}

// fakeCloudflared stubs the cloudflared CLI for launch-time preparation tests.
type fakeCloudflared struct {
	routes *[]string
}

func (f fakeCloudflared) LoggedIn() bool { return true }
func (f fakeCloudflared) LoginURL() (string, error) {
	return "", errors.New("unexpected LoginURL on a logged-in run")
}
func (f fakeCloudflared) EnsureTunnel(string) (string, []byte, error) {
	return testCFID, []byte(`{"AccountTag":"abc","TunnelID":"x","TunnelSecret":"y"}`), nil
}
func (f fakeCloudflared) RouteDNS(tunnel, hostname string) error {
	if f.routes != nil {
		*f.routes = append(*f.routes, tunnel+" "+hostname)
	}
	return nil
}

const testCFID = "6ff42ae9-29c3-4b8f-93b2-5b2acd3e737d"

func TestUpCloudflaredWritesCredsAndConfig(t *testing.T) {
	dbDir := filepath.Join(t.TempDir(), "test.db")
	d, err := db.Open(dbDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	c, _ := crypto.New("master")
	deployDir := t.TempDir()
	var routes []string
	h := New(Config{DB: d, Cipher: c, DeployDir: deployDir, Docker: &fakeRunner{}, Cloudflared: fakeCloudflared{routes: &routes}})

	id, _ := d.CreateService("cloudflared", "tun")
	form := "CF_TUNNEL=my-tunnel&CF_INGRESS_0_HOST=app.example.com&CF_INGRESS_0_SERVICE=http%3A%2F%2Flocalhost%3A8080"
	save := httptest.NewRequest("POST", "/service/"+id, strings.NewReader(form))
	save.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, save)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save status %d", rec.Code)
	}

	up := httptest.NewRequest("POST", "/service/"+id+"/action?op=up", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, up)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("up status %d: %s", rec.Code, rec.Body.String())
	}

	dir := filepath.Join(deployDir, id)
	cfgBytes, err := os.ReadFile(filepath.Join(dir, "config.yml"))
	if err != nil {
		t.Fatalf("launch must write config.yml: %v", err)
	}
	if !strings.Contains(string(cfgBytes), "tunnel: 6ff42ae9") ||
		!strings.Contains(string(cfgBytes), "app.example.com") ||
		!strings.Contains(string(cfgBytes), "http_status:404") {
		t.Fatalf("config.yml wrong:\n%s", cfgBytes)
	}
	if !strings.Contains(string(cfgBytes), "/etc/cloudflared/creds.json") {
		t.Fatalf("config.yml should reference the container creds path:\n%s", cfgBytes)
	}
	credsBytes, err := os.ReadFile(filepath.Join(dir, "creds.json"))
	if err != nil {
		t.Fatalf("launch must write creds.json: %v", err)
	}
	info, _ := os.Stat(filepath.Join(dir, "creds.json"))
	perm := info.Mode().Perm()
	// As root we chown creds.json to the container uid and keep 0600; otherwise
	// it falls back to 0644 so the container uid can read it.
	if perm != 0o600 && perm != 0o644 {
		t.Fatalf("creds.json mode = %v, want 0600 or 0644", perm)
	}
	if !strings.Contains(string(credsBytes), "AccountTag") {
		t.Fatalf("creds.json must hold the tunnel credentials: %s", credsBytes)
	}
	if want := "my-tunnel app.example.com"; strings.Join(routes, ",") != want {
		t.Fatalf("RouteDNS calls = %v, want %v", routes, want)
	}
}

// reuseCloudflared simulates an existing tunnel: no fresh credentials, so the
// launch must reuse the cache from the deploy dir.
type reuseCloudflared struct {
	routes *[]string
}

func (r reuseCloudflared) LoggedIn() bool { return true }
func (r reuseCloudflared) LoginURL() (string, error) {
	return "", errors.New("unexpected LoginURL on a logged-in run")
}
func (r reuseCloudflared) EnsureTunnel(string) (string, []byte, error) {
	return testCFID, nil, nil
}
func (r reuseCloudflared) RouteDNS(tunnel, hostname string) error {
	if r.routes != nil {
		*r.routes = append(*r.routes, tunnel+" "+hostname)
	}
	return nil
}

func TestUpCloudflaredReusesCachedCredsWhenTunnelExists(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	c, _ := crypto.New("master")
	deployDir := t.TempDir()
	h := New(Config{DB: d, Cipher: c, DeployDir: deployDir, Docker: &fakeRunner{}, Cloudflared: &reuseCloudflared{}})

	id, _ := d.CreateService("cloudflared", "tun")
	dir := filepath.Join(deployDir, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cached := `{"AccountTag":"abc","TunnelID":"` + testCFID + `","TunnelSecret":"cached-secret"}`
	if err := os.WriteFile(filepath.Join(dir, "creds.json"), []byte(cached), 0o600); err != nil {
		t.Fatal(err)
	}

	form := "CF_TUNNEL=my-tunnel&CF_INGRESS_0_HOST=app.example.com&CF_INGRESS_0_SERVICE=http%3A%2F%2Flocalhost%3A8080"
	save := httptest.NewRequest("POST", "/service/"+id, strings.NewReader(form))
	save.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, save)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save status %d", rec.Code)
	}

	up := httptest.NewRequest("POST", "/service/"+id+"/action?op=up", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, up)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("up status %d: %s", rec.Code, rec.Body.String())
	}
	if got := strings.TrimSpace(string(mustRead(t, filepath.Join(dir, "creds.json")))); got != cached {
		t.Fatalf("credits must be preserved from the cached copy, got %s", got)
	}
}

func TestUpCloudflaredErrorsWhenExistingTunnelHasNoCachedCreds(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	c, _ := crypto.New("master")
	deployDir := t.TempDir()
	h := New(Config{DB: d, Cipher: c, DeployDir: deployDir, Docker: &fakeRunner{}, Cloudflared: &reuseCloudflared{}})

	id, _ := d.CreateService("cloudflared", "tun")
	form := "CF_TUNNEL=my-tunnel&CF_INGRESS_0_HOST=app.example.com&CF_INGRESS_0_SERVICE=http%3A%2F%2Flocalhost%3A8080"
	save := httptest.NewRequest("POST", "/service/"+id, strings.NewReader(form))
	save.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, save)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save status %d", rec.Code)
	}

	up := httptest.NewRequest("POST", "/service/"+id+"/action?op=up", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, up)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("up should redirect with an error, got %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "err=1") {
		t.Fatalf("expected an error redirect, got %v", loc)
	}
	decoded, _ := url.QueryUnescape(loc)
	if !strings.Contains(decoded, "no credentials are cached") {
		t.Fatalf("expected an explainable error redirect, got %v", loc)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestUpCloudflaredPromptsForLoginWhenNotLoggedIn(t *testing.T) {
	// A missing `cloudflared tunnel login` (no cert.pem => not logged in) must
	// be surfaced as an inline prompt back on the service page, not a bare
	// error page.
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	c, _ := crypto.New("master")
	deployDir := t.TempDir()
	h := New(Config{DB: d, Cipher: c, DeployDir: deployDir, Docker: &fakeRunner{},
		Cloudflared: &notLoggedInCloudflared{}})

	id, _ := d.CreateService("cloudflared", "tun")
	form := "CF_TUNNEL=my-tunnel&CF_INGRESS_0_HOST=app.example.com&CF_INGRESS_0_SERVICE=http://localhost:8080"
	save := httptest.NewRequest("POST", "/service/"+id, strings.NewReader(form))
	save.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, save)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save status %d", rec.Code)
	}

	up := httptest.NewRequest("POST", "/service/"+id+"/action?op=up", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, up)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("up should redirect back to the service page, got %d", rec.Code)
	}
	if !strings.HasPrefix(rec.Header().Get("Location"), "/service/"+id+"?") {
		t.Fatalf("expected redirect to the service page, got %v", rec.Header().Get("Location"))
	}

	// The redirect target must render the login prompt as an inline error.
	get := httptest.NewRequest("GET", rec.Header().Get("Location"), nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, get)
	if rec.Code != 200 {
		t.Fatalf("get status %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Cloudflare login required") {
		t.Fatalf("expected login prompt inline, got: %s", body)
	}
	// The authorization URL must be surfaced as a link that opens in a new tab.
	if !strings.Contains(body, `href="`+testLoginURL+`"`) {
		t.Fatalf("login prompt must link the Cloudflare authorization URL, got: %s", body)
	}
	if !strings.Contains(body, `target="_blank"`) {
		t.Fatalf("login link must open in a new tab, got: %s", body)
	}
}

type notLoggedInCloudflared struct{ fakeCloudflared }

func (notLoggedInCloudflared) LoggedIn() bool { return false }

const testLoginURL = "https://dash.cloudflare.com/argotunnel?callback=xyz"

func (notLoggedInCloudflared) LoginURL() (string, error) {
	return testLoginURL, nil
}

func TestResaveCloudflaredRemovesStaleIngressRows(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	c, _ := crypto.New("master")
	h := New(Config{DB: d, Cipher: c, DeployDir: t.TempDir(), Docker: &fakeRunner{}})
	id, _ := d.CreateService("cloudflared", "tun")

	save := func(form string) {
		req := httptest.NewRequest("POST", "/service/"+id, strings.NewReader(form))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("save status %d: %s", rec.Code, rec.Body.String())
		}
	}

	save("CF_TUNNEL=my-tunnel&CF_INGRESS_0_HOST=a.ex&CF_INGRESS_0_SERVICE=http://localhost:80&CF_INGRESS_1_HOST=b.ex&CF_INGRESS_1_SERVICE=http://localhost:81")
	items, _ := d.ConfigItems(id)
	if items["CF_INGRESS_1_HOST"] != "b.ex" {
		t.Fatalf("seed save missing second row: %+v", items)
	}

	// Re-save with one fewer rule: the removed row's keys must vanish.
	save("CF_TUNNEL=my-tunnel&CF_INGRESS_0_HOST=a.ex&CF_INGRESS_0_SERVICE=http://localhost:80")
	items, _ = d.ConfigItems(id)
	if items["CF_INGRESS_1_HOST"] != "" || items["CF_INGRESS_1_SERVICE"] != "" {
		t.Fatalf("stale ingress keys must be removed, got %+v", items)
	}
}

func TestShowCloudflaredPrefillsRoutingRows(t *testing.T) {
	d, h := testServer(t)
	id, err := d.CreateService("cloudflared", "tun")
	if err != nil {
		t.Fatal(err)
	}
	form := "CF_TUNNEL=my-tunnel&CF_INGRESS_0_HOST=app.example.com&CF_INGRESS_0_SERVICE=http%3A%2F%2Flocalhost%3A8080"
	req := httptest.NewRequest("POST", "/service/"+id, strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save status %d", rec.Code)
	}

	req = httptest.NewRequest("GET", "/service/"+id, nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("get status %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `name="CF_INGRESS_0_HOST"`) {
		t.Fatalf("routing host input missing: %s", body)
	}
	if !strings.Contains(body, `value="app.example.com"`) {
		t.Fatalf("routing host value should pre-fill: %s", body)
	}
	if !strings.Contains(body, `name="CF_INGRESS_0_SERVICE"`) {
		t.Fatalf("routing service input missing: %s", body)
	}
	if !strings.Contains(body, "Traffic routing") {
		t.Fatalf("traffic routing card missing: %s", body)
	}
}

func TestSaveCloudflaredRejectsPartialIngressRow(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	c, _ := crypto.New("master")
	h := New(Config{DB: d, Cipher: c, DeployDir: t.TempDir(), Docker: &fakeRunner{}})
	id, _ := d.CreateService("cloudflared", "tun")

	form := "CF_TUNNEL=my-tunnel&CF_INGRESS_0_HOST=app.example.com"
	req := httptest.NewRequest("POST", "/service/"+id, strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("partial row should re-render the form with 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `class="flash error"`) {
		t.Fatalf("expected inline error flash: %s", rec.Body.String())
	}
	items, _ := d.ConfigItems(id)
	if items["CF_INGRESS_0_HOST"] != "" || items["CF_TUNNEL"] != "" {
		t.Fatalf("rejected save must not persist anything, got %+v", items)
	}
}

func TestSaveCloudflaredRejectsMissingTunnelName(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	c, _ := crypto.New("master")
	h := New(Config{DB: d, Cipher: c, DeployDir: t.TempDir(), Docker: &fakeRunner{}})
	id, _ := d.CreateService("cloudflared", "tun")

	form := "CF_INGRESS_0_HOST=app.example.com&CF_INGRESS_0_SERVICE=http://localhost:8080"
	req := httptest.NewRequest("POST", "/service/"+id, strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("missing tunnel name should re-render the form with 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `class="flash error"`) {
		t.Fatalf("expected inline error flash: %s", rec.Body.String())
	}
}

func TestSaveSizeChangeTriggersResize(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	c, _ := crypto.New("master")
	deployDir := t.TempDir()
	id, _ := d.CreateService("garage", "main")

	// Seed initial config via a plain save (no raw runner, no volume yet) so a
	// later size change can then be detected as a real change.
	seed := httptest.NewRequest("POST", "/service/"+id,
		strings.NewReader("GARAGE_ACCESS_KEY_ID=admin&GARAGE_SECRET_ACCESS_KEY=supersecret&GARAGE_HOSTNAME=s3.example.com&GARAGE_VOLUME_SIZE=1&GARAGE_VOLUME_SIZE_UNIT=G"))
	seed.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	seedRec := httptest.NewRecorder()
	New(Config{DB: d, Cipher: c, DeployDir: deployDir, Docker: &fakeRunner{}}).ServeHTTP(seedRec, seed)
	if seedRec.Code != http.StatusSeeOther {
		t.Fatalf("seed save status %d", seedRec.Code)
	}

	raw := &fakeRawRunner{}
	// Changing the size (1G -> 2G) on the main save form must drive the
	// fail-safe resize (volume exists per the fake raw runner).
	req := httptest.NewRequest("POST", "/service/"+id,
		strings.NewReader("GARAGE_ACCESS_KEY_ID=admin&GARAGE_SECRET_ACCESS_KEY=supersecret&GARAGE_HOSTNAME=s3.example.com&GARAGE_VOLUME_SIZE=2&GARAGE_VOLUME_SIZE_UNIT=G"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	New(Config{DB: d, Cipher: c, DeployDir: deployDir, Docker: &fakeRunner{}, DockerRaw: raw}).ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("resize save status %d: %s", rec.Code, rec.Body.String())
	}

	items, _ := d.ConfigItems(id)
	if items["GARAGE_VOLUME_SIZE"] != "2G" {
		t.Fatalf("resized size stored = %q, want 2G", items["GARAGE_VOLUME_SIZE"])
	}
	if items["GARAGE_VOLUME_NAME"] == "" {
		t.Fatal("resize must persist the new volume name")
	}
	composeBytes, err := os.ReadFile(filepath.Join(deployDir, id, "docker-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(composeBytes), "name: "+items["GARAGE_VOLUME_NAME"]) {
		t.Fatalf("compose should reference the new volume name %q: %s", items["GARAGE_VOLUME_NAME"], composeBytes)
	}
	if len(raw.ran) == 0 {
		t.Fatal("raw runner should have executed volume commands")
	}
}

func TestSaveUnchangedSizeDoesNotResize(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	c, _ := crypto.New("master")
	deployDir := t.TempDir()
	id, _ := d.CreateService("garage", "main")

	seed := httptest.NewRequest("POST", "/service/"+id,
		strings.NewReader("GARAGE_ACCESS_KEY_ID=admin&GARAGE_SECRET_ACCESS_KEY=supersecret&GARAGE_HOSTNAME=s3.example.com&GARAGE_VOLUME_SIZE=1&GARAGE_VOLUME_SIZE_UNIT=G"))
	seed.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	New(Config{DB: d, Cipher: c, DeployDir: deployDir, Docker: &fakeRunner{}}).ServeHTTP(httptest.NewRecorder(), seed)

	raw := &fakeRawRunner{}
	// Same size again: should be a plain save, no volume commands.
	req := httptest.NewRequest("POST", "/service/"+id,
		strings.NewReader("GARAGE_ACCESS_KEY_ID=admin&GARAGE_SECRET_ACCESS_KEY=supersecret&GARAGE_HOSTNAME=s3.example.com&GARAGE_VOLUME_SIZE=1&GARAGE_VOLUME_SIZE_UNIT=G"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	New(Config{DB: d, Cipher: c, DeployDir: deployDir, Docker: &fakeRunner{}, DockerRaw: raw}).ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save status %d: %s", rec.Code, rec.Body.String())
	}
	if len(raw.ran) != 0 {
		t.Fatalf("unchanged size must not trigger resize, but ran: %v", raw.ran)
	}
}

func TestFirstSavePersistsSizeWithoutResize(t *testing.T) {
	// A brand-new garage service has no volume yet; saving a size is a plain
	// save (no raw runner needed). The volume is later created at that size by
	// EnsureVolume on `up`.
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	c, _ := crypto.New("master")
	id, _ := d.CreateService("garage", "main")

	req := httptest.NewRequest("POST", "/service/"+id,
		strings.NewReader("GARAGE_ACCESS_KEY_ID=admin&GARAGE_SECRET_ACCESS_KEY=supersecret&GARAGE_HOSTNAME=s3.example.com&GARAGE_VOLUME_SIZE=1&GARAGE_VOLUME_SIZE_UNIT=G"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	New(Config{DB: d, Cipher: c, DeployDir: t.TempDir(), Docker: &fakeRunner{}}).ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("first save status %d", rec.Code)
	}
	items, _ := d.ConfigItems(id)
	if items["GARAGE_VOLUME_SIZE"] != "1G" {
		t.Fatalf("first save stored size = %q, want 1G", items["GARAGE_VOLUME_SIZE"])
	}
}

func TestShowServicePrefillsSizePicker(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	c, _ := crypto.New("master")
	id, _ := d.CreateService("garage", "main")

	save := httptest.NewRequest("POST", "/service/"+id,
		strings.NewReader("GARAGE_ACCESS_KEY_ID=admin&GARAGE_SECRET_ACCESS_KEY=supersecret&GARAGE_HOSTNAME=s3.example.com&GARAGE_VOLUME_SIZE=1&GARAGE_VOLUME_SIZE_UNIT=G"))
	save.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	New(Config{DB: d, Cipher: c, DeployDir: t.TempDir(), Docker: &fakeRunner{}}).ServeHTTP(httptest.NewRecorder(), save)

	get := httptest.NewRequest("GET", "/service/"+id, nil)
	rec := httptest.NewRecorder()
	New(Config{DB: d, Cipher: c, DeployDir: t.TempDir(), Docker: &fakeRunner{}}).ServeHTTP(rec, get)
	if rec.Code != 200 {
		t.Fatalf("get status %d", rec.Code)
	}
	body := rec.Body.String()
	// the size input must pre-fill the saved numeric value and select its unit
	if !strings.Contains(body, `value="1"`) {
		t.Fatalf("size input should pre-fill value 1: %s", body)
	}
	if !strings.Contains(body, `<option value="G" selected>`) {
		t.Fatalf("G unit should be pre-selected: %s", body)
	}
}

func TestSaveRejectsOversizedVolume(t *testing.T) {
	// A volume size the disk can't fit must be rejected before anything is
	// persisted — even on a brand-new service whose volume does not exist yet
	// (the preflight must not be gated on VolumeExists or on having a live
	// volume). The rejection renders the service form back with the error shown
	// inline as a danger notice rather than a bare error page.
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	c, _ := crypto.New("master")
	id, _ := d.CreateService("garage", "main")

	// 999999T (~1000 PiB) exceeds the free space of any real filesystem.
	req := httptest.NewRequest("POST", "/service/"+id,
		strings.NewReader("GARAGE_ACCESS_KEY_ID=admin&GARAGE_SECRET_ACCESS_KEY=supersecret&GARAGE_HOSTNAME=s3.example.com&GARAGE_VOLUME_SIZE=999999&GARAGE_VOLUME_SIZE_UNIT=T"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	New(Config{DB: d, Cipher: c, DeployDir: t.TempDir(), Docker: &fakeRunner{}}).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("oversized volume should re-render the form with 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `class="flash error"`) {
		t.Fatalf("expected an inline error flash, got: %s", body)
	}
	if !strings.Contains(body, "not enough free disk space") {
		t.Fatalf("expected insufficient-storage message, got: %s", body)
	}
	items, _ := d.ConfigItems(id)
	if items["GARAGE_VOLUME_SIZE"] != "" {
		t.Fatalf("rejected size must not be persisted, stored %q", items["GARAGE_VOLUME_SIZE"])
	}
}

func TestDeleteServiceTearsDownAndRemovesRows(t *testing.T) {
	d, h := testServer(t)
	id, err := d.CreateService("cloudflared", "tun")
	if err != nil {
		t.Fatal(err)
	}

	// seed config so we can assert cascade removal of config items
	if err := d.SetConfigItems(id, []db.ConfigItem{{Key: "CF_TUNNEL", Value: "x"}}); err != nil {
		t.Fatal(err)
	}

	// testServer uses its own ephemeral deploy dir; build a fresh handler with a
	// known deploy dir + runner so we can assert `down` ran and the dir removal.
	runner := &fakeRunner{}
	dep := t.TempDir()
	c, _ := crypto.New("master")
	h = New(Config{DB: d, Cipher: c, DeployDir: dep, Docker: runner})
	deployDir := filepath.Join(dep, id)
	if err := os.MkdirAll(deployDir, 0o755); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("POST", "/service/"+id+"/delete", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("delete status %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/?msg=deleted" {
		t.Fatalf("redirect location = %q, want /?msg=deleted", loc)
	}
	// compose down ran
	if got := strings.Join(runner.recorded(), ","); !strings.Contains(got, "down") {
		t.Fatalf("delete must run compose down, got %v", runner.recorded())
	}
	// DB row + config items gone
	if _, err := d.ServiceByID(id); err == nil {
		t.Fatal("service row should be deleted")
	}
	items, _ := d.ConfigItems(id)
	if len(items) != 0 {
		t.Fatalf("config items should cascade-delete, got %+v", items)
	}
	// deploy dir removed
	if _, err := os.Stat(deployDir); !os.IsNotExist(err) {
		t.Fatalf("deploy dir should be removed after delete (got %v)", err)
	}
}

func TestDeleteGarageRemovesDataVolume(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	c, _ := crypto.New("master")
	deployDir := t.TempDir()
	raw := &fakeRawRunner{}
	h := New(Config{DB: d, Cipher: c, DeployDir: deployDir, Docker: &fakeRunner{}, DockerRaw: raw})

	id, _ := d.CreateService("garage", "main")
	// persist a custom volume name to assert we remove exactly that one
	if err := d.SetConfigItems(id, []db.ConfigItem{{Key: "GARAGE_VOLUME_NAME", Value: "custom_data"}}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("POST", "/service/"+id+"/delete", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("delete status %d: %s", rec.Code, rec.Body.String())
	}
	// assert the data volume was removed by the raw runner
	joined := strings.Join(raw.ran, " ")
	if !strings.Contains(joined, "volume rm") || !strings.Contains(joined, "custom_data") {
		t.Fatalf("delete must remove the garage data volume, raw runner ran: %v", raw.ran)
	}
	// DB row gone
	if _, err := d.ServiceByID(id); err == nil {
		t.Fatal("service row should be deleted")
	}
}

// TestSizePreflightAwareOfOtherServices guards the free-space preflight
// counting EVERY service's declared volume size: garage + postgres (and future
// sized kinds) collectively claim the same disk, so a preflight for one must
// fail when another declares a size the disk can't hold — while still ignoring
// the service's own past declaration.
func TestSizePreflightAwareOfOtherServices(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	c, _ := crypto.New("master")
	garageID, err := d.CreateService("garage", "data")
	if err != nil {
		t.Fatal(err)
	}
	pgID, err := d.CreateService("postgres", "data2")
	if err != nil {
		t.Fatal(err)
	}
	put := func(id, key, val string) {
		t.Helper()
		if err := d.SetConfigItems(id, []db.ConfigItem{{Key: key, Value: val}}); err != nil {
			t.Fatal(err)
		}
	}
	put(garageID, "GARAGE_VOLUME_SIZE", "999999T")
	put(pgID, "POSTGRES_VOLUME_SIZE", "999999T")
	s := &server{cfg: Config{DB: d, Cipher: c, DeployDir: t.TempDir()}}

	// A tiny new size for garage must still fail: postgres's declared volume
	// size counts against the same disk.
	if _, _, msg, ok := s.sizePreflight(t.TempDir(), garageID, "1M"); ok {
		t.Fatalf("garage preflight must account for postgres's declared size, passed: %s", msg)
	}
	// With postgres's declaration dropped, and garage excluded from its own
	// check, a small garage volume fits.
	put(pgID, "POSTGRES_VOLUME_SIZE", "")
	if _, _, msg, ok := s.sizePreflight(t.TempDir(), garageID, "1M"); !ok {
		t.Fatalf("without other declarations a small volume must pass: %s", msg)
	}
}

// TestPostgresUpSizeGuard covers the postgres "initiation guard": a declared
// volume size that the disk cannot hold blocks `up` (before any volume is
// created), while a small declared size launches normally.
func TestPostgresUpSizeGuard(t *testing.T) {
	d, h := testServer(t)
	id, err := d.CreateService("postgres", "db")
	if err != nil {
		t.Fatal(err)
	}
	save := func(form string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("POST", "/service/"+id, strings.NewReader(form))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	rec := save("POSTGRES_DB=app&POSTGRES_USER=admin&POSTGRES_PASSWORD=supersecret&POSTGRES_VOLUME_SIZE=999999&POSTGRES_VOLUME_SIZE_UNIT=T")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save status %d: %s", rec.Code, rec.Body.String())
	}
	up := func() string {
		t.Helper()
		req := httptest.NewRequest("POST", "/service/"+id+"/action?op=up", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Header().Get("Location")
	}
	if loc := up(); !strings.Contains(loc, "err=1") {
		t.Fatalf("up with an oversized declaration must hit the size guard, redirect = %q", loc)
	}
	rec = save("POSTGRES_DB=app&POSTGRES_USER=admin&POSTGRES_PASSWORD=supersecret&POSTGRES_VOLUME_SIZE=1&POSTGRES_VOLUME_SIZE_UNIT=M")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save status %d", rec.Code)
	}
	if loc := up(); loc != "/?msg=up" {
		t.Fatalf("up with a small declared size must not be blocked, redirect = %q", loc)
	}
}

func TestDeleteMailcowRunsDownAndDeletes(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	c, _ := crypto.New("master")
	deployDir := t.TempDir()
	runner := &fakeRunner{}
	h := New(Config{DB: d, Cipher: c, DeployDir: deployDir, Docker: runner})
	id := seedMailcow(t, d, deployDir)

	req := httptest.NewRequest("POST", "/service/"+id+"/delete", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("delete status %d", rec.Code)
	}
	// mailcow is lifecycle-controlled: deletion must bring its stack down.
	if got := runner.recorded(); len(got) != 1 || got[0] != "compose down" {
		t.Fatalf("deleting mailcow must run compose down, got %v", got)
	}
	if _, err := d.ServiceByID(id); err == nil {
		t.Fatal("service row should be deleted")
	}
}

func TestDashboardAndServicePageRenderDelete(t *testing.T) {
	d, h := testServer(t)
	id, err := d.CreateService("garage", "main")
	if err != nil {
		t.Fatal(err)
	}
	dash := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, dash)
	if !strings.Contains(rec.Body.String(), "/service/"+id+"/delete") {
		t.Fatalf("dashboard should render the delete form: %s", rec.Body.String())
	}
	page := httptest.NewRequest("GET", "/service/"+id, nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, page)
	body := rec.Body.String()
	if !strings.Contains(body, "/service/"+id+"/delete") {
		t.Fatalf("service page should render the delete form: %s", body)
	}
	if !strings.Contains(body, "Delete service") {
		t.Fatalf("service page should have a Delete service button: %s", body)
	}
}

func TestDeleteUnknownServiceNotFound(t *testing.T) {
	_, h := testServer(t)
	req := httptest.NewRequest("POST", "/service/"+uuid.NewString()+"/delete", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("deleting a missing service should be 404, got %d", rec.Code)
	}
}

func TestNonUUIDServiceIDNotFound(t *testing.T) {
	_, h := testServer(t)
	for _, path := range []string{"/service/999999", "/service/not-a-uuid"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s should be 404 (id must be a UUID), got %d", path, rec.Code)
		}
	}
}

// TestSaveAutostartPersists verifies the start-on-boot checkbox save flow: the
// flag is a universal config item (absent ⇒ off), toggled by the checkbox.
func TestSaveAutostartPersists(t *testing.T) {
	dbDir := filepath.Join(t.TempDir(), "test.db")
	d, err := db.Open(dbDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	c, _ := crypto.New("master")
	h := New(Config{DB: d, Cipher: c, DeployDir: t.TempDir(), Docker: &fakeRunner{}})

	id, err := d.CreateService("mailcow", "mail")
	if err != nil {
		t.Fatal(err)
	}

	save := func(checked bool) {
		t.Helper()
		form := url.Values{"MAILCOW_HOSTNAME": {"mail.example.com"}}
		if checked {
			form.Set("autostart", "on")
		}
		req := httptest.NewRequest("POST", "/service/"+id, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("save status %d (body %s)", rec.Code, rec.Body.String())
		}
	}

	save(false)
	items, _ := d.ConfigItems(id)
	if items["autostart"] != "false" {
		t.Fatalf("unchecked save must store autostart=false, got %q", items["autostart"])
	}
	save(true)
	items, _ = d.ConfigItems(id)
	if items["autostart"] != "true" {
		t.Fatalf("checked save must store autostart=true, got %q", items["autostart"])
	}
	save(false)
	items, _ = d.ConfigItems(id)
	if items["autostart"] != "false" {
		t.Fatalf("re-unchecking must store autostart=false, got %q", items["autostart"])
	}
}

// TestServicePageShowsAutostart verifies the checkbox renders checked for a
// service flagged to start on boot.
func TestServicePageShowsAutostart(t *testing.T) {
	dbDir := filepath.Join(t.TempDir(), "test.db")
	d, err := db.Open(dbDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	c, _ := crypto.New("master")
	h := New(Config{DB: d, Cipher: c, DeployDir: t.TempDir(), Docker: &fakeRunner{}})

	id, err := d.CreateService("mailcow", "mail")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetConfigItems(id, []db.ConfigItem{{Key: "autostart", Value: "true"}}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/service/"+id, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, `name="autostart"`) {
		t.Fatalf("service page must render the start-on-boot checkbox: %s", body)
	}
	if !strings.Contains(body, `name="autostart"`+" checked") {
		t.Fatalf("flagged service must render the checkbox checked: %s", body)
	}
}

// TestAutoStartStartsFlaggedServices verifies that on app boot only services
// with the start-on-boot flag get a compose `up` — through both the mailcow
// background path and the synchronous upService path — and that unflagged
// services are left alone.
func TestAutoStartStartsFlaggedServices(t *testing.T) {
	dbDir := filepath.Join(t.TempDir(), "test.db")
	d, err := db.Open(dbDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	c, _ := crypto.New("master")
	deployDir := t.TempDir()
	runner := &fakeRunner{}
	h := New(Config{DB: d, Cipher: c, DeployDir: deployDir, Docker: runner})

	seedMailcowDir := func(id string) {
		t.Helper()
		dir := filepath.Join(deployDir, id)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Flagged mailcow: must come up via the background job path.
	mail, err := d.CreateService("mailcow", "mail")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetConfigItems(mail, []db.ConfigItem{
		{Key: "MAILCOW_HOSTNAME", Value: "mail.example.com"},
		{Key: "autostart", Value: "true"},
	}); err != nil {
		t.Fatal(err)
	}
	seedMailcowDir(mail)

	// Unflagged mailcow: must stay down.
	other, err := d.CreateService("mailcow", "other")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetConfigItems(other, []db.ConfigItem{{Key: "MAILCOW_HOSTNAME", Value: "mail2.example.com"}}); err != nil {
		t.Fatal(err)
	}
	seedMailcowDir(other)

	// Flagged garage: must come up through the non-mailcow upService path.
	min, err := d.CreateService("garage", "data2")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetConfigItems(min, []db.ConfigItem{{Key: "autostart", Value: "true"}}); err != nil {
		t.Fatal(err)
	}
	minDir := filepath.Join(deployDir, min)
	if err := os.MkdirAll(minDir, 0o755); err != nil {
		t.Fatal(err)
	}

	h.AutoStart()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ups := 0
		for _, g := range runner.recorded() {
			if g == "compose up -d --force-recreate" {
				ups++
			}
		}
		if ups >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	got := runner.recorded()
	upCount := 0
	for _, g := range got {
		switch g {
		case "compose up -d --force-recreate":
			upCount++
		case "compose down", "compose restart":
			t.Fatalf("AutoStart must not run %q, got %v", g, got)
		}
	}
	if upCount != 2 {
		t.Fatalf("AutoStart must up exactly the flagged services (mailcow + garage), got %v", got)
	}
}

// fakeVaultAPI stubs the minimal Vault HTTP API surface the app drives:
// sys/init (GET state + PUT to initialize) and sys/unseal. Proxied to the test
// server so the app's unseal action can run end-to-end against it.
type fakeVaultAPI struct {
	initialized bool
	unsealed    bool
	keys        []string
	rootToken   string
	threshold   int
	unsealCalls int
}

func (f *fakeVaultAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/v1/sys/init":
		switch r.Method {
		case http.MethodGet:
			w.Write([]byte(`{"initialized":` + boolJSON(f.initialized) + `}`))
		case http.MethodPut:
			if f.initialized {
				http.Error(w, `{"errors":["already initialized"]}`, 400)
				return
			}
			f.initialized = true
			var body struct {
				Shares    int `json:"secret_shares"`
				Threshold int `json:"secret_threshold"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.threshold = body.Threshold
			if f.threshold == 0 {
				f.threshold = 3
			}
			w.Write([]byte(`{"keys":["k1","k2","k3","k4","k5"],"root_token":"root-tok","secret_shares":5,"secret_threshold":` + fmt.Sprintf("%d", body.Threshold) + `}`))
		}
	case "/v1/sys/unseal":
		f.unsealCalls++
		if f.unsealCalls >= f.threshold {
			f.unsealed = true
		}
		w.Write([]byte(`{"sealed":` + boolJSON(!f.unsealed) + `,"threshold":` + fmt.Sprintf("%d", f.threshold) + `}`))
	default:
		http.NotFound(w, r)
	}
}

func boolJSON(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func TestVaultUnsealInitializesAndUnseals(t *testing.T) {
	api := &fakeVaultAPI{}
	srv := httptest.NewServer(api)
	defer srv.Close()
	port := strings.TrimPrefix(srv.URL, "http://127.0.0.1:")

	d, h := testServer(t)
	id, err := d.CreateService("vault", "vault")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/service/"+id, strings.NewReader(url.Values{
		"VAULT_PORT":             {port},
		"VAULT_VOLUME_SIZE":      {"1"},
		"VAULT_VOLUME_SIZE_UNIT": {"M"},
	}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save status %d: %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest("POST", "/service/"+id+"/action?op=unseal", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "msg=vault-unsealed") {
		t.Fatalf("unseal redirect should report success, got %q body=%s", loc, rec.Body.String())
	}
	if !api.initialized {
		t.Fatal("Vault must have been initialized")
	}
	if !api.unsealed {
		t.Fatal("Vault must have been unsealed")
	}
	// key + root token must be persisted encrypted in the config store.
	items, _ := d.ConfigItems(id)
	enc := items[services.VaultSecretUnsealKey1]
	if enc == "" {
		t.Fatal("expected unseal key persisted encrypted")
	}
	cd, _ := crypto.New("master") // same master key → can decrypt
	if plain, err := cd.Decrypt(enc); err != nil || plain != "k1" {
		t.Fatalf("unseal key 1 mismatch: plain=%q err=%v", plain, err)
	}
}

func TestVaultUnsealRejectedForNonVaultKind(t *testing.T) {
	d, h := testServer(t)
	id, err := d.CreateService("garage", "garage")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/service/"+id+"/action?op=unseal", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("non-vault unseal must be 400, got %d", rec.Code)
	}
}

func TestVaultUpSizeGuard(t *testing.T) {
	d, h := testServer(t)
	id, err := d.CreateService("vault", "vault")
	if err != nil {
		t.Fatal(err)
	}
	save := func(size, unit string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("POST", "/service/"+id, strings.NewReader(url.Values{
			"VAULT_PORT":             {"8200"},
			"VAULT_VOLUME_SIZE":      {size},
			"VAULT_VOLUME_SIZE_UNIT": {unit},
		}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	up := func() string {
		t.Helper()
		req := httptest.NewRequest("POST", "/service/"+id+"/action?op=up", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Header().Get("Location")
	}
	if rec := save("999999", "T"); rec.Code != http.StatusSeeOther {
		t.Fatalf("save status %d", rec.Code)
	}
	if loc := up(); !strings.Contains(loc, "err=1") {
		t.Fatalf("up with an oversized declaration must hit the size guard, redirect=%q", loc)
	}
	if rec := save("1", "M"); rec.Code != http.StatusSeeOther {
		t.Fatalf("save status %d", rec.Code)
	}
	if loc := up(); loc != "/?msg=up" {
		t.Fatalf("up with a small declared size must not be blocked, redirect=%q", loc)
	}
}

func TestDashboardRendersVaultUnsealButton(t *testing.T) {
	d, h := testServer(t)
	id, err := d.CreateService("vault", "vault")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, "/service/"+id+"/action?op=unseal") {
		t.Fatalf("dashboard should render an Unseal form for vault: %s", body)
	}
}

func TestBasicAuthDisabledWithoutPassword(t *testing.T) {
	d, h := testServer(t)
	_ = d
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("without a password the app must serve the dashboard, got %d", rec.Code)
	}
}

func TestBasicAuth(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	c, _ := crypto.New("master")
	cfg := Config{
		DB:        d,
		Cipher:    c,
		DeployDir: t.TempDir(),
		Docker:    &fakeRunner{},
		Password:  "s3cret",
	}
	h := New(cfg)

	t.Run("rejects unauthenticated", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", rec.Code)
		}
		if got := rec.Header().Get("WWW-Authenticate"); got == "" {
			t.Fatalf("expected WWW-Authenticate challenge header")
		}
	})

	t.Run("rejects wrong password", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		req.SetBasicAuth("admin", "wrong")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", rec.Code)
		}
	})

	t.Run("rejects wrong username", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		req.SetBasicAuth("someoneelse", "s3cret")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", rec.Code)
		}
	})

	t.Run("allows valid credentials", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		req.SetBasicAuth("admin", "s3cret")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
	})
}

// TestPrepareGlitchTipGeneratesAndPersistsSecrets guards the secret lifecycle:
// on first launch the Django SECRET_KEY and the bundled postgres password are
// generated, injected into the launch-time values (so the ephemeral .env
// resolves the compose ${…} placeholders), and persisted ENCRYPTED in config —
// never plaintext — and reused verbatim on every later launch so a restore and
// a recreate keep working against the same data.
func TestPrepareGlitchTipGeneratesAndPersistsSecrets(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	c, _ := crypto.New("master")
	s := &server{cfg: Config{DB: d, Cipher: c, DeployDir: t.TempDir()}}
	id, _ := d.CreateService("glitchtip", "errors")
	dir := filepath.Join(t.TempDir(), id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	values := map[string]string{"GLITCHTIP_PORT": "8000"}
	if err := prepareGlitchTip(s, id, dir, values); err != nil {
		t.Fatal(err)
	}
	if len(values["GLITCHTIP_SECRET_KEY"]) < 32 {
		t.Fatalf("secret key too short: %q", values["GLITCHTIP_SECRET_KEY"])
	}
	if len(values["GLITCHTIP_DB_PASSWORD"]) < 20 {
		t.Fatalf("db password too short: %q", values["GLITCHTIP_DB_PASSWORD"])
	}
	if values["EMAIL_URL"] != "consolemail://" {
		t.Fatalf("blank EMAIL_URL must default to consolemail://, got %q", values["EMAIL_URL"])
	}

	items, _ := d.ConfigItems(id)
	for _, k := range []string{"GLITCHTIP_SECRET_KEY", "GLITCHTIP_DB_PASSWORD"} {
		if items[k] == "" {
			t.Fatalf("%s not persisted", k)
		}
		if items[k] == values[k] {
			t.Fatalf("%s must be stored encrypted, found plaintext", k)
		}
		if dec, derr := c.Decrypt(items[k]); derr != nil || dec != values[k] {
			t.Fatalf("%s encrypt/decrypt mismatch: %v", k, derr)
		}
	}

	// Second launch reuses the stored secrets verbatim.
	again := map[string]string{}
	if err := prepareGlitchTip(s, id, dir, again); err != nil {
		t.Fatal(err)
	}
	if again["GLITCHTIP_SECRET_KEY"] != values["GLITCHTIP_SECRET_KEY"] {
		t.Fatal("SECRET_KEY must be stable across launches")
	}
	if again["GLITCHTIP_DB_PASSWORD"] != values["GLITCHTIP_DB_PASSWORD"] {
		t.Fatal("DB password must be stable across launches")
	}
}
