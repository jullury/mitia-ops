// Package cloudflared drives the cloudflared CLI through a Docker container
// (cloudflare/cloudflared) so the host needs no cloudflared install: check that
// a login (cert.pem) exists and create (or reuse) a named tunnel, capturing the
// tunnel's credentials from the command's JSON output.
package cloudflared

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jullury/mitia-ops/internal/docker"
)

// containerHome is where cloudflared looks for its state inside the official
// image: the image sets its WorkingDir to /home/nonroot and its passwd entry
// maps the non-root user (uid 65532) there, so per-user state is ~/.cloudflared.
const containerHome = "/home/nonroot/.cloudflared"

// containerUID is the image's fixed non-root user. The container runs as it (no
// --user override) so cloudflared resolves its home from the image's passwd
// entry instead of a host uid that has no entry (which leaves `~` unresolved).
const containerUID = 65532

// loginPort is the port cloudflared's `tunnel login` callback binds to.
// loginContainerName names the detached container that runs the interactive
// login so it can be found, restarted, and cleaned up by name.
const (
	loginPort          = 4979
	loginContainerName = "mitiaops-cloudflared-login"
)

var (
	createdRe = regexp.MustCompile(`with id\s+([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})`)
	infoIDRe  = regexp.MustCompile(`(?m)^\s*ID:\s*([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})`)
	uuidRe    = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	// loginURLRe matches the single-line Cloudflare authorization URL cloudflared
	// prints after "Please open the following URL...".
	loginURLRe = regexp.MustCompile(`(?m)^(https://\S+)$`)
)

// CLI runs cloudflared inside a container. Home is the app-managed directory
// holding the login artifact (cert.pem) and the per-tunnel credentials; the
// container mounts it read-write under containerHome. Raw provides `docker
// run`; Run, when set, is called with only the cloudflared subcommand args
// (tests stub it without docker).
type CLI struct {
	Image string
	Home  string
	Raw   docker.RawRunner
	Run   func(args ...string) (string, error)
	// PollDelay is the interval between `docker logs` polls while waiting for
	// the login URL to appear (tests lower it).
	PollDelay time.Duration
}

func New() *CLI { return &CLI{} }

// EnsureHome creates Home and makes it writable by the container's uid
// (containerUID) so cloudflared can persist cert.pem and credentials in it.
// When the app cannot chown (a non-root host user), it falls back to a
// world-writable mode; the files inside stay owned by the container's uid and
// are only readable by it, so the app never reads them directly.
func (c *CLI) EnsureHome() error {
	if err := os.MkdirAll(c.Home, 0o700); err != nil {
		return err
	}
	if err := os.Chown(c.Home, containerUID, containerUID); err == nil {
		return os.Chmod(c.Home, 0o700)
	}
	return os.Chmod(c.Home, 0o777)
}

func (c *CLI) image() string {
	if c.Image != "" {
		return c.Image
	}
	return "cloudflare/cloudflared:latest"
}

func (c *CLI) home() string {
	if c.Home == "" {
		c.Home = ".cloudflared"
	}
	return c.Home
}

// dockerCmd builds the `docker run` invocation for a cloudflared subcommand,
// mounting the app-managed home so cert.pem and tunnel credentials persist.
func (c *CLI) dockerCmd(sub []string) []string {
	full := []string{
		"run", "--rm",
		"-v", c.home() + ":" + containerHome,
		c.image(),
	}
	return append(full, sub...)
}

func (c *CLI) run(args ...string) (string, error) {
	if c.Run != nil {
		return c.Run(args...)
	}
	if c.Raw == nil {
		return "", errors.New("docker is not available")
	}
	out, err := c.Raw.RunRaw(c.dockerCmd(args)...)
	return strings.TrimSpace(out), err
}

// LoggedIn reports whether a prior `cloudflared tunnel login` has produced a
// cert.pem. Only its existence is checked: the file is owned by the container's
// uid and this app never reads it. Without it, `tunnel create` cannot run; the
// app prompts the user to run the login command interactively.
func (c *CLI) LoggedIn() bool {
	_, err := os.Stat(filepath.Join(c.home(), "cert.pem"))
	return err == nil
}

// loginHint tells the user how to complete the interactive, browser-based login
// through the same container image the app uses.
func (c *CLI) loginHint() string {
	return fmt.Sprintf(`cloudflared login required: run on the host:
  docker run --rm -it -p %d:%d -v %s:%s %s tunnel login
then open the printed Cloudflare URL and try again`,
		loginPort, loginPort, c.home(), containerHome, c.image())
}

// loginContainerCmd builds the detached `docker run` for the interactive login.
func (c *CLI) loginContainerCmd() []string {
	return []string{
		"run", "-d", "--rm", "--name", loginContainerName,
		"-p", fmt.Sprintf("%d:%d", loginPort, loginPort),
		"-v", c.home() + ":" + containerHome,
		c.image(),
		"tunnel", "login",
	}
}

// LoginURL starts (or restarts) the interactive-login container and returns the
// Cloudflare authorization URL the user must open in a browser. The container
// runs detached, publishes the callback port, and exits on its own once the
// login completes (writing cert.pem into the mounted home). The URL is read
// from the container's log, so the user never needs to run cloudflared
// themselves.
func (c *CLI) LoginURL() (string, error) {
	if c.Raw == nil {
		return "", errors.New("docker is not available: " + c.loginHint())
	}
	// A stale login container from an earlier attempt must not block the
	// callback port (once the login completed, `--rm` already removed it).
	_, _ = c.Raw.RunRaw("rm", "-f", loginContainerName)
	if _, err := c.Raw.RunRaw(c.loginContainerCmd()...); err != nil {
		return "", fmt.Errorf("start cloudflared login: %w", err)
	}
	var tail string
	for i := 0; i < 20; i++ {
		out, _ := c.Raw.RunRaw("logs", loginContainerName)
		tail = out
		if m := loginURLRe.FindStringSubmatch(out); m != nil {
			return m[1], nil
		}
		time.Sleep(c.pollDelay())
	}
	return "", fmt.Errorf("cloudflared login did not print an authorization URL (log tail: %s)", tail)
}

func (c *CLI) pollDelay() time.Duration {
	if c.PollDelay > 0 {
		return c.PollDelay
	}
	return 500 * time.Millisecond
}

// credFile is the shape cloudflared persists as a tunnel's credentials file.
type credFile struct {
	AccountTag   string `json:"AccountTag"`
	TunnelID     string `json:"TunnelID"`
	TunnelSecret string `json:"TunnelSecret"`
}

// tunnelJSON covers the output shapes `tunnel create --output json` may emit:
// the credentials-file casing (AccountTag/TunnelID/TunnelSecret), the shorthand
// casing (id/name/secret), or the tunnel-object casing (id/name/token) where
// the credentials live in the base64 `token` payload.
type tunnelJSON struct {
	AccountTag   string `json:"AccountTag"`
	TunnelID     string `json:"TunnelID"`
	TunnelSecret string `json:"TunnelSecret"`
	ID           string `json:"id"`
	Name         string `json:"name"`
	Secret       string `json:"secret"`
	Token        string `json:"token"`
}

// tokenPayload is the base64 JSON a `tunnel create` token encodes:
// {"a": account tag, "t": tunnel id, "s": tunnel secret}.
type tokenPayload struct {
	AccountTag   string `json:"a"`
	TunnelID     string `json:"t"`
	TunnelSecret string `json:"s"`
}

// decodeToken decodes the credentials cloudflared packs into the `token`
// field of `tunnel create --output json`.
func decodeToken(token string) (tokenPayload, error) {
	var tp tokenPayload
	for _, enc := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding} {
		if b, err := enc.DecodeString(token); err == nil {
			if err := json.Unmarshal(b, &tp); err == nil && tp.AccountTag != "" && tp.TunnelID != "" {
				return tp, nil
			}
		}
	}
	return tp, fmt.Errorf("token is not a base64 credentials payload")
}

// EnsureTunnel returns the id of the named tunnel plus its credentials, creating
// it via the container if it does not exist yet. creds is nil when the tunnel
// already existed (its credential file is recreated only on a fresh create), so
// callers reuse their cached copy in that case. It fails with a prompt-to-login
// error when no cert.pem is present.
func (c *CLI) EnsureTunnel(name string) (string, []byte, error) {
	if !c.LoggedIn() {
		return "", nil, errors.New(c.loginHint())
	}
	out, err := c.run("tunnel", "create", "--output", "json", name)
	if err == nil {
		return c.parseCreated(out)
	}
	if !strings.Contains(strings.ToLower(out), "already exists") {
		return "", nil, fmt.Errorf("cloudflared tunnel create: %s", out)
	}
	id, err := c.findTunnel(name)
	if err != nil {
		return "", nil, err
	}
	return id, nil, nil
}

// parseCreated extracts the tunnel id (and the credentials JSON when the
// `--output json` format is honoured) from the create output. A legacy
// plain-text output yields the id with nil credentials.
func (c *CLI) parseCreated(out string) (string, []byte, error) {
	var tj tunnelJSON
	if err := json.Unmarshal([]byte(out), &tj); err == nil {
		id := tj.TunnelID
		if id == "" {
			id = tj.ID
		}
		tag, secret := tj.AccountTag, tj.TunnelSecret
		if secret == "" {
			secret = tj.Secret
		}
		if tj.Token != "" {
			tp, err := decodeToken(tj.Token)
			if err != nil {
				return "", nil, fmt.Errorf("cloudflared tunnel create: cannot read credentials from token: %w", err)
			}
			tag, secret = tp.AccountTag, tp.TunnelSecret
			if id == "" {
				id = tp.TunnelID
			}
		}
		if !uuidRe.MatchString(id) {
			return "", nil, fmt.Errorf("cloudflared tunnel create: unexpected output: %q", out)
		}
		creds, err := json.Marshal(credFile{
			AccountTag:   tag,
			TunnelID:     id,
			TunnelSecret: secret,
		})
		if err != nil {
			return "", nil, err
		}
		return id, creds, nil
	}
	if m := createdRe.FindStringSubmatch(out); m != nil {
		return m[1], nil, nil
	}
	return "", nil, fmt.Errorf("cloudflared tunnel create: unexpected output: %q", out)
}

// findTunnel looks up an existing tunnel's id by name.
func (c *CLI) findTunnel(name string) (string, error) {
	if out, err := c.run("tunnel", "info", name); err == nil {
		if m := infoIDRe.FindStringSubmatch(out); m != nil {
			return m[1], nil
		}
	}
	out, err := c.run("tunnel", "list")
	if err != nil {
		return "", fmt.Errorf("cloudflared tunnel list: %s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && uuidRe.MatchString(fields[0]) && fields[1] == name {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("tunnel %q not found under this cloudflared login", name)
}

// RouteDNS points the DNS CNAME for hostname at the named tunnel, so traffic to
// that hostname is proxied from the Cloudflare edge to this tunnel. It is
// idempotent: re-running against an already-routed hostname reports "already
// configured" and succeeds, so the app may call it for every ingress hostname
// on every launch without tracking state.
func (c *CLI) RouteDNS(tunnel, hostname string) error {
	args := []string{"tunnel", "route", "dns", tunnel, hostname}
	out, err := c.run(args...)
	if err != nil {
		return fmt.Errorf("cloudflared tunnel route dns: %s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "Added CNAME "+hostname) ||
			strings.Contains(line, hostname+" is already configured to route") {
			return nil
		}
	}
	return fmt.Errorf("cloudflared tunnel route dns: unexpected output: %q", out)
}
