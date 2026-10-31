package app

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
	if err := dropAll(t.Context()); err != nil {
		t.Fatal(err)
	}
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

// TestMigrationToBudgets upgrades a database from before several budgets, with
// data, and checks that every year ends up in the default budget unchanged.
func TestMigrationToBudgets(t *testing.T) {
	testApp(t, false)
	if err := dropAll(t.Context()); err != nil {
		t.Fatal(err)
	}
	old, _ := fs.Sub(embedded, "migrations")
	before := fstest.MapFS{}
	for _, f := range []string{"001_init.sql", "002_history.sql", "003_roles.sql"} {
		b, err := fs.ReadFile(old, f)
		if err != nil {
			t.Fatal(err)
		}
		before[f] = &fstest.MapFile{Data: b}
	}
	if _, err := testDB.Exec("CREATE TABLE schema_migrations (name VARCHAR(255) PRIMARY KEY, applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)"); err != nil {
		t.Fatal(err)
	}
	if err := migrate(testDB, "core", before); err != nil {
		t.Fatal(err)
	}
	exec(t, "INSERT INTO budgets (year) VALUES (2025), (2026)")
	exec(t, "INSERT INTO budget_lines (budget_id, section, name) SELECT id, 'income', 'Salary' FROM budgets")
	exec(t, "INSERT INTO budget_amounts (line_id, month, amount) SELECT id, 1, 100 FROM budget_lines")

	if err := migrateAll(testDB); err != nil {
		t.Fatal(err)
	}
	if n := queryInt(t, `SELECT COUNT(*) FROM budget_years y JOIN budgets b ON b.id = y.budget_id
		JOIN budget_lines l ON l.year_id = y.id JOIN budget_amounts a ON a.line_id = l.id
		WHERE b.is_default AND b.everyone AND a.amount = 100`); n != 2 {
		t.Errorf("%d of 2 years with their lines and amounts in the default budget", n)
	}
}
