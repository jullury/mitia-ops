package web

import (
	"net/http"
	"net/http/httptest"
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
