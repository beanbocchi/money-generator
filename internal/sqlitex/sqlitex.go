// Package sqlitex opens SQLite databases with the pure-Go modernc driver (no cgo, so the
// binary stays static).
package sqlitex

import (
	"database/sql"
	"net/url"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// OpenReadOnly opens path in read-only mode. The live trade database is written by the
// bot in WAL mode; a read-only connection never writes to it, not even a checkpoint.
func OpenReadOnly(path string) (*sql.DB, error) {
	return sql.Open("sqlite", "file:"+url.PathEscape(path)+"?mode=ro")
}

// Open opens or creates path read-write.
func Open(path string) (*sql.DB, error) {
	return sql.Open("sqlite", "file:"+url.PathEscape(path))
}
