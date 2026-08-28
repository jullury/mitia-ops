package db

import (
	"path/filepath"
	"testing"
)

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
	if id <= 0 {
		t.Fatalf("expected positive id, got %d", id)
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
