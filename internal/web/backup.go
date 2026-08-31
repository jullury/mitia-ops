package web

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jullury/mitia-ops/internal/crypto"
	"github.com/jullury/mitia-ops/internal/db"
	"github.com/jullury/mitia-ops/internal/docker"
	"github.com/jullury/mitia-ops/internal/services"
)

// Schedule enumerates the supported backup cadences.
type Schedule int

const (
	ScheduleOff     Schedule = iota // scheduled backups disabled
	ScheduleInherit                 // follow the global default (per-service only)
	ScheduleHourly
	ScheduleDaily
	ScheduleWeekly
)

// backupFilename renders the stable per-snapshot name "<ts>-<kind>.tar.gz" in
// UTC so multiple snapshots coexist and sort chronologically.
func backupFilename(kind string, t time.Time) string {
	return t.UTC().Format("20060102T150405") + "-" + kind + ".tar.gz"
}

// backupVolumes maps a service kind to the named volumes it owns for backup
// purposes. Values are the compose volume keys; the real Docker volume name is
// resolved per deploy dir (project-scoped) or, for garage, the tracked external
// name. nil/empty means "deploy dir only".
var backupVolumes = map[services.Kind][]string{
	services.KindGarage:   {"garage_data"},
	services.KindPostgres: {"pg_data"},
	services.KindVault:    {"vault_data"},
	services.KindCaddy:    {"caddy_data", "caddy_config"},
}

func parseSchedule(s string) (Schedule, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "off":
		return ScheduleOff, nil
	case "inherit":
		return ScheduleInherit, nil
	case "@hourly", "hourly":
		return ScheduleHourly, nil
	case "daily":
		return ScheduleDaily, nil
	case "weekly":
		return ScheduleWeekly, nil
	default:
		return ScheduleOff, fmt.Errorf("unknown schedule %q", s)
	}
}

func effectiveSchedule(global, perService string) (Schedule, error) {
	ps, err := parseSchedule(perService)
	if err != nil {
		return ScheduleOff, err
	}
	if ps != ScheduleInherit {
		return ps, nil // explicit per-service setting wins (incl. off)
	}
	gs, err := parseSchedule(global)
	if err != nil {
		return ScheduleOff, err
	}
	return gs, nil
}

func scheduleInterval(s Schedule) time.Duration {
	switch s {
	case ScheduleHourly:
		return time.Hour
	case ScheduleWeekly:
		return 7 * 24 * time.Hour
	default: // ScheduleDaily
		return 24 * time.Hour
	}
}

func due(lastRun time.Time, sched Schedule) bool {
	if sched == ScheduleOff {
		return false
	}
	return lastRun.IsZero() || time.Since(lastRun) >= scheduleInterval(sched)
}

// newID returns a fresh UUID string for a backup row id.
func newID() string { return uuid.NewString() }

// BackupService performs and records per-service backup snapshots.
type BackupService struct {
	DB        *db.DB
	Dir       string // snapshot root
	Docker    docker.RawRunner
	DockerC   docker.Runner
	DeployDir string
	Cipher    *crypto.Cipher // decrypts secret config values (e.g. garage access key)
}

// Run takes one backup of the given service and records it. It returns the
// snapshot filename written (or "" when nothing was produced). Secret values are
// never read or written; only the raw volume + deploy-dir contents are captured.
func (b *BackupService) Run(id string) (string, error) {
	svc, err := b.DB.ServiceByID(id)
	if err != nil {
		return "", err
	}
	kind := services.Kind(svc.Kind)
	def, ok := services.Get(kind)
	if !ok {
		return "", fmt.Errorf("unknown kind %q", svc.Kind)
	}
	dir := filepath.Join(b.DeployDir, id)

	vols, err := b.resolveVolumes(def, id)
	if err != nil {
		return "", err
	}

	// Stage any DB-aware dump (postgres only) before the snapshot.
	pgStage := ""
	if kind == services.KindPostgres {
		if stage, derr := b.dumpPostgresStage(id, dir); derr == nil {
			pgStage = stage
			defer os.RemoveAll(pgStage)
		}
	}
	// Stage per-bucket logical dumps (garage only) before the snapshot.
	s3Stage := ""
	if kind == services.KindGarage {
		if stage, derr := b.dumpS3Buckets(id); derr == nil {
			s3Stage = stage
			defer os.RemoveAll(s3Stage)
		}
	}

	stage, err := os.MkdirTemp("", "mitia-ops-backup-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)

	ts := time.Now()
	filename := backupFilename(string(kind), ts)
	out := filepath.Join(stage, "snap.tgz")
	if err := docker.BackupSnapshot(b.Docker, vols, dir, pgStage, s3Stage, out, docker.VolumeImage); err != nil {
		return "", err
	}

	snapDir := filepath.Join(b.Dir, id)
	if err := os.MkdirAll(snapDir, 0o755); err != nil {
		return "", err
	}
	final := filepath.Join(snapDir, filename)
	if err := os.Rename(out, final); err != nil {
		return "", err
	}
	info, err := os.Stat(final)
	if err != nil {
		return "", err
	}

	rec := db.Backup{
		ID:        newID(),
		ServiceID: id,
		Kind:      svc.Kind,
		Filename:  filename,
		Size:      info.Size(),
		CreatedAt: ts.UTC().Format(time.RFC3339),
	}
	if err := b.DB.CreateBackup(rec); err != nil {
		return "", err
	}
	return filename, nil
}

// resolveVolumes returns the actual Docker volume names to back up for a
// service. Non-external compose volumes use docker.VolumeName(dir, name);
// garage's external volume uses the tracked GARAGE_VOLUME_NAME or the fallback.
func (b *BackupService) resolveVolumes(def services.Definition, id string) ([]string, error) {
	raw, ok := backupVolumes[def.Kind]
	if !ok {
		return nil, nil
	}
	dir := filepath.Join(b.DeployDir, id)
	out := make([]string, 0, len(raw))
	for _, name := range raw {
		vol := docker.VolumeName(dir, name)
		if def.Kind == services.KindGarage {
			if items, err := b.DB.ConfigItems(id); err == nil && items["GARAGE_VOLUME_NAME"] != "" {
				vol = items["GARAGE_VOLUME_NAME"]
			}
		}
		out = append(out, vol)
	}
	return out, nil
}

// dumpPostgresStage captures a pg_dump of the running postgres into a temp dir
// and returns its path. A down/unreachable container is a non-fatal skip
// (returns "", nil) so the snapshot can still capture the raw pg_data volume.
func (b *BackupService) dumpPostgresStage(id, dir string) (string, error) {
	items, err := b.DB.ConfigItems(id)
	if err != nil {
		return "", err
	}
	dbName := items["POSTGRES_DB"]
	if dbName == "" {
		dbName = "postgres"
	}
	user := items["POSTGRES_USER"]
	if user == "" {
		user = "postgres"
	}
	container := filepath.Base(dir) + "-postgres-1"
	out, err := docker.DumpPostgres(b.Docker, container, dbName, user)
	if err != nil {
		return "", err
	}
	stage := filepath.Join(b.Dir, id, ".pgdump")
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(stage, "postgres.dump"), out, 0o600); err != nil {
		return "", err
	}
	return stage, nil
}

// backupService builds the snapshot orchestrator from the server config.
func (s *server) backupService() *BackupService {
	return &BackupService{
		DB:        s.cfg.DB,
		Dir:       s.cfg.BackupDir,
		Docker:    s.cfg.DockerRaw,
		DockerC:   s.cfg.Docker,
		DeployDir: s.cfg.DeployDir,
		Cipher:    s.cfg.Cipher,
	}
}

// parseBucketList splits a comma-separated bucket list, trimming whitespace
// and dropping empties. Any invalid characters are rejected.
func parseBucketList(s string) ([]string, error) {
	var out []string
	for _, part := range strings.Split(s, ",") {
		name := strings.TrimSpace(part)
		if name == "" {
			continue
		}
		if strings.ContainsAny(name, " /\\:*?\"<>|") {
			return nil, fmt.Errorf("invalid bucket name %q", name)
		}
		out = append(out, name)
	}
	return out, nil
}

// s3Client builds an mc client for a garage service's running container from
// its stored config (endpoint is the compose-internal service address). The
// secret access key is decrypted from the config store.
func (b *BackupService) s3Client(id string) (docker.S3Client, error) {
	items, err := b.DB.ConfigItems(id)
	if err != nil {
		return docker.S3Client{}, err
	}
	pass := items[garageSecretKeyKey]
	if b.Cipher != nil && pass != "" {
		if dec, derr := b.Cipher.Decrypt(pass); derr == nil {
			pass = dec
		}
	}
	base := filepath.Base(filepath.Join(b.DeployDir, id))
	return docker.S3Client{
		HTTPAddr: "http://" + base + "-garage-1:3900",
		User:     items[garageAccessKeyKey],
		Password: pass,
		Network:  base + "_default",
	}, nil
}

// dumpS3Buckets mirrors each configured bucket into a staging dir under the
// snapshot dir so the snapshot can bundle per-bucket dumps, and returns the
// staging root (or "" when no buckets are configured). A down/unreachable
// server is a non-fatal skip (returns "", nil) so the snapshot still captures
// the raw volume.
func (b *BackupService) dumpS3Buckets(id string) (string, error) {
	items, err := b.DB.ConfigItems(id)
	if err != nil {
		return "", err
	}
	buckets, err := parseBucketList(items[bucketBackupKey])
	if err != nil {
		return "", err
	}
	if len(buckets) == 0 {
		return "", nil
	}
	mc, err := b.s3Client(id)
	if err != nil {
		return "", err
	}
	stageRoot := filepath.Join(b.Dir, id, ".s3")
	// Start from a clean staging dir so buckets removed from the config (or a
	// bucket shrunk on the server) never leak stale files into the snapshot.
	if err := os.RemoveAll(stageRoot); err != nil {
		return "", err
	}
	if err := os.MkdirAll(stageRoot, 0o755); err != nil {
		return "", err
	}
	for _, bucket := range buckets {
		d := filepath.Join(stageRoot, bucket)
		if err := os.MkdirAll(d, 0o755); err != nil {
			return "", err
		}
		if err := docker.MirrorS3Bucket(b.Docker, mc, bucket, d); err != nil {
			return "", fmt.Errorf("mirror bucket %s: %w", bucket, err)
		}
	}
	return stageRoot, nil
}

// backupNow starts a background backup job for a service (so large snapshots
// don't block the HTTP response) and redirects to the service page. Requires a
// configured BackupDir.
func (s *server) backupNow(w http.ResponseWriter, r *http.Request) {
	id := idFromPath(w, r)
	if id == "" {
		return
	}
	if s.cfg.BackupDir == "" {
		http.Error(w, "backups are not configured (MITIAOPS_BACKUPS unset)", 500)
		return
	}
	s.jobs.set(id, JobBackingUp, "Backing up…", "")
	go func() {
		_, err := s.backupService().Run(id)
		if err != nil {
			s.jobs.set(id, JobError, "", err.Error())
			return
		}
		s.jobs.set(id, JobRunning, "Backup complete", "")
	}()
	q := url.Values{}
	q.Set("msg", "backing-up")
	http.Redirect(w, r, "/service/"+id+"?"+q.Encode(), http.StatusSeeOther)
}

// downloadBackup streams a saved snapshot with an attachment disposition.
func (s *server) downloadBackup(w http.ResponseWriter, r *http.Request) {
	id := idFromPath(w, r)
	if id == "" {
		return
	}
	bid := r.PathValue("bid")
	b, err := s.cfg.DB.GetBackup(bid)
	if err != nil || b.ServiceID != id {
		http.NotFound(w, r)
		return
	}
	file := filepath.Join(s.cfg.BackupDir, id, b.Filename)
	w.Header().Set("Content-Disposition", "attachment; filename=\""+b.Filename+"\"")
	http.ServeFile(w, r, file)
}

// restoreBackup restores a chosen snapshot: it stops the service, unpacks the
// deploy dir (and volumes, except postgres which re-imports its dump onto a
// fresh volume), then starts it again. On failure it redirects back with the
// error so it is shown inline on the service page.
func (s *server) restoreBackup(w http.ResponseWriter, r *http.Request) {
	id := idFromPath(w, r)
	if id == "" {
		return
	}
	bid := r.PathValue("bid")
	b, err := s.cfg.DB.GetBackup(bid)
	if err != nil || b.ServiceID != id {
		http.NotFound(w, r)
		return
	}
	if err := s.doRestore(id, b); err != nil {
		q := url.Values{}
		q.Set("err", "1")
		q.Set("msg", "restore failed: "+err.Error())
		http.Redirect(w, r, "/service/"+id+"?"+q.Encode(), http.StatusSeeOther)
		return
	}
	q := url.Values{}
	q.Set("msg", "restored")
	http.Redirect(w, r, "/service/"+id+"?"+q.Encode(), http.StatusSeeOther)
}

// doRestore runs a full snapshot restore for a service. The service is stopped
// first (best-effort), the deployment dir is replaced from the archive, volumes
// are replaced (postgres via logical dump import on a fresh volume; everything
// else via the physical volume tars), and the service is started again.
func (s *server) doRestore(id string, b db.Backup) error {
	svc, err := s.cfg.DB.ServiceByID(id)
	if err != nil {
		return err
	}
	def, ok := services.Get(services.Kind(svc.Kind))
	if !ok {
		return fmt.Errorf("unknown kind %q", svc.Kind)
	}
	dir := s.deployDir(id)
	bs := s.backupService()
	inFile := filepath.Join(s.cfg.BackupDir, id, b.Filename)
	if _, err := os.Stat(inFile); err != nil {
		return fmt.Errorf("snapshot file missing: %w", err)
	}

	// 1. stop the service so no container holds the volumes during restore.
	if _, err := docker.Down(dir, s.cfg.Docker); err != nil {
		return fmt.Errorf("stop service: %w", err)
	}

	vols, err := bs.resolveVolumes(def, id)
	if err != nil {
		return err
	}

	if def.Kind == services.KindPostgres {
		return restorePostgresDump(s, id, bs, dir, vols, inFile)
	}
	if def.Kind == services.KindGarage {
		items, _ := s.cfg.DB.ConfigItems(id)
		if buckets, _ := parseBucketList(items[bucketBackupKey]); len(buckets) > 0 {
			return restoreS3Buckets(s, id, bs, dir, vols, buckets, inFile)
		}
	}

	// 2. generic: replace the owned volumes and the deploy dir from the archive.
	for _, v := range vols {
		if err := docker.EnsureVolume(bs.Docker, v); err != nil {
			return fmt.Errorf("ensure volume %s: %w", v, err)
		}
	}
	if err := docker.RestoreSnapshot(bs.Docker, vols, dir, inFile, docker.VolumeImage); err != nil {
		return err
	}

	// 3. start the service again with the restored data.
	if _, err := docker.Up(dir, s.cfg.Docker); err != nil {
		return err
	}
	return nil
}

// restorePostgresDump restores a postgres backup by re-importing its logical
// dump onto a freshly recreated empty pg_data volume (the dump is authoritative
// over the physical volume tar). The postgres container is started first so the
// import can run against the live, empty database.
func restorePostgresDump(s *server, id string, bs *BackupService, dir string, vols []string, inFile string) error {
	// Drop and recreate the pg_data volume(s) fresh so no stale physical data
	// remains; the dump will repopulate it.
	for _, v := range vols {
		if ok, _ := docker.VolumeExists(bs.Docker, v); ok {
			if err := docker.RemoveVolume(bs.Docker, v); err != nil {
				return fmt.Errorf("remove volume %s: %w", v, err)
			}
		}
		if err := docker.EnsureVolume(bs.Docker, v); err != nil {
			return fmt.Errorf("recreate volume %s: %w", v, err)
		}
	}
	// Restore only the deploy dir (compose + env) so postgres boots with the
	// same credentials and database name.
	if err := docker.RestoreSnapshot(bs.Docker, nil, dir, inFile, docker.VolumeImage); err != nil {
		return fmt.Errorf("restore deploy dir: %w", err)
	}
	// Start postgres on the fresh volume, then import the logical dump.
	if _, err := docker.Up(dir, s.cfg.Docker); err != nil {
		return fmt.Errorf("start postgres: %w", err)
	}
	items, _ := s.cfg.DB.ConfigItems(id)
	dbName := items["POSTGRES_DB"]
	if dbName == "" {
		dbName = "postgres"
	}
	user := items["POSTGRES_USER"]
	if user == "" {
		user = "postgres"
	}
	container := filepath.Base(dir) + "-postgres-1"
	return restorePostgresFromArchive(bs.Docker, inFile, container, dbName, user)
}

// restorePostgresFromArchive extracts the pgdump/postgres.dump member from a
// snapshot, copies it into the running postgres container, and imports it with
// pg_restore (--clean so the fresh-initdb objects are replaced, not duplicated).
func restorePostgresFromArchive(raw docker.RawRunner, inFile, container, db, user string) error {
	dir, err := os.MkdirTemp("", "pgrestore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	// Extract only the dump member via a disposable container, chmod it world
	// readable so the postgres user can read it after docker cp.
	if _, err := raw.RunRaw("run", "--rm",
		"-v", inFile+":/in/snap.tgz:ro",
		"-v", dir+":/out",
		docker.VolumeImage,
		"sh", "-c", "tar -xzf /in/snap.tgz -C /out pgdump/postgres.dump && chmod 0644 /out/pgdump/postgres.dump"); err != nil {
		return fmt.Errorf("extract dump: %w", err)
	}
	dump := filepath.Join(dir, "pgdump", "postgres.dump")
	if _, err := os.Stat(dump); err != nil {
		return fmt.Errorf("backup has no pgdump/postgres.dump: %w", err)
	}
	// Copy the dump into the container, then pg_restore it in place. docker cp
	// runs as root inside the container; pg_restore connects via the trusted
	// local unix socket (peer auth, no password needed).
	if _, err := raw.RunRaw("cp", dump, container+":/tmp/postgres.dump"); err != nil {
		return fmt.Errorf("copy dump into %s: %w", container, err)
	}
	if _, err := raw.RunRaw("exec", container, "pg_restore", "-U", user, "-d", db, "--clean", "--if-exists", "/tmp/postgres.dump"); err != nil {
		return fmt.Errorf("pg_restore: %w", err)
	}
	return nil
}

// extractSnapshotMember extracts a top-level directory (member) from a snapshot
// archive into hostDir via a disposable container. The chmod makes the restored
// files world-readable so they can be bind-mounted back for the next step.
func extractSnapshotMember(raw docker.RawRunner, inFile, member, hostDir string) error {
	abs, err := filepath.Abs(hostDir)
	if err != nil {
		return err
	}
	if _, err := raw.RunRaw("run", "--rm",
		"-v", inFile+":/in/snap.tgz:ro",
		"-v", abs+":/out",
		docker.VolumeImage,
		"sh", "-c", "tar -xzf /in/snap.tgz -C /out "+member+" && chmod -R a+rX /out/"+member); err != nil {
		return fmt.Errorf("extract %s: %w", member, err)
	}
	return nil
}

// restoreS3Buckets restores a garage backup logically, bucket by bucket. The
// data volume is dropped and recreated fresh, the deploy dir is restored, the
// service is started, and each previously-backed-up bucket is recreated and
// repopulated from its per-bucket dump. Only the buckets that were backed up
// (the current config) are restored.
func restoreS3Buckets(s *server, id string, bs *BackupService, dir string, vols []string, buckets []string, inFile string) error {
	// Drop and recreate the garage volume(s) fresh.
	for _, v := range vols {
		if ok, _ := docker.VolumeExists(bs.Docker, v); ok {
			if err := docker.RemoveVolume(bs.Docker, v); err != nil {
				return fmt.Errorf("remove volume %s: %w", v, err)
			}
		}
		if err := docker.EnsureVolume(bs.Docker, v); err != nil {
			return fmt.Errorf("recreate volume %s: %w", v, err)
		}
	}
	// Restore only the deploy dir (compose + env) so garage boots with the same
	// credentials and endpoint.
	if err := docker.RestoreSnapshot(bs.Docker, nil, dir, inFile, docker.VolumeImage); err != nil {
		return fmt.Errorf("restore deploy dir: %w", err)
	}
	// Start garage on the fresh volume.
	if _, err := docker.Up(dir, s.cfg.Docker); err != nil {
		return fmt.Errorf("start garage: %w", err)
	}
	// Extract the per-bucket dumps and mirror each back into a recreated bucket.
	tmp, err := os.MkdirTemp("", "s3restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := extractSnapshotMember(bs.Docker, inFile, "s3", tmp); err != nil {
		return fmt.Errorf("extract bucket dumps: %w", err)
	}
	mc, err := bs.s3Client(id)
	if err != nil {
		return err
	}
	for _, bucket := range buckets {
		hostDir := filepath.Join(tmp, "s3", bucket)
		if err := docker.MakeS3Bucket(bs.Docker, mc, bucket); err != nil {
			return err
		}
		if err := docker.RestoreS3Bucket(bs.Docker, mc, bucket, hostDir); err != nil {
			return fmt.Errorf("restore bucket %s: %w", bucket, err)
		}
	}
	return nil
}

// config keys for per-service backup scheduling.
const (
	backupScheduleKey = "backup_schedule" // inherit | off | @hourly | daily | weekly
	backupLastRunKey  = "backup_last_run" // RFC3339; set when the last backup succeeded
	// bucketBackupKey stores the comma-separated list of garage buckets to back
	// up logically (per bucket). Empty ⇒ fall back to the whole-volume snapshot.
	bucketBackupKey = "garage_backup_buckets"
)

// StartBackupScheduler launches the periodic backup sweep in the background. It
// runs an immediate catch-up sweep (services already due when the process
// starts) and then re-sweeps every interval. Call it once at startup, after the
// DB and Docker runners are ready.
func (s *server) StartBackupScheduler(interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	s.backupSchedulerOnce()
	tick := time.NewTicker(interval)
	go func() {
		defer tick.Stop()
		for range tick.C {
			s.backupSchedulerOnce()
		}
	}()
}

// backupSchedulerOnce sweeps every service and takes backups for those whose
// effective schedule is enabled AND due. It returns how many backups were
// taken. A failed per-service backup does not advance backup_last_run, so the
// next sweep retries it.
func (s *server) backupSchedulerOnce() int {
	if s.cfg.BackupDir == "" {
		return 0
	}
	svcs, err := s.cfg.DB.ListServices()
	if err != nil {
		return 0
	}
	count := 0
	for _, svc := range svcs {
		items, err := s.cfg.DB.ConfigItems(svc.ID)
		if err != nil {
			continue
		}
		sched, err := effectiveSchedule(s.cfg.BackupSchedule, items[backupScheduleKey])
		if err != nil || sched == ScheduleOff {
			continue
		}
		last, _ := time.Parse(time.RFC3339, items[backupLastRunKey])
		if !due(last, sched) {
			continue
		}
		if _, err := s.backupService().Run(svc.ID); err != nil {
			log.Printf("backup %s (%s): %v", svc.Name, svc.Kind, err)
			continue // do not advance last run; retry next sweep
		}
		_ = s.cfg.DB.SetConfigItems(svc.ID, []db.ConfigItem{
			{Key: backupLastRunKey, Value: time.Now().UTC().Format(time.RFC3339)},
		})
		count++
	}
	return count
}
