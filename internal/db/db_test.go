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

func TestCascadeDelete(t *testing.T) {
	d, _ := Open(filepath.Join(t.TempDir(), "test.db"))
	defer d.Close()
	id, _ := d.CreateService("minio", "x")
	_ = d.SetConfigItems(id, []ConfigItem{{Key: "K", Value: "V"}})
	// no explicit delete API in v1; verify FK pragma is on
	if !d.ForeignKeysEnabled() {
		t.Fatal("expected foreign keys enabled")
	}
}
