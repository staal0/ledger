package main

import (
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

func TestMigrationResumesAfterPartialRun(t *testing.T) {
	testApp(t, false)
	// A first start that stopped after the first statement of the initial migration.
	schema, err := fs.ReadFile(embedded, "migrations/001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	exec(t, `DROP TABLE IF EXISTS budget_history, budget_amounts, budget_lines, budgets, sessions, settings, users, schema_migrations`)
	first, _, _ := strings.Cut(string(schema), ";\n")
	exec(t, first)
	if err := migrateAll(testDB); err != nil {
		t.Fatalf("second start: %v", err)
	}
	if n := queryInt(t, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = 'budget_amounts'"); n != 1 {
		t.Error("the migration did not finish")
	}
}

func TestMigrationWithCRLF(t *testing.T) {
	testApp(t, false)
	t.Cleanup(func() {
		exec(t, "DROP TABLE IF EXISTS crlf_a, crlf_b")
		exec(t, "DELETE FROM schema_migrations WHERE name LIKE 'crlf/%'")
	})
	fsys := fstest.MapFS{"001.sql": {Data: []byte("CREATE TABLE crlf_a (id INT);\r\nCREATE TABLE crlf_b (id INT);\r\n")}}
	if err := migrate(testDB, "crlf", fsys); err != nil {
		t.Fatal(err)
	}
	if n := queryInt(t, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name IN ('crlf_a', 'crlf_b')"); n != 2 {
		t.Errorf("%d of 2 tables created", n)
	}
}
