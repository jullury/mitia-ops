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
	New(Config{DB: d, Cipher: c, DeployDir: deployDir, Docker: fakeRunner{}}).ServeHTTP(seedRec, seed)
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
	New(Config{DB: d, Cipher: c, DeployDir: deployDir, Docker: fakeRunner{}, DockerRaw: raw}).ServeHTTP(rec, req)
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
	New(Config{DB: d, Cipher: c, DeployDir: deployDir, Docker: fakeRunner{}}).ServeHTTP(httptest.NewRecorder(), seed)

	raw := &fakeRawRunner{}
	// Same size again: should be a plain save, no volume commands.
	req := httptest.NewRequest("POST", "/service/"+itoa(id),
		strings.NewReader("MINIO_ROOT_USER=admin&MINIO_ROOT_PASSWORD=supersecret&MINIO_HOSTNAME=s3.example.com&MINIO_VOLUME_SIZE=1&MINIO_VOLUME_SIZE_UNIT=G"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	New(Config{DB: d, Cipher: c, DeployDir: deployDir, Docker: fakeRunner{}, DockerRaw: raw}).ServeHTTP(rec, req)
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
	New(Config{DB: d, Cipher: c, DeployDir: t.TempDir(), Docker: fakeRunner{}}).ServeHTTP(rec, req)
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
	New(Config{DB: d, Cipher: c, DeployDir: t.TempDir(), Docker: fakeRunner{}}).ServeHTTP(httptest.NewRecorder(), save)

	get := httptest.NewRequest("GET", "/service/"+itoa(id), nil)
	rec := httptest.NewRecorder()
	New(Config{DB: d, Cipher: c, DeployDir: t.TempDir(), Docker: fakeRunner{}}).ServeHTTP(rec, get)
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
	New(Config{DB: d, Cipher: c, DeployDir: t.TempDir(), Docker: fakeRunner{}}).ServeHTTP(rec, req)
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
