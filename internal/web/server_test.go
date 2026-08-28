package web

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jullury/mitia-ops/internal/crypto"
	"github.com/jullury/mitia-ops/internal/db"
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
		DB:         d,
		Cipher:     c,
		DeployDir:  t.TempDir(),
		MailcowDir: t.TempDir(),
		Docker:     &fakeRunner{},
	}
	return d, New(cfg)
}

func TestDashboard(t *testing.T) {
	d, h := testServer(t)
	id, err := d.CreateService("minio", "main")
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
	// assert a service-row-specific marker, not just "minio" (always present
	// as a dropdown option)
	if !strings.Contains(body, `href="/service/`+itoa(id)+`"`) {
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
		{`time="2026-08-28T22:24:34+03:00" level=warning msg="The \"MINIO_ROOT_USER\" variable is not set. Defaulting to a blank string."` + "\n" + `{"State":"running","Status":"Up 4 minutes"}`, "running", "Running"},
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
	id, err := d.CreateService("minio", "main")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/service/"+itoa(id)+"/action?op=up", nil)
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
	id, err := d.CreateService("minio", "main")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/service/"+itoa(id)+"/action?op=explode", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown op must be rejected with 400, got %d", rec.Code)
	}
}

func TestSaveAndShowService(t *testing.T) {
	d, h := testServer(t)
	id, _ := d.CreateService("minio", "main")
	form := "MINIO_ROOT_USER=admin&MINIO_ROOT_PASSWORD=supersecret&MINIO_HOSTNAME=s3.example.com"
	req := httptest.NewRequest("POST", "/service/"+itoa(id), strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect, got %d", rec.Code)
	}
	// stored value must be encrypted
	items, _ := d.ConfigItems(id)
	if items["MINIO_ROOT_PASSWORD"] == "supersecret" {
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

	save := func(id int64, form string) {
		req := httptest.NewRequest("POST", "/service/"+itoa(id), strings.NewReader(form))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		New(Config{DB: d, Cipher: c, DeployDir: deployDir, Docker: &fakeRunner{}}).ServeHTTP(rec, req)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("save status %d", rec.Code)
		}
	}

	id, _ := d.CreateService("minio", "main")
	save(id, "MINIO_ROOT_PASSWORD=supersecret&MINIO_HOSTNAME=s3.example.com&MINIO_ROOT_USER=admin")

	envPath := filepath.Join(deployDir, itoa(id), ".env")
	if _, err := os.Stat(envPath); !os.IsNotExist(err) {
		t.Fatal("saveService must not write a .env (secrets stay in SQLite)")
	}

	req := httptest.NewRequest("POST", "/service/"+itoa(id)+"/action?op=up", nil)
	rec := httptest.NewRecorder()
	New(Config{DB: d, Cipher: c, DeployDir: deployDir, Docker: &fakeRunner{}}).ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("up status %d", rec.Code)
	}
	if _, err := os.Stat(envPath); !os.IsNotExist(err) {
		t.Fatalf("ephemeral .env must be removed after up, still exists: %v", err)
	}
}

func TestMailcowLifecycleRejected(t *testing.T) {
	dbDir := filepath.Join(t.TempDir(), "test.db")
	d, err := db.Open(dbDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	c, _ := crypto.New("master")
	deployDir := t.TempDir()
	runner := &fakeRunner{}
	h := New(Config{DB: d, Cipher: c, DeployDir: deployDir, MailcowDir: t.TempDir(), Docker: runner})

	id, err := d.CreateService("mailcow", "mail")
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"up", "down", "restart"} {
		req := httptest.NewRequest("POST", "/service/"+itoa(id)+"/action?op="+op, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("mailcow %s must be rejected with 400, got %d", op, rec.Code)
		}
		if got := runner.recorded(); len(got) != 0 {
			t.Fatalf("mailcow %s must be rejected before any docker command runs, got %v", op, got)
		}
		envPath := filepath.Join(deployDir, itoa(id), ".env")
		if _, err := os.Stat(envPath); !os.IsNotExist(err) {
			t.Fatalf("mailcow %s must be rejected before a temp .env is written (at %s)", op, envPath)
		}
	}
}

func TestSaveReadOnlyServiceDoesNotCreateDeployDir(t *testing.T) {
	dbDir := filepath.Join(t.TempDir(), "test.db")
	d, err := db.Open(dbDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	c, _ := crypto.New("master")
	deployDir := t.TempDir()
	h := New(Config{DB: d, Cipher: c, DeployDir: deployDir, MailcowDir: t.TempDir(), Docker: &fakeRunner{}})

	id, err := d.CreateService("mailcow", "mail")
	if err != nil {
		t.Fatal(err)
	}
	form := "MAILCOW_HTTP_PORT=8080"
	req := httptest.NewRequest("POST", "/service/"+itoa(id), strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save status %d", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(deployDir, itoa(id))); !os.IsNotExist(err) {
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
	req := httptest.NewRequest("POST", "/service/"+itoa(id), strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save status %d", rec.Code)
	}

	req = httptest.NewRequest("GET", "/service/"+itoa(id), nil)
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

func TestDashboardMailcowShowsConfigLink(t *testing.T) {
	d, h := testServer(t)
	id, err := d.CreateService("mailcow", "mail")
	if err != nil {
		t.Fatal(err)
	}
	// set the http port so a config url can be derived
	form := "MAILCOW_HTTP_PORT=8080"
	req := httptest.NewRequest("POST", "/service/"+itoa(id), strings.NewReader(form))
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
		if strings.Contains(body, btn) {
			t.Fatalf("dashboard must not render lifecycle buttons for read-only mailcow (found %s): %s", btn, body)
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
	req := httptest.NewRequest("POST", "/service/"+itoa(id), strings.NewReader(form))
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

	dir := filepath.Join(deployDir, itoa(id))
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
	save := httptest.NewRequest("POST", "/service/"+itoa(id), strings.NewReader(form))
	save.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, save)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save status %d", rec.Code)
	}

	up := httptest.NewRequest("POST", "/service/"+itoa(id)+"/action?op=up", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, up)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("up status %d: %s", rec.Code, rec.Body.String())
	}

	dir := filepath.Join(deployDir, itoa(id))
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
	dir := filepath.Join(deployDir, itoa(id))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cached := `{"AccountTag":"abc","TunnelID":"` + testCFID + `","TunnelSecret":"cached-secret"}`
	if err := os.WriteFile(filepath.Join(dir, "creds.json"), []byte(cached), 0o600); err != nil {
		t.Fatal(err)
	}

	form := "CF_TUNNEL=my-tunnel&CF_INGRESS_0_HOST=app.example.com&CF_INGRESS_0_SERVICE=http%3A%2F%2Flocalhost%3A8080"
	save := httptest.NewRequest("POST", "/service/"+itoa(id), strings.NewReader(form))
	save.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, save)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save status %d", rec.Code)
	}

	up := httptest.NewRequest("POST", "/service/"+itoa(id)+"/action?op=up", nil)
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
	save := httptest.NewRequest("POST", "/service/"+itoa(id), strings.NewReader(form))
	save.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, save)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save status %d", rec.Code)
	}

	up := httptest.NewRequest("POST", "/service/"+itoa(id)+"/action?op=up", nil)
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
	save := httptest.NewRequest("POST", "/service/"+itoa(id), strings.NewReader(form))
	save.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, save)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save status %d", rec.Code)
	}

	up := httptest.NewRequest("POST", "/service/"+itoa(id)+"/action?op=up", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, up)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("up should redirect back to the service page, got %d", rec.Code)
	}
	if !strings.HasPrefix(rec.Header().Get("Location"), "/service/"+itoa(id)+"?") {
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
		req := httptest.NewRequest("POST", "/service/"+itoa(id), strings.NewReader(form))
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
	req := httptest.NewRequest("POST", "/service/"+itoa(id), strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save status %d", rec.Code)
	}

	req = httptest.NewRequest("GET", "/service/"+itoa(id), nil)
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
	req := httptest.NewRequest("POST", "/service/"+itoa(id), strings.NewReader(form))
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
	req := httptest.NewRequest("POST", "/service/"+itoa(id), strings.NewReader(form))
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
	id, _ := d.CreateService("minio", "main")

	// Seed initial config via a plain save (no raw runner, no volume yet) so a
	// later size change can then be detected as a real change.
	seed := httptest.NewRequest("POST", "/service/"+itoa(id),
		strings.NewReader("MINIO_ROOT_USER=admin&MINIO_ROOT_PASSWORD=supersecret&MINIO_HOSTNAME=s3.example.com&MINIO_VOLUME_SIZE=1&MINIO_VOLUME_SIZE_UNIT=G"))
	seed.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	seedRec := httptest.NewRecorder()
	New(Config{DB: d, Cipher: c, DeployDir: deployDir, Docker: &fakeRunner{}}).ServeHTTP(seedRec, seed)
	if seedRec.Code != http.StatusSeeOther {
		t.Fatalf("seed save status %d", seedRec.Code)
	}

	raw := &fakeRawRunner{}
	// Changing the size (1G -> 2G) on the main save form must drive the
	// fail-safe resize (volume exists per the fake raw runner).
	req := httptest.NewRequest("POST", "/service/"+itoa(id),
		strings.NewReader("MINIO_ROOT_USER=admin&MINIO_ROOT_PASSWORD=supersecret&MINIO_HOSTNAME=s3.example.com&MINIO_VOLUME_SIZE=2&MINIO_VOLUME_SIZE_UNIT=G"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	New(Config{DB: d, Cipher: c, DeployDir: deployDir, Docker: &fakeRunner{}, DockerRaw: raw}).ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("resize save status %d: %s", rec.Code, rec.Body.String())
	}

	items, _ := d.ConfigItems(id)
	if items["MINIO_VOLUME_SIZE"] != "2G" {
		t.Fatalf("resized size stored = %q, want 2G", items["MINIO_VOLUME_SIZE"])
	}
	if items["MINIO_VOLUME_NAME"] == "" {
		t.Fatal("resize must persist the new volume name")
	}
	composeBytes, err := os.ReadFile(filepath.Join(deployDir, itoa(id), "docker-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(composeBytes), "name: "+items["MINIO_VOLUME_NAME"]) {
		t.Fatalf("compose should reference the new volume name %q: %s", items["MINIO_VOLUME_NAME"], composeBytes)
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
	id, _ := d.CreateService("minio", "main")

	seed := httptest.NewRequest("POST", "/service/"+itoa(id),
		strings.NewReader("MINIO_ROOT_USER=admin&MINIO_ROOT_PASSWORD=supersecret&MINIO_HOSTNAME=s3.example.com&MINIO_VOLUME_SIZE=1&MINIO_VOLUME_SIZE_UNIT=G"))
	seed.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	New(Config{DB: d, Cipher: c, DeployDir: deployDir, Docker: &fakeRunner{}}).ServeHTTP(httptest.NewRecorder(), seed)

	raw := &fakeRawRunner{}
	// Same size again: should be a plain save, no volume commands.
	req := httptest.NewRequest("POST", "/service/"+itoa(id),
		strings.NewReader("MINIO_ROOT_USER=admin&MINIO_ROOT_PASSWORD=supersecret&MINIO_HOSTNAME=s3.example.com&MINIO_VOLUME_SIZE=1&MINIO_VOLUME_SIZE_UNIT=G"))
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
	// A brand-new minio service has no volume yet; saving a size is a plain
	// save (no raw runner needed). The volume is later created at that size by
	// EnsureVolume on `up`.
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	c, _ := crypto.New("master")
	id, _ := d.CreateService("minio", "main")

	req := httptest.NewRequest("POST", "/service/"+itoa(id),
		strings.NewReader("MINIO_ROOT_USER=admin&MINIO_ROOT_PASSWORD=supersecret&MINIO_HOSTNAME=s3.example.com&MINIO_VOLUME_SIZE=1&MINIO_VOLUME_SIZE_UNIT=G"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	New(Config{DB: d, Cipher: c, DeployDir: t.TempDir(), Docker: &fakeRunner{}}).ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("first save status %d", rec.Code)
	}
	items, _ := d.ConfigItems(id)
	if items["MINIO_VOLUME_SIZE"] != "1G" {
		t.Fatalf("first save stored size = %q, want 1G", items["MINIO_VOLUME_SIZE"])
	}
}

func TestShowServicePrefillsSizePicker(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	c, _ := crypto.New("master")
	id, _ := d.CreateService("minio", "main")

	save := httptest.NewRequest("POST", "/service/"+itoa(id),
		strings.NewReader("MINIO_ROOT_USER=admin&MINIO_ROOT_PASSWORD=supersecret&MINIO_HOSTNAME=s3.example.com&MINIO_VOLUME_SIZE=1&MINIO_VOLUME_SIZE_UNIT=G"))
	save.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	New(Config{DB: d, Cipher: c, DeployDir: t.TempDir(), Docker: &fakeRunner{}}).ServeHTTP(httptest.NewRecorder(), save)

	get := httptest.NewRequest("GET", "/service/"+itoa(id), nil)
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
	id, _ := d.CreateService("minio", "main")

	// 999999T (~1000 PiB) exceeds the free space of any real filesystem.
	req := httptest.NewRequest("POST", "/service/"+itoa(id),
		strings.NewReader("MINIO_ROOT_USER=admin&MINIO_ROOT_PASSWORD=supersecret&MINIO_HOSTNAME=s3.example.com&MINIO_VOLUME_SIZE=999999&MINIO_VOLUME_SIZE_UNIT=T"))
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
	if items["MINIO_VOLUME_SIZE"] != "" {
		t.Fatalf("rejected size must not be persisted, stored %q", items["MINIO_VOLUME_SIZE"])
	}
}
