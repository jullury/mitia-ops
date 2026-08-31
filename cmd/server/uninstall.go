package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Paths mirror the ones scripts/install.sh writes, so `uninstall` reverses it.
const (
	unitPath = "/etc/systemd/system/mitia-ops.service"
	binPath  = "/usr/local/bin/mitia-ops"
	etcDir   = "/etc/mitia-ops"
	dataDir  = "/var/lib/mitia-ops"
)

// isUninstall reports whether the binary was invoked as `mitia-ops uninstall`.
func isUninstall(args []string) bool {
	return len(args) > 0 && args[0] == "uninstall"
}

// uninstall reverses scripts/install.sh: it stops and removes the systemd unit
// and deletes the installed binary and config. Must run as root (sudo). It
// refuses to touch the source checkout or any Docker stack — only what the
// installer placed under /usr/local, /etc and systemd.
//
// The data directory (/var/lib/mitia-ops — database, deployments, backups) is
// kept by default so nothing is destroyed unintentionally; pass --purge-data to
// also delete it, which is destructive and irreversible.
func uninstall(args []string) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("uninstall must run as root (e.g. 'sudo %s uninstall')", binName())
	}
	purgeData := false
	for _, a := range args[1:] {
		if a == "-y" || a == "--yes" || a == "--purge-data" {
			purgeData = true
		}
	}

	fmt.Println("mitia-ops uninstall — this removes the installed app:")
	fmt.Printf("  binary: %s\n", binPath)
	fmt.Printf("  config: %s\n", etcDir)
	fmt.Printf("  unit:   %s\n", unitPath)
	if purgeData {
		fmt.Printf("  data:   %s  (database, deployments, backups) — PURGED\n", dataDir)
	} else {
		fmt.Printf("  data:   %s  (kept; use --purge-data to also remove)\n", dataDir)
	}

	if purgeData {
		fmt.Print("Type 'uninstall' to confirm PERMANENT deletion of all data: ")
		ans, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if strings.TrimSpace(ans) != "uninstall" {
			fmt.Println("aborted.")
			return nil
		}
	}

	// Stop and remove the systemd unit first so nothing restarts mid-removal.
	runCtl("stop", "mitia-ops")
	runCtl("disable", "mitia-ops")
	if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove unit %s: %w", unitPath, err)
	}
	runCtl("daemon-reload")

	for _, p := range []string{binPath, etcDir} {
		if err := os.RemoveAll(p); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", p, err)
		}
	}
	if purgeData {
		if err := os.RemoveAll(dataDir); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", dataDir, err)
		}
	}

	fmt.Println("uninstalled mitia-ops.")
	return nil
}

// runCtl shells out to systemctl, tolerating its absence on non-systemd hosts.
func runCtl(args ...string) {
	cmd := exec.Command("systemctl", args...)
	if err := cmd.Run(); err != nil {
		fmt.Printf("note: systemctl %v: %v\n", args, err)
	}
}

func binName() string {
	if len(os.Args) > 0 && os.Args[0] != "" {
		return os.Args[0]
	}
	return "mitia-ops"
}
