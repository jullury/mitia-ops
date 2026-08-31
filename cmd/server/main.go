package main

import (
	"bufio"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jullury/mitia-ops/internal/cloudflared"
	"github.com/jullury/mitia-ops/internal/crypto"
	"github.com/jullury/mitia-ops/internal/db"
	"github.com/jullury/mitia-ops/internal/docker"
	"github.com/jullury/mitia-ops/internal/render"
	"github.com/jullury/mitia-ops/internal/services"
	"github.com/jullury/mitia-ops/internal/web"
)

// resizeVolumeNames mirrors web.resizeVolumeNames: kinds whose named volume is
// project-scoped and thus embedded in the rendered compose as an external
// volume name that follows the deploy dir (i.e. the service id).
var resizeVolumeNames = map[services.Kind]string{
	services.KindGarage: "garage_data",
}

// regenerateCompose re-renders the deployment file for a service whose compose
// pins a project-scoped external volume name (e.g. the garage compose mounts
// `name: <id>_garage_data`). The compose on disk predates the id change and
// would still mount the old name, so it is derived again from stored config
// with the volume reference pointing at the migrated (relocated) volume.
func regenerateCompose(d *db.DB, dir, id string) error {
	svc, err := d.ServiceByID(id)
	if err != nil {
		return err
	}
	def, ok := services.Get(services.Kind(svc.Kind))
	if !ok {
		return fmt.Errorf("unknown kind %q", svc.Kind)
	}
	namedVolume, ok := resizeVolumeNames[def.Kind]
	if !ok {
		return nil // this kind's compose never embeds the id
	}
	values, err := d.ConfigItems(id)
	if err != nil {
		return err
	}
	name := values["GARAGE_VOLUME_NAME"]
	if name == "" {
		name = docker.VolumeName(dir, namedVolume)
	}
	values["GARAGE_VOLUME_NAME"] = name
	res, err := render.BuildRenderResult(def.Kind, values)
	if err != nil {
		return err
	}
	return render.WriteCompose(dir, res)
}

// migrateLegacyServices upgrades an install whose service ids were sequential
// integers to UUIDs. It runs the full migration: first the DB half — remap
// services/config_items to fresh UUIDs and record the old->new pairing in
// migrated_ids (a no-op on a schema that already uses TEXT ids) — then the
// on-disk half: bring the stack down, rename the deploy directory to the UUID,
// and relocate every project-scoped Docker volume (which embeds the id, e.g.
// "4_garage_data", "4_mysql-vol-1") so application data survives. Each step
// skips gracefully when the source is already gone, so a crash mid-way just
// picks up where it left off on the next boot. The migrated_ids rows are the
// resume marker and are cleared only once every volume has been relocated.
func migrateLegacyServices(d *db.DB, deployDir string, runner docker.Runner, raw docker.RawRunner) error {
	if _, err := d.MigrateLegacyIDs(); err != nil {
		return fmt.Errorf("migration: remap service ids: %w", err)
	}
	pending, err := d.PendingMigrations()
	if err != nil {
		return fmt.Errorf("migration: read pending id remaps: %w", err)
	}
	if len(pending) == 0 {
		return nil
	}
	allDone := true
	for oldID, newID := range pending {
		oldDir := filepath.Join(deployDir, oldID)
		newDir := filepath.Join(deployDir, newID)

		// Take both paths down first. A fresh run only ever has the old path
		// up, but a crash may have brought a half-renamed project up under the
		// new one; containers must release their volumes before any is replaced.
		// Volumes themselves hold the data and are untouched by `down`.
		for _, dir := range []string{oldDir, newDir} {
			if _, err := os.Stat(filepath.Join(dir, "docker-compose.yml")); err != nil {
				continue
			}
			if _, err := runner.Run(dir, "down"); err != nil {
				log.Printf("migration: compose down %s: %v", dir, err)
			}
		}
		if _, err := os.Stat(oldDir); err == nil {
			if err := os.Rename(oldDir, newDir); err != nil {
				log.Printf("migration: rename %s -> %s: %v", oldDir, newDir, err)
			}
		}

		// Re-derive the rendered deployment file so any id-embedding external
		// volume reference (garage's compose) points at the relocated volume.
		if err := regenerateCompose(d, newDir, newID); err != nil {
			log.Printf("migration: regenerate compose for %s: %v", newID, err)
			allDone = false
		}

		// Relocate every volume carrying the old id prefix (name scheme:
		// <dir base>_<volume>). `docker volume rename` is not universally
		// available, so the data is copied rather than renamed.
		out, _ := raw.RunRaw("volume", "ls", "-q", "--filter", "name=^"+oldID+"_")
		for _, vol := range strings.Fields(out) {
			if !strings.HasPrefix(vol, oldID+"_") {
				continue
			}
			target := newID + strings.TrimPrefix(vol, oldID)
			if err := docker.RelocateVolume(raw, vol, target); err != nil {
				log.Printf("migration: relocate volume %s -> %s: %v", vol, target, err)
				allDone = false
			}
		}
	}
	// Keep the migrated_ids marker until every volume has actually moved, so a
	// failure (e.g. a volume still in use) is retried on the next boot instead
	// of silently leaving data stranded under the old name.
	if !allDone {
		log.Printf("migration: incomplete, %d old id(s) still pending", len(pending))
		return nil
	}
	return d.CompleteMigrations()
}

func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if ok && os.Getenv(k) == "" {
			os.Setenv(k, v)
		}
	}
}

func main() {
	loadDotEnv(".env")
	masterKey := os.Getenv("MITIAOPS_KEY")
	if masterKey == "" {
		if kb, err := os.ReadFile(os.Getenv("MITIAOPS_KEY_FILE")); err == nil {
			masterKey = string(kb)
		}
	}
	if masterKey == "" {
		log.Fatal("set MITIAOPS_KEY (or MITIAOPS_KEY_FILE) before starting")
	}

	cipher, err := crypto.New(masterKey)
	if err != nil {
		log.Fatal(err)
	}

	dataDir := os.Getenv("MITIAOPS_DATA")
	if dataDir == "" {
		dataDir = "data"
	}
	_ = os.MkdirAll(dataDir, 0o755)
	d, err := db.Open(dataDir + "/mitiaops.db")
	if err != nil {
		log.Fatal(err)
	}
	defer d.Close()

	deployDir := os.Getenv("MITIAOPS_DEPLOY")
	if deployDir == "" {
		deployDir = "deployments"
	}

	backupDir := os.Getenv("MITIAOPS_BACKUPS")
	if backupDir == "" {
		backupDir = filepath.Join(dataDir, "backups")
	}
	_ = os.MkdirAll(backupDir, 0o755)

	backupSchedule := os.Getenv("MITIAOPS_BACKUP_SCHEDULE")
	if backupSchedule == "" {
		backupSchedule = "off"
	}

	addr := os.Getenv("MITIAOPS_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	cli := docker.NewCLI()

	if err := migrateLegacyServices(d, deployDir, cli, cli); err != nil {
		log.Fatal(err)
	}
	// Convert any pre-existing minio services to garage (kind + config keys).
	// The old minio data is not imported — Garage's on-disk format differs — so
	// migrated services must re-seed their buckets after their first launch.
	if err := d.MigrateMinioToGarage(); err != nil {
		log.Fatal(err)
	}

	// cloudflared runs through a cloudflare/cloudflared container (never a host
	// install), so the host only needs docker. Its state — the login certificate
	// (cert.pem) and per-tunnel credentials — lives in an app-managed home dir,
	// default <deploy>/cloudflared, overridable via MITIAOPS_CLOUDFLARED_HOME.
	cfHome := os.Getenv("MITIAOPS_CLOUDFLARED_HOME")
	if cfHome == "" {
		cfHome = filepath.Join(deployDir, "cloudflared")
	}
	if abs, err := filepath.Abs(cfHome); err == nil {
		cfHome = abs
	}
	cf := cloudflared.New()
	cf.Raw = cli
	cf.Home = cfHome
	// The cloudflared container runs as the image's uid (65532): create the
	// home dir and make it writable by that uid (chown when possible, else
	// world-writable) so login and tunnel-create can persist state in it.
	if err := cf.EnsureHome(); err != nil {
		log.Fatal(err)
	}
	app := web.New(web.Config{
		DB:             d,
		Cipher:         cipher,
		DeployDir:      deployDir,
		Docker:         cli,
		DockerRaw:      cli,
		Cloudflared:    cf,
		BackupDir:      backupDir,
		BackupSchedule: backupSchedule,
	})
	// Bring back every service flagged "start on boot" (runs `up` per service in
	// the background), so an app restart / host reboot restores the stack.
	app.AutoStart()
	// Scheduled backups: catch up anything already due at boot, then sweep every
	// minute so a service whose cadence comes due gets backed up promptly.
	app.StartBackupScheduler(time.Minute)
	log.Printf("mitia-ops listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, app))
}
