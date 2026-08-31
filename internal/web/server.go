package web

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jullury/mitia-ops/internal/crypto"
	"github.com/jullury/mitia-ops/internal/db"
	"github.com/jullury/mitia-ops/internal/docker"
	"github.com/jullury/mitia-ops/internal/render"
	"github.com/jullury/mitia-ops/internal/services"
)

//go:embed templates/*.html
var tmplFS embed.FS

type Config struct {
	DB          *db.DB
	Cipher      *crypto.Cipher
	DeployDir   string
	Docker      docker.Runner
	DockerRaw   docker.RawRunner
	Cloudflared CloudflaredCLI // optional: drives tunnel creation at launch for cloudflared services
	// BackupDir is the snapshot root directory (MITIAOPS_BACKUPS). When empty,
	// backups are disabled for every service.
	BackupDir string
	// BackupSchedule is the global default backup cadence: "" | "off" | "daily"
	// | "weekly" | "@hourly". Per-service overrides win when "inherit" or a
	// concrete value is chosen.
	BackupSchedule string
}

// CloudflaredCLI is the subset of the cloudflared binary the app drives when
// preparing a locally-managed tunnel at launch.
type CloudflaredCLI interface {
	LoggedIn() bool
	// LoginURL starts (or restarts) the interactive cloudflared-login container
	// and returns the Cloudflare authorization URL the user must open in a
	// browser to complete the login.
	LoginURL() (string, error)
	// EnsureTunnel returns the tunnel id, creating it if needed. creds are the
	// freshly-issued credentials for a tunnel created just now; they are nil
	// when the tunnel already existed (callers reuse their cached copy).
	EnsureTunnel(name string) (id string, creds []byte, err error)
	// RouteDNS points the DNS CNAME for hostname at the named tunnel
	// (idempotently), so saved ingress hostnames actually route to it.
	RouteDNS(tunnel, hostname string) error
}

func New(cfg Config) *App {
	// base.html provides the shared layout via `{{ template "content" . }}`,
	// and each page defines that content block. Because every page uses the
	// same "content" name, parse base together with exactly one page per set
	// so the block resolves without cross-page collision.
	funcs := template.FuncMap{
		"linkify":      linkify,
		"statusClass":  statusClass,
		"statusTitle":  statusTitle,
		"sizeNum":      sizeNum,
		"sizeSuffix":   sizeSuffix,
		"listSuffixes": listSuffixes,
	}
	dashTmpl := template.Must(template.New("base.html").Funcs(funcs).ParseFS(tmplFS, "templates/base.html", "templates/dashboard.html"))
	svcTmpl := template.Must(template.New("base.html").Funcs(funcs).ParseFS(tmplFS, "templates/base.html", "templates/service.html"))
	s := &server{cfg: cfg, dashTmpl: dashTmpl, svcTmpl: svcTmpl, jobs: newJobTracker()}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.dashboard)
	mux.HandleFunc("POST /service/new", s.newService)
	mux.HandleFunc("GET /service/{id}", s.showService)
	mux.HandleFunc("POST /service/{id}", s.saveService)
	mux.HandleFunc("POST /service/{id}/action", s.serviceAction)
	mux.HandleFunc("POST /service/{id}/delete", s.deleteService)
	mux.HandleFunc("GET /service/{id}/status", s.serviceStatus)
	mux.HandleFunc("POST /service/{id}/backup", s.backupNow)
	mux.HandleFunc("GET /service/{id}/backup/{bid}/download", s.downloadBackup)
	mux.HandleFunc("POST /service/{id}/backup/{bid}/restore", s.restoreBackup)
	return &App{mux: mux, s: s}
}

// App is the mitia-ops web application: an http.Handler carrying the routes
// plus a startup hook that restores previously-running services (see
// AutoStart).
type App struct {
	mux *http.ServeMux
	s   *server
}

func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mux.ServeHTTP(w, r)
}

// AutoStart brings up every service flagged "start on boot", one background
// goroutine per service. Call it once at process startup, after the DB and
// Docker runner are ready, so an app restart (host reboot included) restores
// the previously-running stack. Failures are logged per service, never fatal —
// a service missing required config surfaces through its own UI as usual.
func (a *App) AutoStart() {
	a.s.AutoStart()
}

// BackupSchedulerOnce runs one scheduling sweep immediately (used as a test
// seam and by StartBackupScheduler's startup catch-up). Returns the number of
// backups taken.
func (a *App) BackupSchedulerOnce() int { return a.s.backupSchedulerOnce() }

// StartBackupScheduler launches the periodic backup sweep in the background.
// Call once at startup; see server.StartBackupScheduler.
func (a *App) StartBackupScheduler(interval time.Duration) { a.s.StartBackupScheduler(interval) }

type server struct {
	cfg      Config
	dashTmpl *template.Template
	svcTmpl  *template.Template
	jobs     *jobTracker
}

// JobState is the coarse lifecycle state of a long-running background
// deployment (used by mailcow, whose first start clones and pulls a large
// stack). The UI polls serviceStatus and humanises these states.
type JobState string

const (
	JobCloning   JobState = "cloning"
	JobPulling   JobState = "pulling"
	JobBackingUp JobState = "backing-up"
	JobRunning   JobState = "running"
	JobError     JobState = "error"
)

// job is one in-flight background deployment for a single service.
type job struct {
	state JobState
	msg   string
	err   string
}

// jobTracker holds the current background deployment job per service id. It is
// intentionally simple: one job per service, retained briefly after it ends so
// the polling UI can read the final state before it clears.
type jobTracker struct {
	mu   sync.Mutex
	jobs map[string]job
}

func newJobTracker() *jobTracker {
	return &jobTracker{jobs: map[string]job{}}
}

func (t *jobTracker) set(id string, state JobState, msg, err string) {
	t.mu.Lock()
	t.jobs[id] = job{state: state, msg: msg, err: err}
	t.mu.Unlock()
}

func (t *jobTracker) get(id string) (job, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	j, ok := t.jobs[id]
	return j, ok
}

func (t *jobTracker) active(id string) bool {
	j, ok := t.get(id)
	return ok && (j.state == JobCloning || j.state == JobPulling)
}

type dashData struct {
	Kinds    []services.Definition
	Services []dashRow
	Msg      string
}

// listSuffixes joins a FieldList's column suffixes (e.g. "HOST,SERVICE") for
// the traffic-routing editor's JS row handling.
func listSuffixes(cols []services.ListColumn) string {
	parts := make([]string, len(cols))
	for i, c := range cols {
		parts[i] = c.Suffix
	}
	return strings.Join(parts, ",")
}

var urlRe = regexp.MustCompile(`https?://\S+`)

// linkify escapes a flash message for safe HTML and wraps any http(s) URLs in
// it in links that open in a new tab (e.g. the Cloudflare login authorization
// URL the app surfaces in the web UI).
func linkify(s string) template.HTML {
	var b strings.Builder
	last := 0
	for _, m := range urlRe.FindAllStringIndex(s, -1) {
		b.WriteString(template.HTMLEscapeString(s[last:m[0]]))
		u := template.HTMLEscapeString(s[m[0]:m[1]])
		b.WriteString(`<a href="` + u + `" target="_blank" rel="noopener">` + u + `</a>`)
		last = m[1]
	}
	b.WriteString(template.HTMLEscapeString(s[last:]))
	return template.HTML(b.String())
}

// sizeNum returns the numeric part of a canonical size value ("100G" -> "100").
func sizeNum(v string) string { n, _ := services.SplitSize(v); return n }

// sizeSuffix returns the unit-suffix part of a canonical size value
// ("100G" -> "G").
func sizeSuffix(v string) string { _, s := services.SplitSize(v); return s }

var actionMsg = map[string]string{
	"up":          "Service started",
	"down":        "Service stopped",
	"restart":     "Service restarted",
	"deleted":     "Service deleted",
	"deleted_err": "Service deleted, but containers could not be brought down — they may still be running",
}

var serviceMsg = map[string]string{
	"saved":              "Settings saved",
	"resized":            "Volume resized — data preserved",
	"backing-up":         "Backup started — the snapshot will appear below when finished",
	"restored":           "Restored from snapshot",
	"vault-unsealed":     "Vault unsealed — it is ready to use",
	"vault-still-sealed": "Vault is still sealed (needs more unseal keys); initialize/unseal it via the UI or the vault CLI",
}

type dashRow struct {
	db.Service
	Status    string
	ReadOnly  bool
	ConfigURL string
	JobState  string
	JobMsg    string
}

var stateRe = regexp.MustCompile(`"State":\s*"([a-zA-Z]+)"`)

// statusClass inspects the raw `docker compose ps --format json` output. That
// output may have stderr warnings (e.g. "variable is not set") prepended, so
// we scan the whole string for the JSON "State" field rather than trusting a
// single line. For multi-container services we report the worst state present.
func statusClass(raw string) string {
	var states []string
	for _, m := range stateRe.FindAllStringSubmatch(raw, -1) {
		states = append(states, strings.ToLower(m[1]))
	}
	if len(states) == 0 {
		return fallbackStatusClass(raw)
	}
	has := func(s string) bool {
		for _, st := range states {
			if st == s {
				return true
			}
		}
		return false
	}
	switch {
	case has("restarting"), has("paused"):
		return "warning"
	case has("running"):
		return "running"
	case has("exited"), has("created"), has("stopped"), has("dead"):
		return "stopped"
	default:
		return "unknown"
	}
}

// fallbackStatusClass handles non-JSON output (e.g. an error or a version of
// docker without the json format) via simple keyword heuristics.
func fallbackStatusClass(raw string) string {
	lower := strings.ToLower(raw)
	switch {
	case strings.Contains(lower, "running"), strings.Contains(lower, " up "):
		return "running"
	case strings.Contains(lower, "restarting"), strings.Contains(lower, "paused"):
		return "warning"
	case strings.Contains(lower, "exited"),
		strings.Contains(lower, "dead"),
		strings.Contains(lower, "stopped"),
		strings.Contains(lower, "created"):
		return "stopped"
	default:
		return "unknown"
	}
}

func statusTitle(raw string) string {
	switch statusClass(raw) {
	case "running":
		return "Running"
	case "warning":
		return "Warning"
	case "stopped":
		return "Stopped"
	default:
		return "Unknown"
	}
}

func (s *server) dashboard(w http.ResponseWriter, r *http.Request) {
	svcs, err := s.cfg.DB.ListServices()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	rows := make([]dashRow, 0, len(svcs))
	for _, svc := range svcs {
		status, _ := docker.Status(s.statusDir(svc), s.cfg.Docker)
		url := ""
		ro := false
		if def, ok := services.Get(services.Kind(svc.Kind)); ok {
			ro = def.ReadOnly
			if def.ConfigURL != nil {
				items, _ := s.cfg.DB.ConfigItems(svc.ID)
				url = def.ConfigURL(items)
			}
		}
		jobState, jobMsg := "", ""
		if j, ok := s.jobs.get(svc.ID); ok {
			jobState = string(j.state)
			if jobState != string(JobError) {
				jobMsg = j.msg
			}
		}
		rows = append(rows, dashRow{
			Service:   svc,
			Status:    status,
			ReadOnly:  ro,
			ConfigURL: url,
			JobState:  jobState,
			JobMsg:    jobMsg,
		})
	}
	msg := actionMsg[r.URL.Query().Get("msg")]
	s.dashTmpl.ExecuteTemplate(w, "base.html", dashData{Kinds: services.All(), Services: rows, Msg: msg})
}

func (s *server) newService(w http.ResponseWriter, r *http.Request) {
	kind := r.FormValue("kind")
	if _, ok := services.Get(services.Kind(kind)); !ok {
		http.Error(w, "unknown kind", 400)
		return
	}
	id, err := s.cfg.DB.CreateService(kind, kind)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/service/"+id, http.StatusSeeOther)
}

func (s *server) showService(w http.ResponseWriter, r *http.Request) {
	id := idFromPath(w, r)
	if id == "" {
		return
	}
	msg := r.URL.Query().Get("msg")
	if text, ok := serviceMsg[msg]; ok {
		msg = text
	}
	s.renderService(w, r, id, msg, r.URL.Query().Get("err") == "1")
}

// renderService re-renders a service's detail form. It is shared by the GET
// handler and the save path (which falls back to it on a validation or I/O
// error so the user sees the error inline rather than a bare error page).
// The saved values are re-read from the DB; on an error the prior (persisted)
// values are shown, since nothing was written.
func (s *server) renderService(w http.ResponseWriter, r *http.Request, id string, msg string, msgErr bool) {
	svc, err := s.cfg.DB.ServiceByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	def, ok := services.Get(services.Kind(svc.Kind))
	if !ok {
		http.Error(w, "unknown kind", 500)
		return
	}
	items, _ := s.cfg.DB.ConfigItems(id)
	values, secrets, lists := decryptValues(s.cfg.Cipher, def, items, false)
	url := ""
	if def.ConfigURL != nil {
		url = def.ConfigURL(values)
	}
	jobState, jobMsg := "", ""
	if j, ok := s.jobs.get(id); ok {
		jobState = string(j.state)
		if jobState != string(JobError) {
			jobMsg = j.msg
		}
	}
	backups, _ := s.cfg.DB.ListBackups(id)
	isMinio := services.Kind(svc.Kind) == services.KindMinio
	var minioBuckets []string
	enabledBuckets := map[string]bool{}
	minioErr := ""
	if isMinio {
		enabled, _ := parseBucketList(items[bucketBackupKey])
		for _, b := range enabled {
			enabledBuckets[b] = true
		}
		detected := []string{}
		if s.cfg.BackupDir != "" && s.cfg.DockerRaw != nil {
			bs := s.backupService()
			mc, merr := bs.minioClient(id)
			if merr == nil {
				if list, lerr := docker.ListMinioBuckets(s.cfg.DockerRaw, mc); lerr == nil {
					detected = list
				} else {
					minioErr = "could not list buckets (is the service running?)"
				}
			}
		}
		// Union of detected and already-enabled buckets so a down server (or a
		// brand-new bucket) does not drop the user's saved selection on save.
		seen := map[string]bool{}
		all := append([]string{}, enabled...)
		for _, b := range detected {
			if !seen[b] && !enabledBuckets[b] {
				all = append(all, b)
			}
		}
		for _, b := range all {
			if !seen[b] {
				seen[b] = true
				minioBuckets = append(minioBuckets, b)
			}
		}
	}
	s.svcTmpl.ExecuteTemplate(w, "base.html", svcData{
		Def:              def,
		Service:          svc,
		Values:           values,
		SecretSet:        secrets,
		Lists:            lists,
		ConfigURL:        url,
		Msg:              msg,
		MsgError:         msgErr,
		HasSize:          hasSizeField(def),
		Autostart:        items[autostartKey] == "true",
		JobState:         jobState,
		JobMsg:           jobMsg,
		Backups:          backups,
		BackupConfigured: s.cfg.BackupDir != "",
		BackupSchedule:   items[backupScheduleKey],
		BackupSchedules:  []string{"inherit", "off", "@hourly", "daily", "weekly"},
		IsMinio:          isMinio,
		MinioBuckets:     minioBuckets,
		EnabledBuckets:   enabledBuckets,
		MinioBucketsErr:  minioErr,
	})
}

type svcData struct {
	Def       services.Definition
	Service   *db.Service
	Values    map[string]string              // non-secret values for inputs (decrypted secrets excluded)
	SecretSet map[string]bool                // field key -> true if a secret value is stored
	Lists     map[string][]map[string]string // FieldList key -> ordered rows (suffix -> value)
	ConfigURL string
	Msg       string // flash message
	MsgError  bool   // true to render Msg as a danger/error notice
	HasSize   bool   // true if the service has a FieldSize (resizeable volume)
	Autostart bool   // true if the service is flagged to start on boot
	JobState  string // background deployment state, or "" when none
	JobMsg    string // human-readable progress message for the job
	// Backups is this service's saved snapshots, newest first. BackupConfigured
	// reports whether the app was started with a backup directory.
	Backups          []db.Backup
	BackupConfigured bool
	BackupSchedule   string // per-service schedule selector value
	BackupSchedules  []string
	// Minio per-bucket backup toggles: IsMinio flags the kind, MinioBuckets is
	// the bucket names detected on the running server (may be empty when the
	// service is down), EnabledBuckets is the set the user has chosen, and
	// MinioBucketsErr reports a detection failure (server unreachable) so the
	// UI can show an offline note instead of silently hiding the section.
	IsMinio         bool
	MinioBuckets    []string
	EnabledBuckets  map[string]bool
	MinioBucketsErr string
}

// hasSizeField reports whether a definition declares a FieldSize field (i.e. a
// resizeable data volume).
func hasSizeField(def services.Definition) bool {
	for _, f := range def.Fields {
		if f.Type == services.FieldSize {
			return true
		}
	}
	return false
}

func (s *server) saveService(w http.ResponseWriter, r *http.Request) {
	id := idFromPath(w, r)
	if id == "" {
		return
	}
	svc, err := s.cfg.DB.ServiceByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	def, ok := services.Get(services.Kind(svc.Kind))
	if !ok {
		http.Error(w, "unknown kind", 500)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	// preserve existing secrets that were left blank
	existing, _ := s.cfg.DB.ConfigItems(id)
	items := make([]db.ConfigItem, 0, len(def.Fields))
	values := map[string]string{}
	stale := []string{} // stored FieldList keys dropped by this save
	kept := map[string]bool{}
	// form is a single-valued view of the form for FieldList row parsing.
	form := map[string]string{}
	for k, vs := range r.Form {
		if len(vs) > 0 {
			form[k] = vs[len(vs)-1]
		}
	}
	for _, f := range def.Fields {
		raw := strings.TrimSpace(r.FormValue(f.Key))
		switch f.Type {
		case services.FieldSecret:
			if raw == "" {
				if enc, ok := existing[f.Key]; ok {
					// keep as-is (already encrypted)
					items = append(items, db.ConfigItem{Key: f.Key, Value: enc})
					if dec, err := s.cfg.Cipher.Decrypt(enc); err == nil {
						values[f.Key] = dec
					}
					continue
				}
				continue
			}
			enc, err := s.cfg.Cipher.Encrypt(raw)
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			items = append(items, db.ConfigItem{Key: f.Key, Value: enc})
			values[f.Key] = raw
		case services.FieldSize:
			// numeric part + unit dropdown are combined into one
			// canonical Docker size value (e.g. "100G").
			unit := strings.TrimSpace(r.FormValue(f.Key + "_UNIT"))
			val := raw
			if val != "" && unit != "" {
				val += unit
			}
			values[f.Key] = val
			items = append(items, db.ConfigItem{Key: f.Key, Value: val})
		case services.FieldBool:
			if r.FormValue(f.Key) == "" {
				values[f.Key] = "false"
			} else {
				values[f.Key] = "true"
			}
			items = append(items, db.ConfigItem{Key: f.Key, Value: values[f.Key]})
		case services.FieldList:
			rows := services.ListRows(form, f.Key, f.Columns)
			// A row with some (but not all) cells filled is a mistake; a
			// completely blank row terminates the scan. Fully-empty rows are
			// dropped, so a row of blanks doesn't persist empty keys.
			for i, row := range rows {
				hasEmpty, hasValue := false, false
				for _, c := range f.Columns {
					if row[c.Suffix] == "" {
						hasEmpty = true
					} else {
						hasValue = true
					}
				}
				if hasEmpty && hasValue {
					s.renderService(w, r, id, fmt.Sprintf("%s rule %d: every field must be filled", f.Label, i+1), true)
					return
				}
			}
			for i, row := range rows {
				for _, c := range f.Columns {
					key := services.ListItemKey(f.Key, i, c.Suffix)
					values[key] = row[c.Suffix]
					items = append(items, db.ConfigItem{Key: key, Value: row[c.Suffix]})
					kept[key] = true
				}
			}
			// Any previously-stored row keys this save no longer produced are
			// stale and must be removed (config_items upserts, never deletes).
			for stored := range existing {
				if strings.HasPrefix(stored, f.Key+"_") && !kept[stored] {
					stale = append(stale, stored)
				}
			}
		default:
			values[f.Key] = raw
			items = append(items, db.ConfigItem{Key: f.Key, Value: values[f.Key]})
		}
	}
	// Start-on-boot is a universal toggle, not a kind field: a checked box
	// keeps the service running across app restarts / host reboots.
	autostart := "false"
	if r.FormValue(autostartKey) != "" {
		autostart = "true"
	}
	items = append(items, db.ConfigItem{Key: autostartKey, Value: autostart})
	// The per-service backup schedule is likewise universal: inherit (default)
	// follows the global MITIAOPS_BACKUP_SCHEDULE, otherwise an explicit value.
	schedule := strings.TrimSpace(r.FormValue(backupScheduleKey))
	if schedule == "" {
		schedule = "inherit"
	}
	items = append(items, db.ConfigItem{Key: backupScheduleKey, Value: schedule})
	// Minio-only: the set of buckets to back up, given by the "minio_bucket_<n>"
	// checkboxes (value = bucket name). Empty ⇒ fall back to the whole-volume
	// snapshot. Form field order is stable, so sorting the names keeps the
	// stored list deterministic for rendering.
	if def.Kind == services.KindMinio {
		var enabled []string
		for key, vs := range r.Form {
			if !strings.HasPrefix(key, "minio_bucket_") || len(vs) == 0 || vs[0] == "" {
				continue
			}
			enabled = append(enabled, vs[len(vs)-1])
		}
		sort.Strings(enabled)
		items = append(items, db.ConfigItem{Key: bucketBackupKey, Value: strings.Join(enabled, ",")})
	}
	dir := s.deployDir(id)

	// A change to a resizeable data volume's size is preflighted for free space
	// and, when a live volume already exists to swap, drives a live resize (data
	// preserved). So a size-picker edit + Save is the single way to resize. A
	// brand-new service has no volume yet: the plain save (below) persists the
	// size, and EnsureVolume applies it on the next `up`. Either way a size the
	// disk can't fit is rejected here up front.
	if namedVolume, ok := resizeVolumeNames[def.Kind]; ok {
		newSize := values["MINIO_VOLUME_SIZE"]
		if newSize != "" && newSize != existing["MINIO_VOLUME_SIZE"] {
			if _, _, msg, ok := s.sizePreflight(dir, id, newSize); !ok {
				s.renderService(w, r, id, msg, true)
				return
			}
			if s.cfg.DockerRaw != nil {
				cfg, _ := s.cfg.DB.ConfigItems(id)
				currentName := cfg["MINIO_VOLUME_NAME"]
				if currentName == "" {
					currentName = docker.VolumeName(dir, namedVolume)
				}
				if ok, _ := docker.VolumeExists(s.cfg.DockerRaw, currentName); ok {
					s.doResize(w, r, id, def, dir, namedVolume, items, values)
					return
				}
			}
		}
	}

	if err := s.cfg.DB.SetConfigItems(id, items); err != nil {
		s.renderService(w, r, id, err.Error(), true)
		return
	}
	if len(stale) > 0 {
		if err := s.cfg.DB.DeleteConfigItems(id, stale); err != nil {
			s.renderService(w, r, id, err.Error(), true)
			return
		}
	}
	s.injectVolumeName(dir, id, def.Kind, values)
	res, err := render.BuildRenderResult(def.Kind, values)
	if err != nil {
		s.renderService(w, r, id, err.Error(), true)
		return
	}
	// Save persists only the non-secret docker-compose.yml, and only for kinds
	// that actually render a compose payload. Read-only kinds (and any kind
	// with an empty render) write nothing, so no deployments/<id>/ dir is
	// created for them. Secrets are never written to a .env here; they stay in
	// SQLite (encrypted) and are decrypted into a temporary .env only at
	// launch time.
	if strings.TrimSpace(res.ComposeYAML) != "" {
		if err := render.WriteCompose(dir, res); err != nil {
			s.renderService(w, r, id, err.Error(), true)
			return
		}
	}
	http.Redirect(w, r, "/service/"+id+"?msg=saved", http.StatusSeeOther)
}

func (s *server) serviceAction(w http.ResponseWriter, r *http.Request) {
	id := idFromPath(w, r)
	if id == "" {
		return
	}
	op := r.URL.Query().Get("op")
	dir := s.deployDir(id)

	svc, err := s.cfg.DB.ServiceByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	def, ok := services.Get(services.Kind(svc.Kind))
	if !ok {
		http.Error(w, "unknown kind", 500)
		return
	}
	if def.ReadOnly {
		http.Error(w, "read-only service has no lifecycle control", 400)
		return
	}

	// The mailcow first start clones its repository and pulls a large stack, so
	// its `up` runs in the background and the handler returns immediately; the
	// UI polls /service/{id}/status for live progress. `down` and `restart`
	// stay synchronous because the deployment is already on disk by then.
	if def.Kind == services.KindMailcow && op == "up" {
		s.startMailcowUp(id)
		http.Redirect(w, r, "/service/"+id+"?deploying=1", http.StatusSeeOther)
		return
	}

	// Vault's "unseal" action isn't a compose lifecycle op: it talks to the
	// running Vault's HTTP API to (re)initialize it if needed and break the
	// seal with the stored unseal keys. The container must already be up.
	if op == "unseal" {
		if def.Kind != services.KindVault {
			http.Error(w, "unknown op", 400)
			return
		}
		items, err := s.cfg.DB.ConfigItems(id)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		values, _, _ := decryptValues(s.cfg.Cipher, def, items, true)
		if err := s.ensureVaultInitialized(id, values); err != nil {
			q := url.Values{}
			q.Set("err", "1")
			q.Set("msg", err.Error())
			http.Redirect(w, r, "/service/"+id+"?"+q.Encode(), http.StatusSeeOther)
			return
		}
		sealed, err := s.vaultUnseal(id, vaultPort(values))
		if err != nil {
			q := url.Values{}
			q.Set("err", "1")
			q.Set("msg", "unseal failed: "+err.Error())
			http.Redirect(w, r, "/service/"+id+"?"+q.Encode(), http.StatusSeeOther)
			return
		}
		msg := "vault-unsealed"
		if sealed {
			msg = "vault-still-sealed"
		}
		http.Redirect(w, r, "/service/"+id+"?msg="+msg, http.StatusSeeOther)
		return
	}

	if op != "up" && op != "down" && op != "restart" {
		http.Error(w, "unknown op", 400)
		return
	}

	// A non-mailcow `up` runs through the shared upService (prepare, ephemeral
	// .env, volume ensure, `compose up -d --force-recreate`). A prepare failure
	// is "user action needed" and is surfaced as an inline prompt back on the
	// service page; every other failure is an internal error.
	if op == "up" {
		if err := s.upService(id); err != nil {
			var prepErr *servicePrepareError
			if errors.As(err, &prepErr) {
				q := url.Values{}
				q.Set("err", "1")
				q.Set("msg", prepErr.err.Error())
				http.Redirect(w, r, "/service/"+id+"?"+q.Encode(), http.StatusSeeOther)
			} else {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}
		http.Redirect(w, r, "/?msg=up", http.StatusSeeOther)
		return
	}

	// `restart` materializes launch-time files (cloudflared config, mailcow
	// conf) right before restarting the containers, and may fail with a user
	// action needed; those failures are prompted inline back on the page.
	if op == "restart" {
		items, _ := s.cfg.DB.ConfigItems(id)
		values, _, _ := decryptValues(s.cfg.Cipher, def, items, true)
		s.injectVolumeName(dir, id, def.Kind, values)
		if prep, ok := prepareFns[def.Kind]; ok {
			if err := prep(s, id, dir, values); err != nil {
				q := url.Values{}
				q.Set("err", "1")
				q.Set("msg", err.Error())
				http.Redirect(w, r, "/service/"+id+"?"+q.Encode(), http.StatusSeeOther)
				return
			}
		}
	}

	switch op {
	case "down":
		_, err = docker.Down(dir, s.cfg.Docker)
	case "restart":
		_, err = docker.Restart(dir, s.cfg.Docker)
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/?msg="+op, http.StatusSeeOther)
}

// servicePrepareError wraps a failure of the launch-time prepare step (e.g.
// cloudflared not logged in, mailcow missing hostname) so callers can surface
// it as "user action needed" instead of a bare internal error.
type servicePrepareError struct{ err error }

func (e *servicePrepareError) Error() string { return e.err.Error() }

func (e *servicePrepareError) Unwrap() error { return e.err }

// upService starts a lifecycle-controlled service, capturing the steps the
// action handler and AutoStart share: decrypt secrets, run the kind-specific
// prepare step (materializing the config the compose mounts), write the
// ephemeral .env, ensure the resizeable data volume, and `compose up -d
// --force-recreate` so saved config edits are applied. mailcow's long first
// start is delegated to the background job runner instead. Prepare failures
// are wrapped as *servicePrepareError so callers can distinguish them; every
// other failure is returned as-is.
func (s *server) upService(id string) error {
	svc, err := s.cfg.DB.ServiceByID(id)
	if err != nil {
		return err
	}
	def, ok := services.Get(services.Kind(svc.Kind))
	if !ok {
		return fmt.Errorf("unknown kind %q", svc.Kind)
	}
	if def.ReadOnly {
		return nil
	}
	if def.Kind == services.KindMailcow {
		s.startMailcowUp(id)
		return nil
	}
	dir := s.deployDir(id)
	items, _ := s.cfg.DB.ConfigItems(id)
	values, _, _ := decryptValues(s.cfg.Cipher, def, items, true)
	s.injectVolumeName(dir, id, def.Kind, values)
	if prep, ok := prepareFns[def.Kind]; ok {
		if err := prep(s, id, dir, values); err != nil {
			return &servicePrepareError{err}
		}
	}
	if _, err := render.WriteEnvFile(dir, values); err != nil {
		return err
	}
	defer render.RemoveEnvFile(dir)
	if s.cfg.DockerRaw != nil {
		if namedVolume, ok := resizeVolumeNames[def.Kind]; ok {
			name := values["MINIO_VOLUME_NAME"]
			if name == "" {
				name = docker.VolumeName(dir, namedVolume)
			}
			size := values["MINIO_VOLUME_SIZE"]
			if size == "" {
				size = "100G"
			}
			// Reject a configured size the disk can't fit before Docker tries
			// (and fails) to create the volume.
			if _, _, msg, ok := s.sizePreflight(dir, id, size); !ok {
				return fmt.Errorf("volume preflight: %s", msg)
			}
			if err := docker.EnsureVolume(s.cfg.DockerRaw, name); err != nil {
				return err
			}
		}
	}
	_, err = docker.Up(dir, s.cfg.Docker)
	return err
}

// AutoStart starts every service flagged "start on boot", in a background
// goroutine per service, so an app restart (host reboot included) restores the
// previously-running stack. Failures are logged per service so one broken
// config can't take the rest down; the error also surfaces on the service page
// the next time the operator touches it.
func (s *server) AutoStart() {
	svcs, err := s.cfg.DB.ListServices()
	if err != nil {
		log.Printf("autostart: list services: %v", err)
		return
	}
	for _, svc := range svcs {
		items, err := s.cfg.DB.ConfigItems(svc.ID)
		if err != nil {
			log.Printf("autostart %s (%s id=%s): read config: %v", svc.Name, svc.Kind, svc.ID, err)
			continue
		}
		if strings.TrimSpace(items[autostartKey]) != "true" {
			continue
		}
		svc := svc
		go func() {
			if err := s.upService(svc.ID); err != nil {
				log.Printf("autostart %s (%s id=%s): %v", svc.Name, svc.Kind, svc.ID, err)
			}
		}()
	}
}

// startMailcowUp launches a background goroutine that runs the (potentially
// long) mailcow deployment: clone the upstream stack, then `compose up`, while
// updating a per-service job so the UI can show live progress. It refuses to
// start a second deployment while one is already in flight for the service.
func (s *server) startMailcowUp(id string) {
	if s.jobs.active(id) {
		return
	}
	s.jobs.set(id, JobCloning, "Cloning mailcow…", "")
	go func() {
		dir := s.deployDir(id)
		def, _ := services.Get(services.KindMailcow)
		items, _ := s.cfg.DB.ConfigItems(id)
		values, _, _ := decryptValues(s.cfg.Cipher, def, items, true)

		if err := prepareMailcow(s, id, dir, values); err != nil {
			s.jobs.set(id, JobError, "", err.Error())
			return
		}
		s.jobs.set(id, JobPulling, "Pulling images and starting the mail server…", "")
		if _, err := docker.Up(dir, s.cfg.Docker); err != nil {
			s.jobs.set(id, JobError, "", err.Error())
			return
		}
		s.jobs.set(id, JobRunning, "Mailcow is running", "")
	}()
}

// serviceStatus returns the background deployment job for a service (JSON) so
// the UI can show live progress for long-running actions like the mailcow
// first start. When no job exists a 204 is returned and the client stops
// polling.
func (s *server) serviceStatus(w http.ResponseWriter, r *http.Request) {
	id := idFromPath(w, r)
	if id == "" {
		return
	}
	j, ok := s.jobs.get(id)
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"state":%q,"message":%q,"error":%q}`, j.state, j.msg, j.err)
}

// deleteService removes a service: it tears down any running containers,
// removes the resizeable data volume (if any) along with its data, deletes the
// deploy directory, and finally removes the DB row (config items cascade).
func (s *server) deleteService(w http.ResponseWriter, r *http.Request) {
	id := idFromPath(w, r)
	if id == "" {
		return
	}
	svc, err := s.cfg.DB.ServiceByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	dir := s.deployDir(id)

	// Tear down running containers. This is best-effort: a service that was
	// never started (no compose file / deploy dir yet) has nothing to bring
	// down, and that must not block deletion. Read-only kinds rely on an
	// externally-managed checkout and never get a compose here, so skip them.
	downErr := error(nil)
	if def, ok := services.Get(services.Kind(svc.Kind)); ok && !def.ReadOnly {
		_, downErr = docker.Down(dir, s.cfg.Docker)
	}

	// Remove the service-owned resizeable data volume (and its data). Blinding
	// deletion of a minio volume would leak the data; removing it is the change
	// the user opted into.
	if s.cfg.DockerRaw != nil {
		if namedVolume, ok := resizeVolumeNames[services.Kind(svc.Kind)]; ok {
			name := ""
			if items, err := s.cfg.DB.ConfigItems(id); err == nil {
				name = items["MINIO_VOLUME_NAME"]
			}
			if name == "" {
				name = docker.VolumeName(dir, namedVolume)
			}
			if exists, _ := docker.VolumeExists(s.cfg.DockerRaw, name); exists {
				if err := docker.RemoveVolume(s.cfg.DockerRaw, name); err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
			}
		}
	}

	// Drop the deploy directory (compose file, ephemeral creds/config). The
	// directory may hold root-owned files created by docker compose mounts, so
	// removal falls back to a disposable root container when the host-side
	// delete hits a permission error.
	if err := docker.RemoveDeployDir(dir, "", s.cfg.DockerRaw); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	// Remove this service's backup snapshot files and metadata. The rows
	// cascade away with the service row; the files live outside the DB and are
	// removed here best-effort (a leftover is harmless but leaks disk).
	if s.cfg.BackupDir != "" {
		if rows, err := s.cfg.DB.ListBackups(id); err == nil {
			for _, b := range rows {
				_ = os.Remove(filepath.Join(s.cfg.BackupDir, id, b.Filename))
			}
		}
		if err := docker.RemoveDeployDir(filepath.Join(s.cfg.BackupDir, id), "", s.cfg.DockerRaw); err != nil {
			// best-effort; dir may not exist or hold nothing
		}
	}

	if err := s.cfg.DB.DeleteService(id); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	// Surface a down failure (if any) as a flash notice rather than aborting the
	// delete: the row, volume and deploy dir are already gone, and a failed down
	// only means containers may still be running orphaned.
	if downErr != nil {
		http.Redirect(w, r, "/?msg=deleted_err", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/?msg=deleted", http.StatusSeeOther)
}

// autostartKey is the universal config item flagging a service to be started
// automatically when the app boots (see App.AutoStart). It is not a kind field:
// every lifecycle-controlled service carries it.
const autostartKey = "autostart"

// resizeVolumeNames maps a service kind to the named volume (volume name in the
// compose file) whose size may be resized. Only services that declare a
// size-limited named volume are resizable; others return an error.
var resizeVolumeNames = map[services.Kind]string{
	services.KindMinio: "minio_data",
}

// prepareFns maps kinds needing a launch-time step run right before
// `up`|`restart`: materializing files the compose mounts (cloudflared's
// config, mailcow's conf) or guarding the launch (postgres's volume-size
// preflight). id lets a guard exclude the service's own declaration from the
// cross-service free-space check.
var prepareFns = map[services.Kind]func(s *server, id, dir string, values map[string]string) error{
	services.KindCloudflared: prepareCloudflared,
	services.KindMailcow:     prepareMailcow,
	services.KindPostgres:    preparePostgres,
	services.KindVault:       prepareVault,
}

// preparePostgres enforces the volume-size guard at launch: when a
// POSTGRES_VOLUME_SIZE is configured, the launch fails fast if the disk can't
// hold it together with every other service's declared volume size, before
// compose creates the volume on first init. The size is advisory (local
// volumes cannot enforce one) — it guards initiation only.
func preparePostgres(s *server, id, dir string, values map[string]string) error {
	size := strings.TrimSpace(values["POSTGRES_VOLUME_SIZE"])
	if size == "" {
		return nil
	}
	if _, _, msg, ok := s.sizePreflight(dir, id, size); !ok {
		return errors.New(msg)
	}
	return nil
}

// prepareVault materializes the vault.hcl the compose mounts, and enforces the
// volume-size guard at launch (see preparePostgres). The config file is written
// fresh on every launch so hostname/port edits are picked up; it is a plain
// file (no .env interpolation needed).
func prepareVault(s *server, id, dir string, values map[string]string) error {
	size := strings.TrimSpace(values["VAULT_VOLUME_SIZE"])
	if size != "" {
		if _, _, msg, ok := s.sizePreflight(dir, id, size); !ok {
			return errors.New(msg)
		}
	}
	content := services.VaultConfig(values)
	return os.WriteFile(filepath.Join(dir, "vault.hcl"), []byte(content), 0o644)
}

// vaultPort resolves the host port Vault is published on, from the decryptable
// config values (defaults to 8200).
func vaultPort(values map[string]string) string {
	port := strings.TrimSpace(values["VAULT_PORT"])
	if port == "" {
		port = "8200"
	}
	return port
}

// ensureVaultInitialized asks a running Vault for its init state and, when it
// is uninitialized, initializes it (5 unseal shares, threshold 3), persisting
// the returned unseal keys and root token encrypted in the config store so the
// app (and the operator) can unseal it and so a deploy-dir recreate keeps the
// same keys. It is a no-op once initialized.
func (s *server) ensureVaultInitialized(id string, values map[string]string) error {
	port := vaultPort(values)
	base := "http://127.0.0.1:" + port + "/v1/sys/init"
	state, err := vaultRequest("GET", base, "", "")
	if err != nil {
		return fmt.Errorf("cannot reach Vault at :%s (is it up?): %w", port, err)
	}
	var initResp struct {
		Initialized bool `json:"initialized"`
	}
	if err := json.Unmarshal(state, &initResp); err != nil {
		return err
	}
	if initResp.Initialized {
		return nil
	}
	body, err := vaultRequest("PUT", base, "", `{"secret_shares":5,"secret_threshold":3}`)
	if err != nil {
		return fmt.Errorf("initializing Vault: %w", err)
	}
	var initR struct {
		Keys      []string `json:"keys"`
		RootToken string   `json:"root_token"`
	}
	if err := json.Unmarshal(body, &initR); err != nil {
		return err
	}
	if len(initR.Keys) == 0 || initR.RootToken == "" {
		return fmt.Errorf("Vault init returned no keys/root token")
	}
	secrets := map[string]string{}
	for i, key := range services.VaultSecretKeys() {
		if i < len(initR.Keys) {
			secrets[key] = initR.Keys[i]
		}
	}
	secrets[services.VaultSecretRootToken] = initR.RootToken
	persist := make([]db.ConfigItem, 0, len(services.VaultSecretKeys()))
	for _, key := range services.VaultSecretKeys() {
		enc, err := s.cfg.Cipher.Encrypt(secrets[key])
		if err != nil {
			return err
		}
		persist = append(persist, db.ConfigItem{Key: key, Value: enc})
	}
	if err := s.cfg.DB.SetConfigItems(id, persist); err != nil {
		return err
	}
	return nil
}

// loadVaultSecrets decrypts the persisted unseal keys back out of the config
// store. It returns just the keys; Vault needs a threshold of them to unseal.
func (s *server) loadVaultSecrets(id string) (keys []string, err error) {
	items, err := s.cfg.DB.ConfigItems(id)
	if err != nil {
		return nil, err
	}
	for _, key := range services.VaultSecretKeys() {
		if key == services.VaultSecretRootToken {
			continue
		}
		enc := items[key]
		if enc == "" {
			continue
		}
		plain, err := s.cfg.Cipher.Decrypt(enc)
		if err != nil {
			return nil, fmt.Errorf("stored Vault %s cannot be decrypted (%v): restore the original master key, or delete this service and its data volume", key, err)
		}
		keys = append(keys, plain)
	}
	return keys, nil
}

// vaultUnseal drives Vault's sys/unseal with each persisted key (up to the
// threshold) until the seal is broken. It returns whether Vault is still
// sealed afterwards.
func (s *server) vaultUnseal(id, port string) (sealed bool, err error) {
	keys, err := s.loadVaultSecrets(id)
	if err != nil {
		return false, err
	}
	if len(keys) == 0 {
		return false, fmt.Errorf("no unseal keys stored for this service; init it first (start the service and press Unseal again)")
	}
	for _, key := range keys {
		payload, _ := json.Marshal(map[string]string{"key": key})
		body, err := vaultRequest("PUT", "http://127.0.0.1:"+port+"/v1/sys/unseal", "", string(payload))
		if err != nil {
			return false, err
		}
		var r struct {
			Sealed bool `json:"sealed"`
		}
		if err := json.Unmarshal(body, &r); err != nil {
			return false, err
		}
		if !r.Sealed {
			return false, nil
		}
	}
	return true, nil
}

// vaultRequest performs a raw HTTP request against the Vault API. A body is
// sent on PUT; empty for GET. It returns the raw response body.
func vaultRequest(method, urlStr, token, body string) ([]byte, error) {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, urlStr, rd)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("X-Vault-Token", token)
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Vault API %s -> %d %s", urlStr, resp.StatusCode, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// prepareCloudflared resolves the tunnel for a cloudflared service at launch:
// it requires a prior login (prompting the user to run the containerized
// `cloudflared tunnel login` if the cert is missing), creates/reuses the named
// tunnel, and writes creds.json and config.yml (resolved tunnel id + the saved
// ingress rules) into the deploy dir before compose mounts them.
func prepareCloudflared(s *server, svcID, dir string, values map[string]string) error {
	cli := s.cfg.Cloudflared
	if cli == nil {
		return fmt.Errorf("cloudflared container runner is not configured")
	}
	name := strings.TrimSpace(values["CF_TUNNEL"])
	if name == "" {
		return fmt.Errorf("tunnel name is required")
	}
	// No cert.pem yet means no account login: start the login container, hand
	// the user the Cloudflare authorization URL in the web UI, and stop until
	// they have completed it.
	if !cli.LoggedIn() {
		loginURL, err := cli.LoginURL()
		if err != nil {
			return err
		}
		return fmt.Errorf("Cloudflare login required: open the authorization URL in your browser, complete the login, then press Start again. %s", loginURL)
	}
	id, creds, err := cli.EnsureTunnel(name)
	if err != nil {
		return err
	}
	// Point DNS at the tunnel so each saved ingress hostname actually routes:
	// `cloudflared tunnel route dns` creates the CNAME (idempotently). A zone
	// not present under this login fails here with a clear prompt.
	for _, host := range services.CloudflaredIngressHosts(values) {
		if err := cli.RouteDNS(name, host); err != nil {
			return fmt.Errorf("routing %s to tunnel %q failed: %w", host, name, err)
		}
	}
	// A freshly-created tunnel returns its credentials; a reused one has none,
	// so fall back to the credentials a prior launch cached in this deploy dir.
	if len(creds) == 0 {
		creds, err = os.ReadFile(filepath.Join(dir, "creds.json"))
		if err != nil {
			return fmt.Errorf("tunnel %q already exists but no credentials are cached for it; delete this service and recreate the tunnel, or recreate the credentials on the host", name)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "creds.json"), creds, 0o600); err != nil {
		return err
	}
	// The tunnel container runs cloudflared as the image's own uid, so it must
	// be able to read this file: give it ownership when running as root, else
	// make it world-readable (the same fallback EnsureHome uses).
	if err := os.Chown(filepath.Join(dir, "creds.json"), 65532, 65532); err != nil {
		_ = os.Chmod(filepath.Join(dir, "creds.json"), 0o644)
	}
	cfg, err := services.CloudflaredConfig(id, values)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "config.yml"), []byte(cfg), 0o644)
}

// prepareMailcow materializes the mailcow checkout for a service at launch. On
// first start it clones the official mailcow-dockerized repository into the
// service's deploy dir (which then hosts the whole stack); subsequent starts
// reuse the existing checkout. It reconciles the app-managed settings
// (hostname, ports, TZ, stored credentials) into mailcow.conf — rendering it
// on a fresh checkout, rewriting only its managed lines on an existing one —
// and creates the `.env` -> `mailcow.conf` symlink mailcow's compose requires.
// The official stack's own docker-compose.yml is what `up`/`down`/`restart`
// act on; port-mapping edits apply when `up` recreates the containers.
func prepareMailcow(s *server, id, dir string, values map[string]string) error {
	if strings.TrimSpace(values["MAILCOW_HOSTNAME"]) == "" {
		return fmt.Errorf("mail server hostname (FQDN) is required")
	}
	// Resolve (and, on first deploy, persist) the DB/Redis/API credentials
	// BEFORE any file is written, so a freshly re-cloned checkout keeps the
	// secrets its named Docker volumes were born with. Without this, a re-clone
	// regenerates mailcow.conf with new secrets and the persisted MySQL datadir
	// rejects them — php-fpm hangs on "Waiting for SQL..." and the entire
	// mailcow UI goes dark behind nginx 502s.
	if _, err := s.resolveMailcowSecrets(dir, values); err != nil {
		return err
	}
	// The fully-active stack takes a long time on first start; give the clone
	// and the eventual `up` generous time to complete.
	if _, err := os.Stat(filepath.Join(dir, "docker-compose.yml")); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			return err
		}
		// git clone requires an empty (or absent) target dir. Save never
		// materializes files for mailcow, so dir is typically absent; guard
		// against a stale empty dir by cloning into a temp sibling and renaming.
		src := dir + "-src"
		_ = os.RemoveAll(src)
		cmd := exec.Command("git", "clone", "--depth", "1", services.MailcowRepo, src)
		if out, err := cmd.CombinedOutput(); err != nil {
			_ = os.RemoveAll(src)
			return fmt.Errorf("cloning mailcow failed (is git installed and the host online?): %w: %s", err, strings.TrimSpace(string(out)))
		}
		if err := os.Rename(src, dir); err != nil {
			_ = os.RemoveAll(src)
			return err
		}
	} else if err != nil {
		return err
	}

	// Reconcile the app-managed settings into mailcow.conf. A fresh checkout
	// has no conf, so MailcowConf renders one; an existing conf gets only its
	// managed lines rewritten (operator tweaks survive), so saved edits to the
	// hostname/ports/TZ propagate on the next launch — the stack's port
	// mappings, which compose reads from this file, are what the nginx binds
	// when `up` recreates the containers. Credentials come from the store, so
	// reconciliation never rotates the secrets the volumes were initialized
	// with; files whose managed lines already match are left untouched.
	confPath := filepath.Join(dir, "mailcow.conf")
	content, err := os.ReadFile(confPath)
	existed := err == nil
	if os.IsNotExist(err) {
		content = []byte(services.MailcowConf(values))
	} else if err != nil {
		return err
	}
	updated := services.ReconcileMailcowConf(string(content), values)
	if !existed || updated != string(content) {
		if err := os.WriteFile(confPath, []byte(updated), 0o600); err != nil {
			return err
		}
	}

	// mailcow's compose reads its environment via a `.env` symlink to
	// mailcow.conf. Recreate the symlink idempotently.
	envLink := filepath.Join(dir, ".env")
	if fi, err := os.Lstat(envLink); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		// leave an existing valid symlink alone
	} else {
		if err := os.RemoveAll(envLink); err != nil {
			return err
		}
		if err := os.Symlink("mailcow.conf", envLink); err != nil {
			return err
		}
	}

	// Apply environment-specific hardening idempotently (safe on fresh clones
	// and on re-deploys alike): point mailcow's unbound DNS at working upstream
	// forwarders instead of IPv6 root recursion, and seed the bundled TLS cert
	// so nginx can boot on a cold start (e.g. when SKIP_LETS_ENCRYPT=y).
	if err := hardenMailcow(dir); err != nil {
		return err
	}
	return nil
}

// resolveMailcowSecrets loads the service's persisted mailcow credentials
// (MySQL root/user, Redis, API key) from their encrypted config store,
// generating and persisting a complete set on first deploy, and injects them
// into values under the internal keys so MailcowConf reuses them. Doing this
// against SQLite (not the checkout's mailcow.conf, which a re-clone destroys)
// is what keeps the credentials aligned with the named Docker volumes the
// stack mounts. When the store is incomplete but the checkout already carries a
// mailcow.conf (a deploy created before credential persistence existed), the
// credentials it contains are adopted rather than regenerated, since the
// volumes were initialized with those. Corrupt/undecryptable stored
// credentials are a hard error: the volumes are bound to whatever password they
// were initialized with, so silently minting a fresh set would reproduce the
// same drift this guards against.
func (s *server) resolveMailcowSecrets(dir string, values map[string]string) (string, error) {
	// Deploy dirs are named after the service id, so the dir carries the id.
	id := filepath.Base(dir)
	if id == "" || id == "." || id == string(filepath.Separator) {
		return "", fmt.Errorf("mailcow deploy dir %q does not carry a service id", dir)
	}
	items, err := s.cfg.DB.ConfigItems(id)
	if err != nil {
		return id, err
	}
	existing := map[string]string{}
	count := 0
	for _, key := range services.MailcowSecretKeys() {
		enc := items[key]
		if enc == "" {
			continue
		}
		plain, err := s.cfg.Cipher.Decrypt(enc)
		if err != nil {
			return id, fmt.Errorf("stored mailcow %s cannot be decrypted (%v): the service's MySQL/Redis/FPM volumes are bound to the credentials persisted on first deploy; restore the original master key, or delete this service and its mailcow volumes", key, err)
		}
		existing[key] = plain
		count++
	}
	// Complete set present: reuse it. Otherwise adopt what the checkout's
	// mailcow.conf already carries — a deploy created before credential
	// persistence existed has a conf whose credentials the running volumes were
	// initialized with, so minting fresh ones would reproduce exactly the drift
	// this guard exists for — mint what's still missing, and persist the whole
	// set so every later launch (a re-clone included) sees it.
	if count != len(services.MailcowSecretKeys()) {
		if conf, err := os.ReadFile(filepath.Join(dir, "mailcow.conf")); err == nil {
			for key, confKey := range map[string]string{
				services.MailcowSecretDBPass:    "DBPASS",
				services.MailcowSecretDBRoot:    "DBROOT",
				services.MailcowSecretRedisPass: "REDISPASS",
				services.MailcowSecretAPIKey:    "API_KEY",
			} {
				if existing[key] != "" {
					continue
				}
				if v := services.MailcowConfValue(string(conf), confKey); v != "" {
					existing[key] = v
				}
			}
		}
		sec := services.MailcowSecrets(existing)
		persist := make([]db.ConfigItem, 0, len(services.MailcowSecretKeys()))
		for _, key := range services.MailcowSecretKeys() {
			enc, err := s.cfg.Cipher.Encrypt(sec[key])
			if err != nil {
				return id, err
			}
			persist = append(persist, db.ConfigItem{Key: key, Value: enc})
		}
		if err := s.cfg.DB.SetConfigItems(id, persist); err != nil {
			return id, err
		}
		for k, v := range sec {
			values[k] = v
		}
		return id, nil
	}
	for k, v := range existing {
		values[k] = v
	}
	return id, nil
}

// hardenMailcow applies idempotent, environment-safe tweaks to an existing
// mailcow checkout so the stack reliably boots in constrained environments
// (broken IPv6, no public cert yet). It only adds what is missing and never
// overwrites a config the operator has already customized, so it is a no-op on
// re-deploys and on checkouts that don't carry the referenced files.
func hardenMailcow(dir string) error {
	if err := hardenUnboundConf(dir); err != nil {
		return err
	}
	return seedMailcowSelfSignedCert(dir)
}

// hardenUnboundConf makes mailcow's unbound resolve through working IPv4
// forwarders rather than full root-server recursion over IPv6, which can fail
// (and fail the whole stack's healthcheck) in sandboxed/virtualized hosts. It
// edits data/conf/unbound/unbound.conf in place, adding nothing if the file is
// absent or already hardened.
func hardenUnboundConf(dir string) error {
	p := filepath.Join(dir, "data", "conf", "unbound", "unbound.conf")
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	conf := string(b)
	if strings.Contains(conf, "forward-zone:") && strings.Contains(conf, "do-ip6: no") {
		return nil // already hardened
	}
	if strings.Contains(conf, "do-ip6: yes") {
		conf = strings.Replace(conf, "do-ip6: yes", "do-ip6: no", 1)
	}
	if !strings.Contains(conf, "forward-zone:") {
		conf += "\nforward-zone:\n  name: \".\"\n  forward-addr: 1.1.1.1\n  forward-addr: 8.8.8.8\n"
	}
	return os.WriteFile(p, []byte(conf), 0o644)
}

// seedMailcowSelfSignedCert copies mailcow's bundled example TLS certificate
// (cert.pem/key.pem/dhparams.pem) from data/assets/ssl-example into
// data/assets/ssl when none is present. mailcow's compose mounts the ssl dir
// read-only at /etc/ssl/mail and nginx refuses to start without a cert.mailcow
// normally seeds this on boot, but skips it when SKIP_LETS_ENCRYPT=y; doing it
// at launch (before docker mounts the dir) also keeps the directory owned by
// the app user so reruns are idempotent. Existing certificates are left alone.
func seedMailcowSelfSignedCert(dir string) error {
	ssl := filepath.Join(dir, "data", "assets", "ssl")
	if _, err := os.Stat(filepath.Join(ssl, "cert.pem")); err == nil {
		return nil // a certificate is already present
	} else if !os.IsNotExist(err) {
		return err
	}
	example := filepath.Join(dir, "data", "assets", "ssl-example")
	for _, name := range []string{"cert.pem", "key.pem", "dhparams.pem"} {
		if _, err := os.Stat(filepath.Join(example, name)); os.IsNotExist(err) {
			continue // example file not shipped; nothing to seed
		} else if err != nil {
			return err
		}
		if err := os.MkdirAll(ssl, 0o755); err != nil {
			return err
		}
		data, err := os.ReadFile(filepath.Join(example, name))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(ssl, name), data, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// injectVolumeName adds the MINIO_VOLUME_NAME the compose should mount to
// values, using the stored name if present (after a resize) or the
// compose-default project-scoped name for the service's resizeable volume.
// Called wherever the compose is rendered so the external volume always points
// at a real volume. Non-resizeable services are left untouched.
func (s *server) injectVolumeName(dir string, id string, kind services.Kind, values map[string]string) {
	namedVolume, ok := resizeVolumeNames[kind]
	if !ok {
		return
	}
	if values["MINIO_VOLUME_NAME"] != "" {
		return
	}
	current := ""
	if items, err := s.cfg.DB.ConfigItems(id); err == nil {
		current = items["MINIO_VOLUME_NAME"]
	}
	if current == "" {
		current = docker.VolumeName(dir, namedVolume)
	}
	values["MINIO_VOLUME_NAME"] = current
}

// sizePreflight validates a requested volume size string against the free space
// on dir's filesystem, counting every OTHER service's declared volume size: all
// sized services (minio, postgres, …) claim the same disk, so one service's
// guard must fail when the others have already declared sizes it can't host.
// It returns the resized byte count and ok=true when everything fits, or a
// non-200 http status plus a human-readable message otherwise (400 for an
// unparseable size, 507 when the disk lacks room).
func (s *server) sizePreflight(dir, id, newSize string) (int64, int, string, bool) {
	newBytes, err := docker.ParseSize(newSize)
	if err != nil {
		return 0, http.StatusBadRequest, "invalid volume size: " + err.Error(), false
	}
	free, err := docker.BytesAvailable(dir)
	if err != nil {
		return 0, http.StatusInternalServerError, err.Error(), false
	}
	demand := newBytes + s.declaredVolumeSizes(id)
	buffer := int64(1) << 30 // 1 GiB headroom
	if demand/20 > buffer {
		buffer = demand / 20 // 5% of the total demand
	}
	if free < demand+buffer {
		return 0, http.StatusInsufficientStorage,
			"not enough free disk space: need " + prettyBytes(demand+buffer) + ", have " + prettyBytes(free), false
	}
	return newBytes, 0, "", true
}

// declaredVolumeSizes sums the configured FieldSize value of every service
// except id (whose size is being replaced by the value sizePreflight checks).
// Size keys are derived from the service registry, so new sizes services plug
// into the guard automatically.
func (s *server) declaredVolumeSizes(excludeID string) (sum int64) {
	sizeKey := map[services.Kind]string{}
	for _, def := range services.All() {
		for _, f := range def.Fields {
			if f.Type == services.FieldSize {
				sizeKey[def.Kind] = f.Key
				break
			}
		}
	}
	svcs, err := s.cfg.DB.ListServices()
	if err != nil {
		return 0
	}
	for _, svc := range svcs {
		if svc.ID == excludeID {
			continue
		}
		key, ok := sizeKey[services.Kind(svc.Kind)]
		if !ok {
			continue
		}
		items, err := s.cfg.DB.ConfigItems(svc.ID)
		if err != nil {
			continue
		}
		if b, err := docker.ParseSize(items[key]); err == nil {
			sum += b
		}
	}
	return sum
}

func (s *server) doResize(w http.ResponseWriter, r *http.Request, id string, def services.Definition, dir, namedVolume string, items []db.ConfigItem, values map[string]string) {
	if s.cfg.DockerRaw == nil {
		http.Error(w, "volume resize not supported by this Docker runner", 500)
		return
	}
	newSize := values["MINIO_VOLUME_SIZE"]
	// Pre-flight free-space check: the resize must fit the full new volume plus
	// a buffer, so it fails before stopping the service or touching any data.
	if _, status, msg, ok := s.sizePreflight(dir, id, newSize); !ok {
		http.Error(w, msg, status)
		return
	}

	// Current volume name: tracked via MINIO_VOLUME_NAME after a prior resize,
	// otherwise the compose-default project-scoped name.
	cfg, _ := s.cfg.DB.ConfigItems(id)
	currentName := cfg["MINIO_VOLUME_NAME"]
	if currentName == "" {
		currentName = docker.VolumeName(dir, namedVolume)
	}
	newName := currentName + "_new"

	backupDir, err := os.MkdirTemp(dir, "resize-backup-")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	// 1. Stop the service but keep the old volume.
	if _, err := s.cfg.Docker.Run(dir, "down"); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	// Bring the service back up if a later step fails, so a failed resize never
	// leaves the service permanently down.
	up := func() error { _, e := s.cfg.Docker.Run(dir, "up", "-d"); return e }
	defer func() {
		_ = up()
		_ = os.RemoveAll(backupDir)
	}()

	exists, _ := docker.VolumeExists(s.cfg.DockerRaw, currentName)
	if exists {
		// 2. Back up the current volume contents (old volume still intact).
		if err := docker.BackupVolume(s.cfg.DockerRaw, currentName, backupDir, docker.VolumeImage); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
	}
	// 3. Create the new volume at the new size while the old one still exists.
	if err := docker.CreateVolume(s.cfg.DockerRaw, newName); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if exists {
		// 4. Restore the backup into the fresh volume.
		if err := docker.RestoreVolume(s.cfg.DockerRaw, newName, backupDir, docker.VolumeImage); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		// 5. Only now is the old volume redundant; remove it. Data is fully
		//    preserved in newName either way.
		if err := docker.RemoveVolume(s.cfg.DockerRaw, currentName); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
	}

	// Persist config (form fields incl. the new size + the switched volume
	// name), then re-render compose to reference the new external volume.
	cfgItems := append([]db.ConfigItem{}, items...)
	cfgItems = append(cfgItems,
		db.ConfigItem{Key: "MINIO_VOLUME_NAME", Value: newName},
		db.ConfigItem{Key: "MINIO_VOLUME_SIZE", Value: newSize},
	)
	if err := s.cfg.DB.SetConfigItems(id, cfgItems); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	values["MINIO_VOLUME_NAME"] = newName
	res, err := render.BuildRenderResult(def.Kind, values)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if err := render.WriteCompose(dir, res); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	// 6. Start the service mounting the new (populated) volume.
	if _, err := render.WriteEnvFile(dir, values); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer render.RemoveEnvFile(dir)
	if err := up(); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/service/"+id+"?msg=resized", http.StatusSeeOther)
}

// prettyBytes renders a byte count as a human-readable size for error messages.
func prettyBytes(n int64) string {
	switch {
	case n >= 1<<40:
		return fmt.Sprintf("%.1f TiB", float64(n)/(1<<40))
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func (s *server) deployDir(id string) string {
	return s.cfg.DeployDir + "/" + id
}

func (s *server) statusDir(svc db.Service) string {
	return s.deployDir(svc.ID)
}

// decryptValues returns (values, secretSet, lists). values holds non-secret
// inputs keyed by field key (plus invertible FieldList cells); secretSet marks
// secret fields that already have a stored value. Secrets are only included in
// the returned values when includeSecrets is true (the launch-time path). For
// display, secrets are excluded from the input values so stored secrets are
// never echoed back into the page. FieldList rows are returned separately in
// lists (ordered rows keyed by column suffix), with a single blank row when no
// rules are stored so the editor shows an empty row to fill in.
func decryptValues(c *crypto.Cipher, def services.Definition, items map[string]string, includeSecrets bool) (map[string]string, map[string]bool, map[string][]map[string]string) {
	out := map[string]string{}
	secrets := map[string]bool{}
	lists := map[string][]map[string]string{}
	for _, f := range def.Fields {
		switch f.Type {
		case services.FieldSecret:
			if raw := items[f.Key]; raw != "" {
				secrets[f.Key] = true
				if includeSecrets {
					if dec, err := c.Decrypt(raw); err == nil {
						out[f.Key] = dec
					}
				}
			}
		case services.FieldList:
			rows := services.ListRows(items, f.Key, f.Columns)
			if len(rows) == 0 {
				rows = append(rows, map[string]string{})
			}
			lists[f.Key] = rows
			for i, row := range rows {
				for _, col := range f.Columns {
					out[services.ListItemKey(f.Key, i, col.Suffix)] = row[col.Suffix]
				}
			}
		default:
			out[f.Key] = items[f.Key]
		}
	}
	return out, secrets, lists
}

func idFromPath(w http.ResponseWriter, r *http.Request) string {
	id := r.PathValue("id")
	if _, err := uuid.Parse(id); err != nil {
		http.NotFound(w, r)
		return ""
	}
	return id
}
