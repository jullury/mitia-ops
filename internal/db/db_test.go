package db

import (
	"database/sql"
	"path/filepath"
	"regexp"
	"testing"

	_ "modernc.org/sqlite"
)

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func TestServiceCRUD(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	id, err := d.CreateService("minio", "main")
	if err != nil {
		t.Fatal(err)
	}
	if !uuidRe.MatchString(id) {
		t.Fatalf("expected a UUID id, got %q", id)
	}

	if err := d.SetConfigItems(id, []ConfigItem{
		{Key: "MINIO_ROOT_USER", Value: "admin"},
		{Key: "MINIO_ROOT_PASSWORD", Value: "secret123"},
	}); err != nil {
		t.Fatal(err)
	}

	items, err := d.ConfigItems(id)
	if err != nil {
		t.Fatal(err)
	}
	if items["MINIO_ROOT_USER"] != "admin" {
		t.Fatalf("unexpected value: %q", items["MINIO_ROOT_USER"])
	}

	list, err := d.ListServices()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Kind != "minio" || !list[0].Enabled {
		t.Fatalf("unexpected list: %+v", list)
	}
}

func TestDeleteServiceCascades(t *testing.T) {
	d, _ := Open(filepath.Join(t.TempDir(), "test.db"))
	defer d.Close()
	id, _ := d.CreateService("minio", "x")
	_ = d.SetConfigItems(id, []ConfigItem{{Key: "K", Value: "V"}})
	// verify FK pragma is on so the cascade below actually runs
	if !d.ForeignKeysEnabled() {
		t.Fatal("expected foreign keys enabled")
	}
	// existing external integrity: deleting a missing service is idempotent
	if err := d.DeleteService(id); err != nil {
		t.Fatalf("delete service: %v", err)
	}
	// row gone; config items cascaded away
	items, _ := d.ConfigItems(id)
	if len(items) != 0 {
		t.Fatalf("config items should cascade-delete with service, got %+v", items)
	}
	list, _ := d.ListServices()
	if len(list) != 0 {
		t.Fatalf("service list should be empty after delete, got %+v", list)
	}
	// a second delete is a harmless no-op
	if err := d.DeleteService(id); err != nil {
		t.Fatalf("second delete should be a no-op, got %v", err)
	}
}

func TestDeleteConfigItems(t *testing.T) {
	d, _ := Open(filepath.Join(t.TempDir(), "test.db"))
	defer d.Close()

	id, _ := d.CreateService("cloudflared", "tun")
	_ = d.SetConfigItems(id, []ConfigItem{
		{Key: "CF_TUNNEL", Value: "t"},
		{Key: "CF_INGRESS_0_HOST", Value: "a.example.com"},
		{Key: "CF_INGRESS_0_SERVICE", Value: "http://localhost:80"},
		{Key: "CF_INGRESS_1_HOST", Value: "b.example.com"},
		{Key: "CF_INGRESS_1_SERVICE", Value: "http://localhost:81"},
	})

	if err := d.DeleteConfigItems(id, []string{"CF_INGRESS_1_HOST", "CF_INGRESS_1_SERVICE"}); err != nil {
		t.Fatal(err)
	}
	items, _ := d.ConfigItems(id)
	if items["CF_INGRESS_1_HOST"] != "" || items["CF_INGRESS_1_SERVICE"] != "" {
		t.Fatalf("stale ingress keys should be deleted, got %+v", items)
	}
	if items["CF_INGRESS_0_HOST"] != "a.example.com" || items["CF_TUNNEL"] != "t" {
		t.Fatalf("unrelated keys must survive, got %+v", items)
	}

	// deleting a non-existent / empty set is a no-op
	if err := d.DeleteConfigItems(id, nil); err != nil {
		t.Fatal(err)
	}
	// service scoping: no error when another service has no such keys
	other, _ := d.CreateService("minio", "m")
	_ = d.SetConfigItems(other, []ConfigItem{{Key: "CF_INGRESS_0_HOST", Value: "x"}})
	if err := d.DeleteConfigItems(other, []string{"CF_INGRESS_0_HOST"}); err != nil {
		t.Fatal(err)
	}
	// the first service's config is untouched by the other service's delete
	items, _ = d.ConfigItems(id)
	if items["CF_INGRESS_0_HOST"] != "a.example.com" {
		t.Fatalf("delete must be scoped to the service, got %+v", items)
	}
}

func TestMigrateLegacyIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")

	// Build a pre-UUID database by hand (services.id INTEGER PRIMARY KEY).
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE services (id INTEGER PRIMARY KEY, kind TEXT NOT NULL, name TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 1);`,
		`CREATE TABLE config_items (id INTEGER PRIMARY KEY, service_id INTEGER NOT NULL REFERENCES services(id) ON DELETE CASCADE, key TEXT NOT NULL, value TEXT NOT NULL, UNIQUE(service_id, key));`,
		`INSERT INTO services (id, kind, name) VALUES (1, 'cloudflared', 'tun'), (2, 'minio', 'main');`,
		`INSERT INTO config_items (service_id, key, value) VALUES (2, 'MINIO_ROOT_USER', 'admin');`,
		`INSERT INTO config_items (service_id, key, value) VALUES (2, 'MINIO_VOLUME_NAME', '2_minio_data');`,
	} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	raw.Close()

	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	migrated, err := d.MigrateLegacyIDs()
	if err != nil {
		t.Fatal(err)
	}
	if !migrated {
		t.Fatal("expected a legacy schema migration to run")
	}
	// Legacy integer ids must no longer resolve.
	if _, err := d.ServiceByID("2"); err == nil {
		t.Fatal("legacy id 2 should not survive the migration")
	}

	// The migrated services carry UUIDs, and config items followed their
	// service across the remap.
	pending, err := d.PendingMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 {
		t.Fatalf("expected 2 pending id remaps, got %d", len(pending))
	}
	svcs, err := d.ListServices()
	if err != nil {
		t.Fatal(err)
	}
	if len(svcs) != 2 {
		t.Fatalf("expected 2 services after migration, got %d", len(svcs))
	}
	var minioID string
	for _, svc := range svcs {
		if !uuidRe.MatchString(svc.ID) {
			t.Fatalf("service id should be a UUID, got %q", svc.ID)
		}
		if svc.Kind == "minio" {
			minioID = svc.ID
		}
	}
	if pending["2"] != minioID {
		t.Fatalf("remap for legacy id 2 = %q, want %q", pending["2"], minioID)
	}
	if items, _ := d.ConfigItems(minioID); items["MINIO_ROOT_USER"] != "admin" {
		t.Fatalf("config items must follow the service to its new id, got %+v", items)
	}
	// Values embedding the legacy id (project-scoped volume names) are
	// rewritten to the new prefix and keep matching the migrated volumes.
	if items, _ := d.ConfigItems(minioID); items["MINIO_VOLUME_NAME"] != minioID+"_minio_data" {
		t.Fatalf("volume-name values must be rewritten to the new id, got %q", items["MINIO_VOLUME_NAME"])
	}

	// Idempotent: a second run is a no-op.
	again, err := d.MigrateLegacyIDs()
	if err != nil {
		t.Fatal(err)
	}
	if again {
		t.Fatal("second migration should be a no-op")
	}

	// Completing the bookkeeping leaves nothing pending.
	if err := d.CompleteMigrations(); err != nil {
		t.Fatal(err)
	}
	if p, _ := d.PendingMigrations(); len(p) != 0 {
		t.Fatalf("expected no pending migrations after completion, got %v", p)
	}
}
