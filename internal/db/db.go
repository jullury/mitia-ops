package db

import (
	"database/sql"
	_ "embed"
	"fmt"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

type DB struct {
	db *sql.DB
}

type Service struct {
	ID      int64
	Kind    string
	Name    string
	Enabled bool
}

type ConfigItem struct {
	Key   string
	Value string
}

func Open(path string) (*DB, error) {
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := sqlDB.Exec(schemaSQL); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &DB{db: sqlDB}, nil
}

func (d *DB) Close() error { return d.db.Close() }

func (d *DB) ForeignKeysEnabled() bool {
	var v int
	_ = d.db.QueryRow("PRAGMA foreign_keys").Scan(&v)
	return v == 1
}

func (d *DB) CreateService(kind, name string) (int64, error) {
	res, err := d.db.Exec("INSERT INTO services (kind, name) VALUES (?, ?)", kind, name)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *DB) ListServices() ([]Service, error) {
	rows, err := d.db.Query("SELECT id, kind, name, enabled FROM services ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Service
	for rows.Next() {
		var s Service
		var en int
		if err := rows.Scan(&s.ID, &s.Kind, &s.Name, &en); err != nil {
			return nil, err
		}
		s.Enabled = en == 1
		out = append(out, s)
	}
	return out, rows.Err()
}

func (d *DB) ServiceByID(id int64) (*Service, error) {
	var s Service
	var en int
	err := d.db.QueryRow("SELECT id, kind, name, enabled FROM services WHERE id = ?", id).Scan(&s.ID, &s.Kind, &s.Name, &en)
	if err != nil {
		return nil, err
	}
	s.Enabled = en == 1
	return &s, nil
}

// DeleteService removes a service and (via the ON DELETE CASCADE foreign key)
// its config items, in a single transaction so no orphaned rows remain.
func (d *DB) DeleteService(id int64) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM services WHERE id = ?", id); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM config_items WHERE service_id = ?", id); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DB) SetConfigItems(serviceID int64, items []ConfigItem) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, it := range items {
		_, err := tx.Exec(
			"INSERT INTO config_items (service_id, key, value) VALUES (?, ?, ?) "+
				"ON CONFLICT(service_id, key) DO UPDATE SET value = excluded.value",
			serviceID, it.Key, it.Value)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteConfigItems removes the given keys for a service. Keys that don't exist
// are ignored; an empty key set is a no-op. Used to drop stale FieldList rows
// (e.g. old CF_INGRESS_n_* cells) after a re-save shrinks the list.
func (d *DB) DeleteConfigItems(serviceID int64, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, k := range keys {
		if _, err := tx.Exec("DELETE FROM config_items WHERE service_id = ? AND key = ?", serviceID, k); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (d *DB) ConfigItems(serviceID int64) (map[string]string, error) {
	rows, err := d.db.Query("SELECT key, value FROM config_items WHERE service_id = ?", serviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}
