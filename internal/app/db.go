package app

import (
	"database/sql"
	"fmt"
	"io/fs"
	"log"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

func openDB(dsn string) (*sql.DB, error) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return nil, err
	}
	cfg.ParseTime = true
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, err
	}
	db.SetConnMaxLifetime(3 * time.Minute)
	return db, db.Ping()
}

// migrateAll applies the core migrations and then those of each plugin.
func migrateAll(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		name VARCHAR(255) PRIMARY KEY,
		applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`)
	if err != nil {
		return err
	}
	core, _ := fs.Sub(embedded, "migrations")
	if err := migrate(db, "core", core); err != nil {
		return err
	}
	for _, p := range plugins {
		if m := p.Migrations(); m != nil {
			if err := migrate(db, p.ID(), m); err != nil {
				return err
			}
		}
	}
	return nil
}

// migrate runs the *.sql files in fsys in name order, each only once.
// Statements must end with a semicolon at the end of a line. MariaDB cannot roll
// back schema changes, so a migration that stopped partway runs again in full on
// the next start: its statements must be safe to repeat (IF NOT EXISTS).
func migrate(db *sql.DB, prefix string, fsys fs.FS) error {
	files, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return err
	}
	for _, f := range files {
		name := prefix + "/" + f
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE name = ?", name).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		b, err := fs.ReadFile(fsys, f)
		if err != nil {
			return err
		}
		for _, stmt := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), ";\n") {
			if strings.TrimSpace(stmt) == "" {
				continue
			}
			if _, err := db.Exec(stmt); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}
		if _, err := db.Exec("INSERT INTO schema_migrations (name) VALUES (?)", name); err != nil {
			return err
		}
		log.Printf("applied migration %s", name)
	}
	return nil
}
