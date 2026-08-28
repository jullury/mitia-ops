package web

import (
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/jullury/mitia-ops/internal/crypto"
	"github.com/jullury/mitia-ops/internal/db"
	"github.com/jullury/mitia-ops/internal/docker"
	"github.com/jullury/mitia-ops/internal/render"
	"github.com/jullury/mitia-ops/internal/services"
)

//go:embed templates/*.html
var tmplFS embed.FS

type Config struct {
	DB         *db.DB
	Cipher     *crypto.Cipher
	DeployDir  string
	MailcowDir string // shared wrapper-owned mailcow checkout; used only for read-only status probe
	Docker     docker.Runner
	DockerRaw  docker.RawRunner
}

func New(cfg Config) *http.ServeMux {
	// base.html provides the shared layout via `{{ template "content" . }}`,
	// and each page defines that content block. Because every page uses the
	// same "content" name, parse base together with exactly one page per set
	// so the block resolves without cross-page collision.
	funcs := template.FuncMap{
		"statusClass": statusClass,
		"statusTitle": statusTitle,
		"sizeNum":     sizeNum,
		"sizeSuffix":  sizeSuffix,
	}
	dashTmpl := template.Must(template.New("base.html").Funcs(funcs).ParseFS(tmplFS, "templates/base.html", "templates/dashboard.html"))
	svcTmpl := template.Must(template.New("base.html").Funcs(funcs).ParseFS(tmplFS, "templates/base.html", "templates/service.html"))
	s := &server{cfg: cfg, dashTmpl: dashTmpl, svcTmpl: svcTmpl}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.dashboard)
	mux.HandleFunc("POST /service/new", s.newService)
	mux.HandleFunc("GET /service/{id}", s.showService)
	mux.HandleFunc("POST /service/{id}", s.saveService)
	mux.HandleFunc("POST /service/{id}/action", s.serviceAction)
	return mux
}

type server struct {
	cfg      Config
	dashTmpl *template.Template
	svcTmpl  *template.Template
}

type dashData struct {
	Kinds    []services.Definition
	Services []dashRow
	Msg      string
}

// sizeNum returns the numeric part of a canonical size value ("100G" -> "100").
func sizeNum(v string) string { n, _ := services.SplitSize(v); return n }

// sizeSuffix returns the unit-suffix part of a canonical size value
// ("100G" -> "G").
func sizeSuffix(v string) string { _, s := services.SplitSize(v); return s }

var actionMsg = map[string]string{
	"up":      "Service started",
	"down":    "Service stopped",
	"restart": "Service restarted",
}

var serviceMsg = map[string]string{
	"saved":   "Settings saved",
	"resized": "Volume resized — data preserved",
}

type dashRow struct {
	db.Service
	Status    string
	ReadOnly  bool
	ConfigURL string
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
			if ro && def.ConfigURL != nil {
				items, _ := s.cfg.DB.ConfigItems(svc.ID)
				url = def.ConfigURL(items)
			}
		}
		rows = append(rows, dashRow{
			Service:   svc,
			Status:    status,
			ReadOnly:  ro,
			ConfigURL: url,
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
	http.Redirect(w, r, "/service/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

func (s *server) showService(w http.ResponseWriter, r *http.Request) {
	id := idFromPath(w, r)
	if id == 0 {
		return
	}
	msg := r.URL.Query().Get("msg")
	if text, ok := serviceMsg[msg]; ok {
		msg = text
	}
	s.renderService(w, r, id, msg, false)
}

// renderService re-renders a service's detail form. It is shared by the GET
// handler and the save path (which falls back to it on a validation or I/O
// error so the user sees the error inline rather than a bare error page).
// The saved values are re-read from the DB; on an error the prior (persisted)
// values are shown, since nothing was written.
func (s *server) renderService(w http.ResponseWriter, r *http.Request, id int64, msg string, msgErr bool) {
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
	values, secrets := decryptValues(s.cfg.Cipher, def, items, false)
	url := ""
	if def.ConfigURL != nil {
		url = def.ConfigURL(values)
	}
	s.svcTmpl.ExecuteTemplate(w, "base.html", svcData{
		Def:       def,
		Service:   svc,
		Values:    values,
		SecretSet: secrets,
		ConfigURL: url,
		Msg:       msg,
		MsgError:  msgErr,
		HasSize:   hasSizeField(def),
	})
}

type svcData struct {
	Def       services.Definition
	Service   *db.Service
	Values    map[string]string // non-secret values for inputs (decrypted secrets excluded)
	SecretSet map[string]bool   // field key -> true if a secret value is stored
	ConfigURL string
	Msg       string // flash message
	MsgError  bool   // true to render Msg as a danger/error notice
	HasSize   bool   // true if the service has a FieldSize (resizeable volume)
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
	if id == 0 {
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
		default:
			values[f.Key] = raw
			items = append(items, db.ConfigItem{Key: f.Key, Value: values[f.Key]})
		}
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
			if _, _, msg, ok := s.sizePreflight(dir, newSize); !ok {
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
	http.Redirect(w, r, "/service/"+strconv.FormatInt(id, 10)+"?msg=saved", http.StatusSeeOther)
}

func (s *server) serviceAction(w http.ResponseWriter, r *http.Request) {
	id := idFromPath(w, r)
	if id == 0 {
		return
	}
	op := r.URL.Query().Get("op")
	if op != "up" && op != "down" && op != "restart" {
		http.Error(w, "unknown op", 400)
		return
	}
	dir := s.deployDir(id)

	// Read-only kinds (e.g. mailcow) have no lifecycle control: reject any
	// up/down/restart before touching docker or any temporary .env.
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

	// For `up`, decrypt secrets from SQLite into a temporary .env for the
	// duration of the composer command, then delete it (ephemeral .env).
	if op == "up" {
		items, _ := s.cfg.DB.ConfigItems(id)
		values, _ := decryptValues(s.cfg.Cipher, def, items, true)
		s.injectVolumeName(dir, id, def.Kind, values)
		if _, err := render.WriteEnvFile(dir, values); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		// Remove the temp .env no matter how the launch turns out.
		defer render.RemoveEnvFile(dir)

		// The resizeable data volume is external: make sure it exists (at its
		// configured size) before compose mounts it.
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
				// Reject a configured size the disk can't fit before Docker
				// tries (and fails) to create the volume.
				if _, status, msg, ok := s.sizePreflight(dir, size); !ok {
					http.Error(w, msg, status)
					return
				}
				if err := docker.EnsureVolume(s.cfg.DockerRaw, name, size); err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
			}
		}
	}

	switch op {
	case "up":
		_, err = docker.Up(dir, s.cfg.Docker)
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

// resizeVolumeNames maps a service kind to the named volume (volume name in the
// compose file) whose size may be resized. Only services that declare a
// size-limited named volume are resizable; others return an error.
var resizeVolumeNames = map[services.Kind]string{
	services.KindMinio: "minio_data",
}

// injectVolumeName adds the MINIO_VOLUME_NAME the compose should mount to
// values, using the stored name if present (after a resize) or the
// compose-default project-scoped name for the service's resizeable volume.
// Called wherever the compose is rendered so the external volume always points
// at a real volume. Non-resizeable services are left untouched.
func (s *server) injectVolumeName(dir string, id int64, kind services.Kind, values map[string]string) {
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
// on dir's filesystem. It returns the resized byte count and ok=true when it
// fits, or a non-200 http status plus a human-readable message otherwise (400
// for an unparseable size, 507 when the disk lacks room).
func (s *server) sizePreflight(dir, newSize string) (int64, int, string, bool) {
	newBytes, err := docker.ParseSize(newSize)
	if err != nil {
		return 0, http.StatusBadRequest, "invalid volume size: " + err.Error(), false
	}
	free, err := docker.BytesAvailable(dir)
	if err != nil {
		return 0, http.StatusInternalServerError, err.Error(), false
	}
	buffer := int64(1) << 30 // 1 GiB headroom
	if newBytes/20 > buffer {
		buffer = newBytes / 20 // 5% of the new size
	}
	if free < newBytes+buffer {
		return 0, http.StatusInsufficientStorage,
			"not enough free disk space: need " + prettyBytes(newBytes+buffer) + ", have " + prettyBytes(free), false
	}
	return newBytes, 0, "", true
}

func (s *server) doResize(w http.ResponseWriter, r *http.Request, id int64, def services.Definition, dir, namedVolume string, items []db.ConfigItem, values map[string]string) {
	if s.cfg.DockerRaw == nil {
		http.Error(w, "volume resize not supported by this Docker runner", 500)
		return
	}
	newSize := values["MINIO_VOLUME_SIZE"]
	// Pre-flight free-space check: the resize must fit the full new volume plus
	// a buffer, so it fails before stopping the service or touching any data.
	if _, status, msg, ok := s.sizePreflight(dir, newSize); !ok {
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
	if err := docker.CreateVolume(s.cfg.DockerRaw, newName, newSize); err != nil {
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
	http.Redirect(w, r, "/service/"+itoa(id)+"?msg=resized", http.StatusSeeOther)
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

func (s *server) deployDir(id int64) string {
	return s.cfg.DeployDir + "/" + strconv.FormatInt(id, 10)
}

func (s *server) statusDir(svc db.Service) string {
	if def, ok := services.Get(services.Kind(svc.Kind)); ok && def.ReadOnly {
		return s.cfg.MailcowDir
	}
	return s.deployDir(svc.ID)
}

// decryptValues returns a values map and, when includeSecrets is false, a
// SecretSet map marking which secret fields already have a stored value.
// Secrets are only included in the returned values when includeSecrets is
// true (the launch-time path). For display, secrets are excluded from the
// input values so stored secrets are never echoed back into the page.
func decryptValues(c *crypto.Cipher, def services.Definition, items map[string]string, includeSecrets bool) (map[string]string, map[string]bool) {
	out := map[string]string{}
	secrets := map[string]bool{}
	for _, f := range def.Fields {
		raw := items[f.Key]
		if f.Type == services.FieldSecret {
			if raw != "" {
				secrets[f.Key] = true
				if includeSecrets {
					if dec, err := c.Decrypt(raw); err == nil {
						out[f.Key] = dec
					}
				}
			}
		} else {
			out[f.Key] = raw
		}
	}
	return out, secrets
}

func idFromPath(w http.ResponseWriter, r *http.Request) int64 {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return 0
	}
	return id
}

func itoa(id int64) string { return strconv.FormatInt(id, 10) }
