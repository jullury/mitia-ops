package main

import (
	"log"
	"net/http"
	"os"

	"github.com/jullury/mitia-ops/internal/crypto"
	"github.com/jullury/mitia-ops/internal/db"
	"github.com/jullury/mitia-ops/internal/docker"
	"github.com/jullury/mitia-ops/internal/web"
)

func main() {
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

	mux := web.New(web.Config{
		DB:        d,
		Cipher:    cipher,
		DeployDir: deployDir,
		Docker:    docker.NewCLI(),
	})
	log.Printf("mitia-ops listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
