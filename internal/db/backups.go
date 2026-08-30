package db

import (
	"database/sql"
	"fmt"
)

// Backup is the persisted metadata for one on-disk snapshot of a service.
type Backup struct {
	ID        string
	ServiceID string
	Kind      string
	Filename  string
	Size      int64
	CreatedAt string
	Note      string
}

const backupCols = "id, service_id, kind, filename, size, created_at, note"

func scanBackup(row interface{ Scan(...any) error }) (Backup, error) {
	var b Backup
	if err := row.Scan(&b.ID, &b.ServiceID, &b.Kind, &b.Filename, &b.Size, &b.CreatedAt, &b.Note); err != nil {
		return Backup{}, err
	}
	return b, nil
}

func (d *DB) CreateBackup(b Backup) error {
	_, err := d.db.Exec(
		"INSERT INTO backups (id, service_id, kind, filename, size, created_at, note) VALUES (?, ?, ?, ?, ?, ?, ?)",
		b.ID, b.ServiceID, b.Kind, b.Filename, b.Size, b.CreatedAt, b.Note)
	return err
}

func (d *DB) GetBackup(id string) (Backup, error) {
	row := d.db.QueryRow("SELECT "+backupCols+" FROM backups WHERE id = ?", id)
	b, err := scanBackup(row)
	if err == sql.ErrNoRows {
		return Backup{}, fmt.Errorf("backup %q not found", id)
	}
	return b, err
}

func (d *DB) ListBackups(serviceID string) ([]Backup, error) {
	rows, err := d.db.Query("SELECT "+backupCols+" FROM backups WHERE service_id = ? ORDER BY created_at DESC", serviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Backup
	for rows.Next() {
		b, err := scanBackup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// DeleteBackupsByService removes a service's backup metadata rows. It does not
// remove the snapshot files on disk (the web layer handles that best-effort).
func (d *DB) DeleteBackupsByService(serviceID string) error {
	_, err := d.db.Exec("DELETE FROM backups WHERE service_id = ?", serviceID)
	return err
}
