package web

import (
	"embed"
	"html/template"
	"net/http"
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
	DB        *db.DB
	Cipher    *crypto.Cipher
	DeployDir string
	Docker    docker.Runner
}

func New(cfg Config) *http.ServeMux {
	// base.html provides the shared layout via `{{ template "content" . }}`,
	// and each page defines that content block. Because every page uses the
	// same "content" name, parse base together with exactly one page per set
	// so the block resolves without cross-page collision.
	dashTmpl := template.Must(template.New("base.html").ParseFS(tmplFS, "templates/base.html", "templates/dashboard.html"))
	svcTmpl := template.Must(template.New("base.html").ParseFS(tmplFS, "templates/base.html", "templates/service.html"))
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
}

type dashRow struct {
	db.Service
	Status string
}

func (s *server) dashboard(w http.ResponseWriter, r *http.Request) {
	svcs, err := s.cfg.DB.ListServices()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	rows := make([]dashRow, 0, len(svcs))
	for _, svc := range svcs {
		status, _ := docker.Status(s.deployDir(svc.ID), s.cfg.Docker)
		rows = append(rows, dashRow{Service: svc, Status: firstLine(status)})
	}
	s.dashTmpl.ExecuteTemplate(w, "base.html", dashData{Kinds: services.All(), Services: rows})
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
	values := decryptValues(s.cfg.Cipher, def, items)
	s.svcTmpl.ExecuteTemplate(w, "base.html", struct {
		Def     services.Definition
		Service *db.Service
		Values  map[string]string
	}{def, svc, values})
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
		if f.Type == services.FieldSecret {
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
		} else {
			if f.Type == services.FieldBool {
				if r.FormValue(f.Key) == "" {
					values[f.Key] = "false"
				} else {
					values[f.Key] = "true"
				}
			} else {
				values[f.Key] = raw
			}
			items = append(items, db.ConfigItem{Key: f.Key, Value: values[f.Key]})
		}
	}
	if err := s.cfg.DB.SetConfigItems(id, items); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	res, err := render.BuildRenderResult(def.Kind, values)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	// WriteCompose persists only the non-secret docker-compose.yml.
	// Secrets are never written to a .env here; they stay in SQLite (encrypted)
	// and are decrypted into a temporary .env only at launch time.
	if err := render.WriteCompose(s.deployDir(id), res); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/service/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
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

	// For `up`, decrypt secrets from SQLite into a temporary .env for the
	// duration of the composer command, then delete it (ephemeral .env).
	if op == "up" {
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
		values := decryptValues(s.cfg.Cipher, def, items)
		if _, err := render.WriteEnvFile(dir, values); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		// Remove the temp .env no matter how the launch turns out.
		defer render.RemoveEnvFile(dir)
	}

	var err error
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
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *server) deployDir(id int64) string {
	return s.cfg.DeployDir + "/" + strconv.FormatInt(id, 10)
}

func decryptValues(c *crypto.Cipher, def services.Definition, items map[string]string) map[string]string {
	out := map[string]string{}
	for _, f := range def.Fields {
		raw := items[f.Key]
		if f.Type == services.FieldSecret {
			if raw != "" {
				if dec, err := c.Decrypt(raw); err == nil {
					out[f.Key] = dec
				}
			}
		} else {
			out[f.Key] = raw
		}
	}
	return out
}

func idFromPath(w http.ResponseWriter, r *http.Request) int64 {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return 0
	}
	return id
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func itoa(id int64) string { return strconv.FormatInt(id, 10) }
