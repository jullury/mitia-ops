package db

import (
	"path/filepath"
	"testing"
)

func TestBackupsCRUD(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	sid, err := d.CreateService("postgres", "pg")
	if err != nil {
		t.Fatal(err)
	}
	b := Backup{ID: "b1", ServiceID: sid, Kind: "postgres", Filename: "20260831T120000-postgres.tar.gz", Size: 1234, CreatedAt: "2026-08-31T12:00:00Z"}
	if err := d.CreateBackup(b); err != nil {
		t.Fatal(err)
	}
	got, err := d.GetBackup("b1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != "postgres" || got.Size != 1234 || got.ServiceID != sid {
		t.Fatalf("GetBackup = %+v", got)
	}
	list, err := d.ListBackups(sid)
	if err != nil || len(list) != 1 || list[0].Filename != b.Filename {
		t.Fatalf("ListBackups = %v, %v", list, err)
	}
}

func TestBackupsCascadeOnServiceDelete(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	sid, _ := d.CreateService("minio", "m")
	if err := d.CreateBackup(Backup{ID: "x", ServiceID: sid, Kind: "minio", Filename: "f"}); err != nil {
		t.Fatal(err)
	}
	if err := d.DeleteService(sid); err != nil {
		t.Fatal(err)
	}
	if bs, _ := d.ListBackups(sid); len(bs) != 0 {
		t.Fatalf("backups should cascade on service delete, got %v", bs)
	}
	if _, err := d.GetBackup("x"); err == nil {
		t.Fatal("backup row should be gone after service delete")
	}
}

func TestListBackupsNewestFirst(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	sid, _ := d.CreateService("minio", "m")
	_ = d.CreateBackup(Backup{ID: "a", ServiceID: sid, Kind: "minio", Filename: "a", CreatedAt: "2026-01-01T00:00:00Z"})
	_ = d.CreateBackup(Backup{ID: "b", ServiceID: sid, Kind: "minio", Filename: "b", CreatedAt: "2026-06-01T00:00:00Z"})
	list, _ := d.ListBackups(sid)
	if len(list) != 2 || list[0].ID != "b" || list[1].ID != "a" {
		t.Fatalf("ListBackups should be newest first, got %v", list)
	}
}

func TestDeleteBackupsByService(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	sid, _ := d.CreateService("minio", "m")
	_ = d.CreateBackup(Backup{ID: "a", ServiceID: sid, Kind: "minio", Filename: "a"})
	if err := d.DeleteBackupsByService(sid); err != nil {
		t.Fatal(err)
	}
	if list, _ := d.ListBackups(sid); len(list) != 0 {
		t.Fatalf("expected no backups after DeleteBackupsByService, got %v", list)
	}
}
