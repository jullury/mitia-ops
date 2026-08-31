package db

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

type DB struct {
	db *sql.DB
}

type Service struct {
	ID      string
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

func (d *DB) CreateService(kind, name string) (string, error) {
	id := uuid.NewString()
	if _, err := d.db.Exec("INSERT INTO services (id, kind, name) VALUES (?, ?, ?)", id, kind, name); err != nil {
		return "", err
	}
	return id, nil
}

func (d *DB) ListServices() ([]Service, error) {
	// UUIDs don't sort like the legacy integer ids did, so preserve insertion
	// order via rowid.
	rows, err := d.db.Query("SELECT id, kind, name, enabled FROM services ORDER BY rowid")
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

func (d *DB) ServiceByID(id string) (*Service, error) {
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
func (d *DB) DeleteService(id string) error {
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

func (d *DB) SetConfigItems(serviceID string, items []ConfigItem) error {
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
func (d *DB) DeleteConfigItems(serviceID string, keys []string) error {
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

func (d *DB) ConfigItems(serviceID string) (map[string]string, error) {
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

// legacySchema reports whether services.id is still the old INTEGER primary
// key (i.e. the DB predates UUID service ids) rather than the current TEXT.
func (d *DB) legacySchema() (bool, error) {
	rows, err := d.db.Query("PRAGMA table_info(services)")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == "id" && typ != "TEXT" {
			return true, nil
		}
	}
	return false, rows.Err()
}

// MigrateLegacyIDs upgrades a pre-UUID database: every service gets a fresh
// UUID, config_items are remapped, and the old<->new pairing is recorded in
// migrated_ids so the caller can rename the matching deploy directories and
// Docker volumes (resumable across restarts). It is a no-op on a schema that
// already uses TEXT ids. The rebuild runs in a single transaction with foreign
// keys off (the new config_items is built before the old tables are dropped).
func (d *DB) MigrateLegacyIDs() (bool, error) {
	legacy, err := d.legacySchema()
	if err != nil {
		return false, err
	}
	if !legacy {
		return false, nil
	}
	// Load the legacy rows and mint a UUID per id so the remap is stable.
	rows, err := d.db.Query("SELECT id, kind, name, enabled FROM services ORDER BY id")
	if err != nil {
		return false, err
	}
	type oldRow struct {
		id, kind, name string
		enabled        int
	}
	var olds []oldRow
	for rows.Next() {
		var r oldRow
		if err := rows.Scan(&r.id, &r.kind, &r.name, &r.enabled); err != nil {
			rows.Close()
			return false, err
		}
		olds = append(olds, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}

	// PRAGMA foreign_keys is a per-connection setting and a no-op inside a
	// transaction, so pin the whole rebuild to one dedicated connection and
	// toggle it around the rewrite (re-enabled unconditionally on the way out).
	conn, err := d.db.Conn(context.Background())
	if err != nil {
		return false, err
	}
	defer conn.Close()
	defer conn.ExecContext(context.Background(), "PRAGMA foreign_keys = ON")
	if _, err := conn.ExecContext(context.Background(), "PRAGMA foreign_keys = OFF"); err != nil {
		return false, err
	}
	tx, err := conn.BeginTx(context.Background(), nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`CREATE TABLE services_new (
		id      TEXT PRIMARY KEY,
		kind    TEXT NOT NULL,
		name    TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1
	)`); err != nil {
		return false, err
	}
	if _, err := tx.Exec("CREATE TEMP TABLE idmap (old_id TEXT PRIMARY KEY, new_id TEXT NOT NULL)"); err != nil {
		return false, err
	}
	for _, r := range olds {
		uid := uuid.NewString()
		if _, err := tx.Exec("INSERT INTO idmap (old_id, new_id) VALUES (?, ?)", r.id, uid); err != nil {
			return false, err
		}
		if _, err := tx.Exec("INSERT INTO services_new (id, kind, name, enabled) VALUES (?, ?, ?, ?)", uid, r.kind, r.name, r.enabled); err != nil {
			return false, err
		}
	}

	if _, err := tx.Exec(`CREATE TABLE config_items_new (
		id         INTEGER PRIMARY KEY,
		service_id TEXT NOT NULL REFERENCES services(id) ON DELETE CASCADE,
		key        TEXT NOT NULL,
		value      TEXT NOT NULL,
		UNIQUE(service_id, key)
	)`); err != nil {
		return false, err
	}
	if _, err := tx.Exec(`INSERT INTO config_items_new (id, service_id, key, value)
		SELECT ci.id, m.new_id, ci.key, ci.value
		FROM config_items ci JOIN idmap m ON m.old_id = ci.service_id`); err != nil {
		return false, err
	}

	// Rename over the old tables (config_items is dropped first: it references
	// services) and record the pending disk/docker migration.
	if _, err := tx.Exec("DROP TABLE config_items"); err != nil {
		return false, err
	}
	if _, err := tx.Exec("DROP TABLE services"); err != nil {
		return false, err
	}
	if _, err := tx.Exec("ALTER TABLE config_items_new RENAME TO config_items"); err != nil {
		return false, err
	}
	if _, err := tx.Exec("ALTER TABLE services_new RENAME TO services"); err != nil {
		return false, err
	}
	// Some config values embed the legacy id in a project-scoped volume name
	// (e.g. MINIO_VOLUME_NAME=3_minio_data after a resize). Rewrite those value
	// prefixes so the compose keeps pointing at the surviving volume.
	idMap, err := tx.Query("SELECT old_id, new_id FROM idmap")
	if err != nil {
		return false, err
	}
	for idMap.Next() {
		var oldID, newID string
		if err := idMap.Scan(&oldID, &newID); err != nil {
			idMap.Close()
			return false, err
		}
		if _, err := tx.Exec("UPDATE config_items SET value = ? || substr(value, length(?) + 1) WHERE value LIKE ? || '_%'", newID, oldID, oldID); err != nil {
			idMap.Close()
			return false, err
		}
	}
	idMap.Close()
	if _, err := tx.Exec("INSERT INTO migrated_ids (old_id, new_id) SELECT old_id, new_id FROM idmap"); err != nil {
		return false, err
	}

	return true, tx.Commit()
}

// PendingMigrations returns the recorded legacy id -> UUID pairs whose deploy
// directories and Docker volumes still await renaming. Rows are deleted by
// CompleteMigrations once the on-disk work is done.
func (d *DB) PendingMigrations() (map[string]string, error) {
	rows, err := d.db.Query("SELECT old_id, new_id FROM migrated_ids")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var o, n string
		if err := rows.Scan(&o, &n); err != nil {
			return nil, err
		}
		out[o] = n
	}
	return out, rows.Err()
}

// CompleteMigrations clears the pending migration bookkeeping once the deploy
// directories and volumes have been renamed to their UUIDs.
func (d *DB) CompleteMigrations() error {
	_, err := d.db.Exec("DELETE FROM migrated_ids")
	return err
}

// MigrateMinioToGarage converts legacy "minio" services to the "garage" kind and
// migrates their config keys to the GARAGE_* namespace. Keys with no Garage
// equivalent (root user/password, console URL) are dropped; the access key is
// auto-generated on the service's next launch. Idempotent: a second run is a
// no-op because no kind="minio" rows remain.
func (d *DB) MigrateMinioToGarage() error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec("UPDATE services SET kind = 'garage' WHERE kind = 'minio'"); err != nil {
		return err
	}
	rename := map[string]string{
		"MINIO_HOSTNAME":       "GARAGE_HOSTNAME",
		"MINIO_VOLUME_SIZE":    "GARAGE_VOLUME_SIZE",
		"MINIO_VOLUME_NAME":    "GARAGE_VOLUME_NAME",
		"minio_backup_buckets": "garage_backup_buckets",
	}
	for from, to := range rename {
		if _, err := tx.Exec("UPDATE config_items SET key = ? WHERE key = ?", to, from); err != nil {
			return err
		}
	}
	// Keys with no Garage counterpart: the old root/console credentials are not
	// used (Garage uses an auto-generated access key pair).
	dropped := []string{"MINIO_ROOT_USER", "MINIO_ROOT_PASSWORD", "MINIO_CONSOLE_URL"}
	for _, k := range dropped {
		if _, err := tx.Exec("DELETE FROM config_items WHERE key = ?", k); err != nil {
			return err
		}
	}
	return tx.Commit()
}
