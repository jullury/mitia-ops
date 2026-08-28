package render

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jullury/mitia-ops/internal/services"
)

func DotEnv(values map[string]string) string {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, values[k])
	}
	return b.String()
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
	if res.DotEnv == "" {
		res.DotEnv = DotEnv(values)
	}
	return res, nil
}

// WriteCompose writes only the non-secret docker-compose.yml.
// It deliberately never writes a .env (secrets must not persist on disk).
func WriteCompose(dir string, res services.RenderResult) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if strings.TrimSpace(res.ComposeYAML) != "" {
		return os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(res.ComposeYAML), 0o644)
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