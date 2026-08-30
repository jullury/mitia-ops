package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jullury/mitia-ops/internal/crypto"
	"github.com/jullury/mitia-ops/internal/db"
)

// fakeSnapshotRunner records raw calls like fakeRawRunner, but also emulates
// backup/extract containers' file side effects: when a `run` argv carries a
// `:/out` host mount it writes an empty snap.tgz into that mounted host dir on
// a capture run (so BackupService.Run can rename it into the snapshot dir), and
// pgdump/postgres.dump on an extract run (so the postgres restore path can
// stat/`docker cp` it). This lets the orchestrator tests assert on real files +
// DB rows without a Docker daemon.
type fakeSnapshotRunner struct {
	fakeRawRunner
}

func (f *fakeSnapshotRunner) RunRaw(args ...string) (string, error) {
	out, err := f.fakeRawRunner.RunRaw(args...)
	joined := strings.Join(args, " ")
	for _, a := range args {
		if strings.HasSuffix(a, ":/out") {
			host := strings.TrimSpace(strings.TrimSuffix(a, ":/out"))
			host = strings.TrimSpace(strings.TrimPrefix(host, "-v"))
			if host == "" || host == "-v" {
				continue
			}
			if strings.Contains(joined, "pgdump/postgres.dump") {
				_ = os.MkdirAll(filepath.Join(host, "pgdump"), 0o755)
				_ = os.WriteFile(filepath.Join(host, "pgdump", "postgres.dump"), []byte("DUMP"), 0o644)
			} else {
				_ = os.MkdirAll(host, 0o755)
				_ = os.WriteFile(filepath.Join(host, "snap.tgz"), []byte("SNAP"), 0o644)
			}
		}
	}
	return out, err
}

// waitForBackup polls the DB until the service has exactly want backup rows (or
// the timeout elapses). Used because manual backups run in a background job.
func waitForBackup(t *testing.T, d *db.DB, sid string, want int) []db.Backup {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		rows, _ := d.ListBackups(sid)
		if len(rows) == want {
			return rows
		}
		time.Sleep(10 * time.Millisecond)
	}
	rows, _ := d.ListBackups(sid)
	t.Fatalf("expected %d backup rows, got %d", want, len(rows))
	return nil
}

func TestBackupFilenameFormat(t *testing.T) {
	ts := time.Date(2026, 8, 31, 12, 4, 5, 0, time.UTC)
	if got := backupFilename("postgres", ts); got != "20260831T120405-postgres.tar.gz" {
		t.Fatalf("backupFilename = %q", got)
	}
}

func TestScheduleParsing(t *testing.T) {
	if s, err := parseSchedule("daily"); err != nil || s != ScheduleDaily {
		t.Fatalf("daily = %v %v", s, err)
	}
	if _, err := parseSchedule("totally-bogus"); err == nil {
		t.Fatal("bogus schedule should error")
	}
}

func TestEffectiveSchedule(t *testing.T) {
	if s, _ := effectiveSchedule("daily", "inherit"); s != ScheduleDaily {
		t.Fatalf("inherit should follow global, got %v", s)
	}
	if s, _ := effectiveSchedule("daily", "weekly"); s != ScheduleWeekly {
		t.Fatalf("explicit override should win, got %v", s)
	}
	if s, _ := effectiveSchedule("daily", "off"); s != ScheduleOff {
		t.Fatalf("per-service off should disable, got %v", s)
	}
	if s, _ := effectiveSchedule("off", ""); s != ScheduleOff {
		t.Fatalf("global off => off, got %v", s)
	}
}

func TestDue(t *testing.T) {
	now := time.Now()
	if !due(time.Time{}, ScheduleDaily) {
		t.Fatal("no last run should be due")
	}
	if !due(now.Add(-25*time.Hour), ScheduleDaily) {
		t.Fatal("older than daily interval should be due")
	}
	if due(now.Add(-time.Hour), ScheduleDaily) {
		t.Fatal("run within daily interval should not be due")
	}
	if due(time.Time{}, ScheduleOff) {
		t.Fatal("off schedule should never be due")
	}
}

// TestBackupRunWritesSnapshot exercises the orchestrator against a temp FS and
// a recording raw runner (no real Docker). It asserts a snapshot capture runs
// and a backup metadata row is persisted.
func TestBackupRunWritesSnapshot(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	sid, _ := d.CreateService("minio", "m")
	deployDir := t.TempDir()
	svcDir := filepath.Join(deployDir, sid)
	_ = os.MkdirAll(svcDir, 0o755)
	if err := os.WriteFile(filepath.Join(svcDir, "docker-compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	raw := &fakeSnapshotRunner{}
	bs := &BackupService{
		DB:        d,
		Dir:       filepath.Join(t.TempDir(), "backups"),
		Docker:    raw,
		DockerC:   &fakeRunner{},
		DeployDir: deployDir,
	}

	filename, err := bs.Run(sid)
	if err != nil {
		t.Fatalf("backup run: %v", err)
	}
	if filename == "" {
		t.Fatal("expected a backup filename")
	}
	if !strings.HasSuffix(filename, "-minio.tar.gz") {
		t.Fatalf("unexpected filename %q", filename)
	}
	rows, err := d.ListBackups(sid)
	if err != nil || len(rows) != 1 {
		t.Fatalf("expected one backup row, got %v, %v", rows, err)
	}
	if rows[0].Filename != filename || rows[0].Size <= 0 {
		t.Fatalf("backup row mismatch: %+v", rows[0])
	}
	if !strings.Contains(strings.Join(raw.ran, " "), "tar -czf") {
		t.Fatalf("expected a snapshot capture run, got %v", raw.ran)
	}
}

// TestBackupRunResolvesExternalMinioVolume checks the tracked MINIO_VOLUME_NAME
// wins over the project-scoped default when set.
func TestBackupRunResolvesExternalMinioVolume(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	sid, _ := d.CreateService("minio", "m")
	deployDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(deployDir, sid), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := d.SetConfigItems(sid, []db.ConfigItem{{Key: "MINIO_VOLUME_NAME", Value: "external_vol"}}); err != nil {
		t.Fatal(err)
	}
	raw := &fakeSnapshotRunner{}
	bs := &BackupService{
		DB:        d,
		Dir:       filepath.Join(t.TempDir(), "backups"),
		Docker:    raw,
		DockerC:   &fakeRunner{},
		DeployDir: deployDir,
	}
	if _, err := bs.Run(sid); err != nil {
		t.Fatalf("backup run: %v", err)
	}
	joined := strings.Join(raw.ran, " ")
	if !strings.Contains(joined, "external_vol:/snap/volumes/external_vol:ro") {
		t.Fatalf("minio external volume should be captured: %v", joined)
	}
}

func newServiceTestApp(t *testing.T, kind string) (*db.DB, http.Handler, string) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	c, _ := crypto.New("master")
	deployDir := t.TempDir()
	h := New(Config{
		DB:            d,
		Cipher:        c,
		DeployDir:     deployDir,
		Docker:        &fakeRunner{},
		DockerRaw:     &fakeSnapshotRunner{},
		BackupDir:     filepath.Join(t.TempDir(), "backups"),
		BackupSchedule: "off",
	})
	sid, err := d.CreateService(kind, kind)
	if err != nil {
		t.Fatal(err)
	}
	svcDir := filepath.Join(deployDir, sid)
	if err := os.MkdirAll(svcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(svcDir, "docker-compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return d, h, sid
}

func TestManualBackupCreatesRow(t *testing.T) {
	d, h, sid := newServiceTestApp(t, "minio")
	req := httptest.NewRequest("POST", "/service/"+sid+"/backup", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("backup status %d: %s", rec.Code, rec.Body.String())
	}
	waitForBackup(t, d, sid, 1)
	if rows, _ := d.ListBackups(sid); rows[0].Kind != "minio" || rows[0].Size <= 0 {
		t.Fatalf("unexpected backup row: %+v", rows[0])
	}
}

func TestDownloadBackupServesFile(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	sid, _ := d.CreateService("minio", "m")
	c, _ := crypto.New("master")
	backupDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(backupDir, sid), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backupDir, sid, "20260831T120000-minio.tar.gz"), []byte("SNAP"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := d.CreateBackup(db.Backup{ID: "b1", ServiceID: sid, Kind: "minio", Filename: "20260831T120000-minio.tar.gz", Size: 4, CreatedAt: "2026-08-31T12:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	h := New(Config{DB: d, Cipher: c, DeployDir: t.TempDir(), Docker: &fakeRunner{}, BackupDir: backupDir, BackupSchedule: "off"})
	req := httptest.NewRequest("GET", "/service/"+sid+"/backup/b1/download", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("download status %d", rec.Code)
	}
	if rec.Body.String() != "SNAP" {
		t.Fatalf("download body = %q", rec.Body.String())
	}
	if cdisp := rec.Header().Get("Content-Disposition"); !strings.Contains(cdisp, "20260831T120000-minio.tar.gz") {
		t.Fatalf("Content-Disposition = %q", cdisp)
	}
}

func TestRestoreBackupGeneric(t *testing.T) {
	d, h, sid := newServiceTestApp(t, "minio")
	if err := os.MkdirAll(filepath.Join(h.(*App).s.cfg.BackupDir, sid), 0o755); err != nil {
		t.Fatal(err)
	}
	fn := "20260831T120000-minio.tar.gz"
	if err := os.WriteFile(filepath.Join(h.(*App).s.cfg.BackupDir, sid, fn), []byte("SNAP"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := d.CreateBackup(db.Backup{ID: "b1", ServiceID: sid, Kind: "minio", Filename: fn, Size: 4, CreatedAt: "2026-08-31T12:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/service/"+sid+"/backup/b1/restore", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("restore status %d: %s", rec.Code, rec.Body.String())
	}
	app := h.(*App)
	raw := app.s.cfg.DockerRaw.(*fakeSnapshotRunner)
	if !strings.Contains(strings.Join(raw.ran, " "), "xzf") {
		t.Fatalf("restore should unpack the snapshot, got %v", raw.ran)
	}
	composeCalls := strings.Join(app.s.cfg.Docker.(*fakeRunner).recorded(), " ")
	if !strings.Contains(composeCalls, "down") || !strings.Contains(composeCalls, "up -d") {
		t.Fatalf("restore should stop then start the service, got %q", composeCalls)
	}
}

func TestRestoreBackupPostgresReimportsDump(t *testing.T) {
	d, h, sid := newServiceTestApp(t, "postgres")
	app := h.(*App)
	if err := os.MkdirAll(filepath.Join(app.s.cfg.BackupDir, sid), 0o755); err != nil {
		t.Fatal(err)
	}
	fn := "20260831T120000-postgres.tar.gz"
	if err := os.WriteFile(filepath.Join(app.s.cfg.BackupDir, sid, fn), []byte("SNAP"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := d.CreateBackup(db.Backup{ID: "b1", ServiceID: sid, Kind: "postgres", Filename: fn, Size: 4, CreatedAt: "2026-08-31T12:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/service/"+sid+"/backup/b1/restore", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("restore status %d: %s", rec.Code, rec.Body.String())
	}
	joined := strings.Join(app.s.cfg.DockerRaw.(*fakeSnapshotRunner).ran, " ")
	if !strings.Contains(joined, "pg_restore") {
		t.Fatalf("postgres restore should pg_restore the dump, got %v", joined)
	}
	composeCalls := strings.Join(app.s.cfg.Docker.(*fakeRunner).recorded(), " ")
	if !strings.Contains(composeCalls, "up -d") {
		t.Fatalf("postgres restore should start the service before import, got %q", composeCalls)
	}
}

func newSchedulerApp(t *testing.T, global string) (*db.DB, *App) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	c, _ := crypto.New("master")
	deployDir := t.TempDir()
	app := New(Config{
		DB:             d,
		Cipher:         c,
		DeployDir:      deployDir,
		Docker:         &fakeRunner{},
		DockerRaw:      &fakeSnapshotRunner{},
		BackupDir:      filepath.Join(t.TempDir(), "backups"),
		BackupSchedule: global,
	})
	return d, app
}

func TestBackupSchedulerRunsDueService(t *testing.T) {
	d, app := newSchedulerApp(t, "daily")
	// a service with an explicit due (never-backed-up) schedule
	sid, _ := d.CreateService("minio", "m")
	svcDir := filepath.Join(app.s.cfg.DeployDir, sid)
	if err := os.MkdirAll(svcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(svcDir, "docker-compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := d.SetConfigItems(sid, []db.ConfigItem{{Key: "backup_schedule", Value: "daily"}}); err != nil {
		t.Fatal(err)
	}
	// an off-service must NOT be backed up
	offID, _ := d.CreateService("vault", "v")
	if err := d.SetConfigItems(offID, []db.ConfigItem{{Key: "backup_schedule", Value: "off"}}); err != nil {
		t.Fatal(err)
	}

	if n := app.BackupSchedulerOnce(); n != 1 {
		t.Fatalf("scheduler should back up exactly the due service, got %d", n)
	}
	if rows, _ := d.ListBackups(sid); len(rows) != 1 {
		t.Fatalf("due service should have a backup, got %v", rows)
	}
	if rows, _ := d.ListBackups(offID); len(rows) != 0 {
		t.Fatalf("off service must not be backed up, got %v", rows)
	}
	// backup_last_run must advance on success so the next sweep is not due again
	items, _ := d.ConfigItems(sid)
	if items["backup_last_run"] == "" {
		t.Fatalf("backup_last_run should be recorded, got %v", items)
	}
	// a second sweep is now a no-op for the already-backed-up service
	if n := app.BackupSchedulerOnce(); n != 0 {
		t.Fatalf("second sweep should back up nothing, got %d", n)
	}
}

func TestBackupSchedulerSkipsGlobalOff(t *testing.T) {
	d, app := newSchedulerApp(t, "off")
	sid, _ := d.CreateService("minio", "m")
	if err := os.MkdirAll(filepath.Join(app.s.cfg.DeployDir, sid), 0o755); err != nil {
		t.Fatal(err)
	}
	if n := app.BackupSchedulerOnce(); n != 0 {
		t.Fatalf("global off should schedule nothing, got %d", n)
	}
	if rows, _ := d.ListBackups(sid); len(rows) != 0 {
		t.Fatalf("no backups expected, got %v", rows)
	}
}

func TestDeleteServiceRemovesBackupFiles(t *testing.T) {
	d, h, sid := newServiceTestApp(t, "minio")
	app := h.(*App)
	if err := os.MkdirAll(filepath.Join(app.s.cfg.BackupDir, sid), 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(app.s.cfg.BackupDir, sid, "20260831T120000-minio.tar.gz")
	if err := os.WriteFile(f, []byte("SNAP"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := d.CreateBackup(db.Backup{ID: "b1", ServiceID: sid, Kind: "minio", Filename: "20260831T120000-minio.tar.gz", Size: 4, CreatedAt: "2026-08-31T12:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/service/"+sid+"/delete", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("delete status %d", rec.Code)
	}
	if _, err := os.Stat(f); !os.IsNotExist(err) {
		t.Fatalf("backup file should be removed on service delete, got %v", err)
	}
}
