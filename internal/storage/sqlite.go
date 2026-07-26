package storage

import (
	"database/sql"
	"strings"

	_ "modernc.org/sqlite"
)

// SQLiteStore is a persistent, SQLite-backed implementation of Store.
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore opens (or creates) a SQLite database at dbPath and returns a
// Store backed by it.
func NewSQLiteStore(dbPath string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS kv (
		namespace TEXT NOT NULL,
		key       TEXT NOT NULL,
		value     BLOB,
		PRIMARY KEY (namespace, key)
	)`)
	if err != nil {
		db.Close()
		return nil, err
	}

	return &SQLiteStore{db: db}, nil
}

func (s *SQLiteStore) Get(namespace, key string) ([]byte, bool) {
	var value []byte
	err := s.db.QueryRow(
		"SELECT value FROM kv WHERE namespace = ? AND key = ?",
		namespace, key,
	).Scan(&value)
	if err != nil {
		return nil, false
	}
	return value, true
}

func (s *SQLiteStore) Put(namespace, key string, value []byte) {
	_, _ = s.db.Exec(
		"INSERT OR REPLACE INTO kv (namespace, key, value) VALUES (?, ?, ?)",
		namespace, key, value,
	)
}

func (s *SQLiteStore) Delete(namespace, key string) bool {
	res, err := s.db.Exec(
		"DELETE FROM kv WHERE namespace = ? AND key = ?",
		namespace, key,
	)
	if err != nil {
		return false
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false
	}
	return n > 0
}

func (s *SQLiteStore) List(namespace, prefix string) []string {
	// Escape any existing LIKE wildcards in the prefix.
	escaped := strings.NewReplacer("%", "\\%", "_", "\\_").Replace(prefix)
	pattern := escaped + "%"

	rows, err := s.db.Query(
		"SELECT key FROM kv WHERE namespace = ? AND key LIKE ? ESCAPE '\\'",
		namespace, pattern,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err == nil {
			keys = append(keys, k)
		}
	}
	return keys
}

func (s *SQLiteStore) Clear(namespace string) {
	_, _ = s.db.Exec("DELETE FROM kv WHERE namespace = ?", namespace)
}

func (s *SQLiteStore) Namespaces() []string {
	rows, err := s.db.Query("SELECT DISTINCT namespace FROM kv")
	if err != nil {
		return nil
	}
	defer rows.Close()

	var ns []string
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil {
			ns = append(ns, n)
		}
	}
	return ns
}

// Close closes the underlying database connection.
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}
