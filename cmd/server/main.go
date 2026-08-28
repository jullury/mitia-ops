package main

import (
	"bufio"
	"log"
	"net/http"
	"os"
	"strings"

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
	mux := web.New(web.Config{
		DB:         d,
		Cipher:     cipher,
		DeployDir:  deployDir,
		MailcowDir: dataDir + "/mailcow",
		Docker:     cli,
		DockerRaw:  cli,
	})
	log.Printf("mitia-ops listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
