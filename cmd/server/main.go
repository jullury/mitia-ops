package main

import (
	"bufio"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/jullury/mitia-ops/internal/cloudflared"
	"github.com/jullury/mitia-ops/internal/crypto"
	"github.com/jullury/mitia-ops/internal/db"
	"github.com/jullury/mitia-ops/internal/docker"
	"github.com/jullury/mitia-ops/internal/web"
)

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

	addr := os.Getenv("MITIAOPS_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	cli := docker.NewCLI()

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
		DB:          d,
		Cipher:      cipher,
		DeployDir:   deployDir,
		Docker:      cli,
		DockerRaw:   cli,
		Cloudflared: cf,
	})
	// Bring back every service flagged "start on boot" (runs `up` per service in
	// the background), so an app restart / host reboot restores the stack.
	app.AutoStart()
	log.Printf("mitia-ops listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, app))
}
