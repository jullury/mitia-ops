package render

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/jullury/mitia-ops/internal/services"
)

// safeValueRe matches values Docker Compose's dotenv parser reads verbatim
// without quoting: letters, digits, and common URL/path punctuation.
var safeValueRe = regexp.MustCompile(`^[A-Za-z0-9_./@:\-]+$`)

func DotEnv(values map[string]string) string {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		if line, ok := dotEnvLine(k, values[k]); ok {
			b.WriteString(line)
		}
	}
	return b.String()
}

// dotEnvLine renders one KEY=VALUE line. Values containing a newline or a
// carriage return cannot be represented in a dotenv file, so they are refused
// (ok == false) rather than emitted malformed. Anything not matching
// safeValueRe is wrapped in double quotes with \, ", ` and $ escaped.
func dotEnvLine(k, v string) (string, bool) {
	if strings.ContainsAny(v, "\r\n") {
		return "", false
	}
	if safeValueRe.MatchString(v) {
		return k + "=" + v + "\n", true
	}
	var q strings.Builder
	q.WriteString(k + `="`)
	for _, ch := range v {
		switch ch {
		case '\\', '"', '`', '$':
			q.WriteByte('\\')
		}
		q.WriteRune(ch)
	}
	q.WriteString("\"\n")
	return q.String(), true
}

func BuildRenderResult(k services.Kind, values map[string]string) (services.RenderResult, error) {
	def, ok := services.Get(k)
	if !ok {
		return services.RenderResult{}, fmt.Errorf("unknown service kind %q", k)
	}
	res, err := def.Render(values)
	if err != nil {
		return services.RenderResult{}, err
	}
	if res.DotEnv == "" && strings.TrimSpace(res.ComposeYAML) != "" {
		res.DotEnv = DotEnv(values)
	}
	return res, nil
}

// WriteCompose writes only the non-secret docker-compose.yml. It deliberately
// never writes a .env (secrets must not persist on disk). Kinds that need other
// files at launch (e.g. cloudflared's config.yml) materialize them via the
// web layer's launch-time prepare step.
func WriteCompose(dir string, res services.RenderResult) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if strings.TrimSpace(res.ComposeYAML) != "" {
		if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(res.ComposeYAML), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// WriteEnvFile writes a temporary .env for launch and returns its path.
// The caller MUST call RemoveEnvFile(dir) immediately after the docker command.
func WriteEnvFile(dir string, values map[string]string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte(DotEnv(values)), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// RemoveEnvFile deletes dir/.env, ignoring any "not exist" error (idempotent).
func RemoveEnvFile(dir string) error {
	err := os.Remove(filepath.Join(dir, ".env"))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
