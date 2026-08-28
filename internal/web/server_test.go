package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jullury/mitia-ops/internal/crypto"
	"github.com/jullury/mitia-ops/internal/db"
)

type fakeRunner struct{}

func (fakeRunner) Run(dir string, args ...string) (string, error) { return "ok", nil }

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
		Docker:    fakeRunner{},
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
		New(Config{DB: d, Cipher: c, DeployDir: deployDir, Docker: fakeRunner{}}).ServeHTTP(rec, req)
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
	New(Config{DB: d, Cipher: c, DeployDir: deployDir, Docker: fakeRunner{}}).ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("up status %d", rec.Code)
	}
	if _, err := os.Stat(envPath); !os.IsNotExist(err) {
		t.Fatalf("ephemeral .env must be removed after up, still exists: %v", err)
	}
}
