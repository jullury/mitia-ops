package cloudflared

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testID = "6ff42ae9-29c3-4b8f-93b2-5b2acd3e737d"

func TestRouteDNSAdded(t *testing.T) {
	c := loggedInCLI(t, func(args ...string) (string, error) {
		if strings.Join(args, " ") != "tunnel route dns my-tunnel app.example.com" {
			t.Fatalf("unexpected args: %v", args)
		}
		return "2026-08-28T22:47:27Z INF Added CNAME app.example.com which will route to this tunnel tunnelID=" + testID, nil
	})
	if err := c.RouteDNS("my-tunnel", "app.example.com"); err != nil {
		t.Fatal(err)
	}
}

func TestRouteDNSAlreadyConfigured(t *testing.T) {
	c := loggedInCLI(t, func(args ...string) (string, error) {
		return "2026-08-28T22:47:34Z INF app.example.com is already configured to route to your tunnel tunnelID=" + testID, nil
	})
	if err := c.RouteDNS("my-tunnel", "app.example.com"); err != nil {
		t.Fatal(err)
	}
}

func TestRouteDNSCommandFailure(t *testing.T) {
	c := loggedInCLI(t, func(args ...string) (string, error) {
		return "2026-08-28T22:47:40Z ERR Cloudflare API error: Zone not found", nil
	})
	err := c.RouteDNS("my-tunnel", "missing.example.com")
	if err == nil || !strings.Contains(err.Error(), "cloudflared tunnel route dns") {
		t.Fatalf("expected a route dns error, got %v", err)
	}
}

func loggedInCLI(t *testing.T, run func(args ...string) (string, error)) *CLI {
	t.Helper()
	c := New()
	c.Home = t.TempDir()
	if err := os.WriteFile(filepath.Join(c.Home, "cert.pem"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	c.Run = run
	return c
}

func TestLoggedInWithoutCert(t *testing.T) {
	c := New()
	c.Home = t.TempDir()
	if c.LoggedIn() {
		t.Fatal("empty home must not count as logged in")
	}
	_, _, err := c.EnsureTunnel("my-tunnel")
	if err == nil || !strings.Contains(err.Error(), "tunnel login") {
		t.Fatalf("expected a login prompt when no cert.pem exists, got %v", err)
	}
	if !strings.Contains(err.Error(), "docker run --rm -it -p 4979:4979") {
		t.Fatalf("login prompt must show the containerized command, got %v", err)
	}
}

func TestEnsureHome(t *testing.T) {
	c := New()
	c.Home = filepath.Join(t.TempDir(), "sub", "cloudflared")
	if err := c.EnsureHome(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(c.Home)
	if err != nil {
		t.Fatal(err)
	}
	perm := info.Mode().Perm()
	// As root we chown to the container uid and keep 0700; otherwise the dir
	// falls back to 0777 so the container uid can still write into it.
	if perm != 0o700 && perm != 0o777 {
		t.Fatalf("home mode = %o, want 700 or 777", perm)
	}
	if _, err := os.Stat(filepath.Join(c.Home, "cert.pem")); err == nil {
		t.Fatal("EnsureHome must not fabricate a login")
	}
}

// recordingRunner captures the raw docker args RunRaw receives.
type recordingRunner struct{ calls [][]string }

func (r *recordingRunner) RunRaw(args ...string) (string, error) {
	r.calls = append(r.calls, args)
	return `{"AccountTag":"acct","TunnelID":"` + testID + `","TunnelSecret":"sec"}`, nil
}

func TestRunsCloudflaredViaContainer(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "cert.pem"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	raw := &recordingRunner{}
	c := New()
	c.Home = home
	c.Raw = raw

	got, creds, err := c.EnsureTunnel("my-tunnel")
	if err != nil {
		t.Fatal(err)
	}
	if got != testID {
		t.Fatalf("EnsureTunnel id = %q, want %q", got, testID)
	}
	if !strings.Contains(string(creds), `"TunnelSecret":"sec"`) {
		t.Fatalf("EnsureTunnel must return the captured credentials: %s", creds)
	}
	if len(raw.calls) != 1 {
		t.Fatalf("expected one docker run, got %d: %v", len(raw.calls), raw.calls)
	}
	args := raw.calls[0]
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"run", "--rm",
		"-v " + home + ":" + "/home/nonroot/.cloudflared",
		"cloudflare/cloudflared:latest",
		"tunnel create --output json my-tunnel",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("docker invocation missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "--user") {
		t.Fatalf("container must run as the image's uid (no --user) so the home resolves:\n%s", joined)
	}
}

func TestEnsureTunnelCreates(t *testing.T) {
	secret := "RE5Xm5Kw+rFhKvVJ2hAA6l1YqV2QSRcd3R6C2gFmQhA="
	c := loggedInCLI(t, func(args ...string) (string, error) {
		if strings.Join(args, " ") != "tunnel create --output json my-tunnel" {
			t.Fatalf("unexpected args: %v", args)
		}
		return `{"AccountTag":"acct","TunnelID":"` + testID + `","TunnelSecret":"` + secret + `"}`, nil
	})
	id, creds, err := c.EnsureTunnel("my-tunnel")
	if err != nil {
		t.Fatal(err)
	}
	if id != testID {
		t.Fatalf("id = %q, want %q", id, testID)
	}
	want := `{"AccountTag":"acct","TunnelID":"` + testID + `","TunnelSecret":"` + secret + `"}`
	if string(creds) != want {
		t.Fatalf("creds = %s, want %s", creds, want)
	}
}

func TestEnsureTunnelCreatesShorthandJSON(t *testing.T) {
	c := loggedInCLI(t, func(args ...string) (string, error) {
		return `{"id":"` + testID + `","name":"my-tunnel","secret":"base64secret","attached-routes":[]}`, nil
	})
	id, creds, err := c.EnsureTunnel("my-tunnel")
	if err != nil {
		t.Fatal(err)
	}
	if id != testID {
		t.Fatalf("id = %q, want %q", id, testID)
	}
	if !strings.Contains(string(creds), `"TunnelSecret":"base64secret"`) {
		t.Fatalf("creds must bridge the shorthand output to the file shape: %s", creds)
	}
}

func TestEnsureTunnelDecodesTokenPayload(t *testing.T) {
	// Real `tunnel create --output json` returns the tunnel object with the
	// credentials packed into a base64 token: {"a":tag,"t":id,"s":secret}.
	token := base64.RawURLEncoding.EncodeToString([]byte(
		`{"a":"ef2fc5db48cf6d55b98c1fcdd18c62ea","t":"` + testID + `","s":"2Eu1HVe4vqhYnM+Z+umWlZg/6EVQqhqTp/H/dneUXeg="}`,
	))
	c := loggedInCLI(t, func(args ...string) (string, error) {
		return `{"id":"` + testID + `","name":"my-tunnel","created_at":"2026-01-01","deleted_at":"0001-01-01T00:00:00Z","connections":[],"token":"` + token + `"}`, nil
	})
	id, creds, err := c.EnsureTunnel("my-tunnel")
	if err != nil {
		t.Fatal(err)
	}
	if id != testID {
		t.Fatalf("id = %q, want %q", id, testID)
	}
	for _, field := range []string{
		`"AccountTag":"ef2fc5db48cf6d55b98c1fcdd18c62ea"`,
		`"TunnelID":"` + testID + `"`,
		`"TunnelSecret":"2Eu1HVe4vqhYnM+Z+umWlZg/6EVQqhqTp/H/dneUXeg="`,
	} {
		if !strings.Contains(string(creds), field) {
			t.Fatalf("creds missing %s:\n%s", field, creds)
		}
	}
}

func TestEnsureTunnelIgnoresTrailingWarningLog(t *testing.T) {
	token := base64.RawURLEncoding.EncodeToString([]byte(
		`{"a":"ef2fc5db48cf6d55b98c1fcdd18c62ea","t":"` + testID + `","s":"2Eu1HVe4vqhYnM+Z+umWlZg/6EVQqhqTp/H/dneUXeg="}`,
	))
	c := loggedInCLI(t, func(args ...string) (string, error) {
		return `{"id":"` + testID + `","name":"my-tunnel","created_at":"2026-01-01","deleted_at":"0001-01-01T00:00:00Z","connections":[],"token":"` + token + `"}` +
			`{"level":"warn","message":"Your version 2026.8.2 is outdated.","time":"2026-08-31T12:25:42Z"}`, nil
	})
	id, creds, err := c.EnsureTunnel("my-tunnel")
	if err != nil {
		t.Fatal(err)
	}
	if id != testID {
		t.Fatalf("id = %q, want %q", id, testID)
	}
	for _, field := range []string{
		`"AccountTag":"ef2fc5db48cf6d55b98c1fcdd18c62ea"`,
		`"TunnelID":"` + testID + `"`,
		`"TunnelSecret":"2Eu1HVe4vqhYnM+Z+umWlZg/6EVQqhqTp/H/dneUXeg="`,
	} {
		if !strings.Contains(string(creds), field) {
			t.Fatalf("creds missing %s:\n%s", field, creds)
		}
	}
}

func TestEnsureTunnelRejectsUndecodableToken(t *testing.T) {
	c := loggedInCLI(t, func(args ...string) (string, error) {
		return `{"id":"` + testID + `","token":"not-a-token"}`, nil
	})
	if _, _, err := c.EnsureTunnel("my-tunnel"); err == nil || !strings.Contains(err.Error(), "cannot read credentials from token") {
		t.Fatalf("expected a token decode error, got %v", err)
	}
}

func TestEnsureTunnelLegacyPlainOutput(t *testing.T) {
	c := loggedInCLI(t, func(args ...string) (string, error) {
		return "Created tunnel my-tunnel with id " + testID, nil
	})
	id, creds, err := c.EnsureTunnel("my-tunnel")
	if err != nil {
		t.Fatal(err)
	}
	if id != testID {
		t.Fatalf("id = %q, want %q", id, testID)
	}
	if creds != nil {
		t.Fatalf("legacy plain output cannot carry credentials, got %s", creds)
	}
}

func TestEnsureTunnelReusesExisting(t *testing.T) {
	c := loggedInCLI(t, func(args ...string) (string, error) {
		switch strings.Join(args, " ") {
		case "tunnel create --output json my-tunnel":
			return "Your tunnel 'my-tunnel' already exists.", os.ErrProcessDone
		case "tunnel info my-tunnel":
			return "NAME: my-tunnel\nID: " + testID + "\nCREATED: 2024", nil
		}
		t.Fatalf("unexpected args: %v", args)
		return "", nil
	})
	id, creds, err := c.EnsureTunnel("my-tunnel")
	if err != nil {
		t.Fatal(err)
	}
	if id != testID {
		t.Fatalf("id = %q, want %q", id, testID)
	}
	if creds != nil {
		t.Fatalf("a reused tunnel has no fresh credentials, got %s", creds)
	}
}

func TestEnsureTunnelFallsBackToList(t *testing.T) {
	c := loggedInCLI(t, func(args ...string) (string, error) {
		switch strings.Join(args, " ") {
		case "tunnel create --output json my-tunnel":
			return "Your tunnel 'my-tunnel' already exists.", os.ErrProcessDone
		case "tunnel info my-tunnel":
			return "some unknown format", nil
		case "tunnel list":
			return "ID                                   NAME        CREATED      CONNECTOR\n" +
				"6ff42ae9-29c3-4b8f-93b2-5b2acd3e737d my-tunnel   2025-01-01\n", nil
		}
		t.Fatalf("unexpected args: %v", args)
		return "", nil
	})
	id, creds, err := c.EnsureTunnel("my-tunnel")
	if err != nil {
		t.Fatal(err)
	}
	if id != testID {
		t.Fatalf("id = %q, want %q", id, testID)
	}
	if creds != nil {
		t.Fatalf("a reused tunnel has no fresh credentials, got %s", creds)
	}
}

// scriptedRunner replies to docker invocations from a map keyed by joined args.
type scriptedRunner struct {
	responses map[string]string
}

func (r *scriptedRunner) RunRaw(args ...string) (string, error) {
	out, ok := r.responses[strings.Join(args, " ")]
	if !ok {
		return "", errors.New("unexpected docker invocation: " + strings.Join(args, " "))
	}
	return out, nil
}

func loginLogWithURL(url string) string {
	return "Please open the following URL and log in with your Cloudflare account:\n\n" +
		url + "\n\nLeave cloudflared running to download the cert automatically.\n"
}

const testLoginURL = "https://dash.cloudflare.com/argotunnel?aud=&callback=https%3A%2F%2Flogin.cloudflareaccess.org%2Ftoken%3D"

func TestLoginURL(t *testing.T) {
	home := t.TempDir()
	raw := &scriptedRunner{responses: map[string]string{
		"rm -f " + loginContainerName: "",
		"run -d --rm --name " + loginContainerName + " -p 4979:4979 -v " + home + ":/home/nonroot/.cloudflared cloudflare/cloudflared:latest tunnel login": "container-id",
		"logs " + loginContainerName: loginLogWithURL(testLoginURL),
	}}
	c := New()
	c.Home = home
	c.Raw = raw

	got, err := c.LoginURL()
	if err != nil {
		t.Fatal(err)
	}
	if got != testLoginURL {
		t.Fatalf("LoginURL = %q, want %q", got, testLoginURL)
	}
}

func TestLoginURLWaitsForLog(t *testing.T) {
	home := t.TempDir()
	calls := 0
	raw := &scriptedRunner{responses: map[string]string{
		"rm -f " + loginContainerName: "",
		"run -d --rm --name " + loginContainerName + " -p 4979:4979 -v " + home + ":/home/nonroot/.cloudflared cloudflare/cloudflared:latest tunnel login": "container-id",
	}}
	c := New()
	c.Home = home
	c.Raw = &pollingRunner{first: raw, called: &calls}
	c.PollDelay = time.Millisecond

	got, err := c.LoginURL()
	if err != nil {
		t.Fatal(err)
	}
	if got != testLoginURL {
		t.Fatalf("LoginURL = %q, want %q", got, testLoginURL)
	}
}

// pollingRunner returns empty logs until the second call, then the URL log.
type pollingRunner struct {
	first  *scriptedRunner
	called *int
}

func (r *pollingRunner) RunRaw(args ...string) (string, error) {
	if strings.Join(args, " ") == "logs "+loginContainerName {
		*r.called++
		if *r.called < 2 {
			return "", nil
		}
		return loginLogWithURL(testLoginURL), nil
	}
	return r.first.RunRaw(args...)
}

func TestLoginURLSurfacesStartFailure(t *testing.T) {
	home := t.TempDir()
	raw := &scriptedRunner{responses: map[string]string{"rm -f " + loginContainerName: ""}}
	c := New()
	c.Home = home
	c.Raw = raw

	if _, err := c.LoginURL(); err == nil || !strings.Contains(err.Error(), "start cloudflared login") {
		t.Fatalf("expected a start failure, got %v", err)
	}
}
