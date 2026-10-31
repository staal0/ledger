package app

// Support for the tests of the core and of the plugins, which need MariaDB.
// They run when LEDGER_TEST_DSN points at a disposable database whose name
// contains "test" (it is wiped), and are skipped otherwise:
//
//	LEDGER_TEST_DSN='ledger:ledger@tcp(127.0.0.1:3306)/ledger_test' go test ./...
//
// The packages' tests run at the same time; a database lock lets one package
// at a time use the database.

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"

	"github.com/go-sql-driver/mysql"
)

// TB is the part of *testing.T the test support uses.
type TB interface {
	Helper()
	Context() context.Context
	Skip(args ...any)
	Fatal(args ...any)
	Fatalf(format string, args ...any)
}

var (
	testDBOnce sync.Once
	testDB     *sql.DB
	testDBLock *sql.Conn // holds the lock until the tests end
	testDBErr  error
)

// TestApp returns an app on a freshly emptied test database. With login it
// behaves as with OIDC configured; without, it is in local mode. The plugins
// the test binary imports are part of it.
func TestApp(t TB, login bool) *App {
	t.Helper()
	dsn := os.Getenv("LEDGER_TEST_DSN")
	if dsn == "" {
		t.Skip("set LEDGER_TEST_DSN to a disposable MariaDB database to run database tests")
	}
	if cfg, err := mysql.ParseDSN(dsn); err != nil || !strings.Contains(cfg.DBName, "test") {
		t.Fatalf("refusing LEDGER_TEST_DSN: the database name must contain \"test\" because the tests delete all its data")
	}
	testDBOnce.Do(func() {
		if testDB, testDBErr = openDB(dsn); testDBErr != nil {
			return
		}
		ctx := context.Background()
		if testDBLock, testDBErr = testDB.Conn(ctx); testDBErr != nil {
			return
		}
		var got sql.NullInt64
		if testDBErr = testDBLock.QueryRowContext(ctx, "SELECT GET_LOCK('ledger_tests', 600)").Scan(&got); testDBErr == nil && got.Int64 != 1 {
			testDBErr = fmt.Errorf("another package's tests held the test database for 10 minutes")
		}
		// Start from nothing so the migrations themselves are tested too.
		if testDBErr == nil {
			testDBErr = dropAll(ctx)
		}
		if testDBErr == nil {
			testDBErr = migrateAll(testDB)
		}
	})
	if testDBErr != nil {
		t.Fatal(testDBErr)
	}
	if err := emptyAll(t.Context()); err != nil {
		t.Fatal(err)
	}

	locales, err := loadLocales("")
	if err != nil {
		t.Fatal(err)
	}
	pages, err := parsePages()
	if err != nil {
		t.Fatal(err)
	}
	a := &App{cfg: Config{BaseURL: "http://localhost"}, db: testDB, locales: locales, pages: pages}
	if login {
		a.cfg.OIDCIssuer = "http://127.0.0.1:9" // never contacted by these tests
	} else {
		TestExec(t, "INSERT INTO users (subject, name) VALUES ('local', 'Local')")
	}
	return a
}

// eachTable runs q for every table but schema_migrations on one connection,
// without foreign key checks, as tables of users and budgets refer to each other.
func eachTable(ctx context.Context, q func(conn *sql.Conn, table string) error) error {
	conn, err := testDB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	rows, err := conn.QueryContext(ctx, `SELECT table_name FROM information_schema.tables
		WHERE table_schema = DATABASE() AND table_name <> 'schema_migrations'`)
	if err != nil {
		return err
	}
	var tables []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			rows.Close()
			return err
		}
		tables = append(tables, t)
	}
	rows.Close()
	if _, err := conn.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 0"); err != nil {
		return err
	}
	defer conn.ExecContext(context.Background(), "SET FOREIGN_KEY_CHECKS = 1") //nolint:errcheck // the connection is closed right after
	for _, t := range tables {
		if err := q(conn, t); err != nil {
			return err
		}
	}
	return nil
}

// emptyAll deletes every row but the default budget, which the migrations make.
func emptyAll(ctx context.Context) error {
	return eachTable(ctx, func(conn *sql.Conn, table string) error {
		q := "DELETE FROM `" + table + "`"
		if table == "budgets" {
			q += " WHERE NOT is_default"
		}
		_, err := conn.ExecContext(ctx, q)
		return err
	})
}

// dropAll drops every table, schema_migrations too.
func dropAll(ctx context.Context) error {
	if err := eachTable(ctx, func(conn *sql.Conn, table string) error {
		_, err := conn.ExecContext(ctx, "DROP TABLE `"+table+"`")
		return err
	}); err != nil {
		return err
	}
	_, err := testDB.ExecContext(ctx, "DROP TABLE IF EXISTS schema_migrations")
	return err
}

// TestExec runs a statement on the test database.
func TestExec(t TB, q string, args ...any) {
	t.Helper()
	if _, err := testDB.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// TestQueryInt is the one number q selects, 0 for NULL.
func TestQueryInt(t TB, q string, args ...any) int64 {
	t.Helper()
	var n sql.NullInt64
	if err := testDB.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n.Int64
}

// TestQueryInts is the numbers q selects, one per row.
func TestQueryInts(t TB, q string, args ...any) []int64 {
	t.Helper()
	rows, err := testDB.Query(q, args...)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var n int64
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	return out
}

// TestRequest sends a request through the app's full handler chain, as a
// browser in English, with form posted when it is not nil.
func TestRequest(a *App, method, path string, form url.Values, cookie string) *httptest.ResponseRecorder {
	body := ""
	if form != nil {
		body = form.Encode()
	}
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Accept-Language", "en")
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	}
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, r)
	return w
}

// Handler is the app's full handler chain, for a test that builds its own request.
func (a *App) Handler() http.Handler { return a.routes() }

// MustStatus stops the test unless the response has status want.
func MustStatus(t TB, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("status %d, want %d: %s", w.Code, want, w.Body.String())
	}
}

// TestLineIDs returns the year's lines as "section/name" → id.
func TestLineIDs(t TB, year int) map[string]int64 {
	t.Helper()
	rows, err := testDB.Query(`SELECT l.id, l.section, l.name FROM budget_lines l
		JOIN budget_years y ON y.id = l.year_id WHERE y.year = ?`, year)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	ids := map[string]int64{}
	for rows.Next() {
		var id int64
		var section, name string
		if err := rows.Scan(&id, &section, &name); err != nil {
			t.Fatal(err)
		}
		ids[section+"/"+name] = id
	}
	return ids
}

// TestSaveForm is the budget form as the browser posts it: every line's name
// and amounts (line id → month → text), the values the page was loaded with,
// plus the clicked action.
func TestSaveForm(t TB, a *App, year int, action string, amounts map[int64]map[int]string) url.Values {
	t.Helper()
	b, err := testLoadYear(t, a, year)
	if err != nil || b == nil {
		t.Fatalf("loading %d: %v", year, err)
	}
	f := url.Values{"action": {action}, "original": {originalValues(b)}}
	for _, lines := range b.Lines {
		for _, l := range lines {
			f.Set(fmt.Sprintf("n_%d", l.ID), l.Name)
			for m := 1; m <= 12; m++ {
				f.Set(fmt.Sprintf("a_%d_%d", l.ID, m), a.locales["en"].Input(l.Amounts[m]))
			}
			for m, v := range amounts[l.ID] {
				f.Set(fmt.Sprintf("a_%d_%d", l.ID, m), v)
			}
		}
	}
	return f
}

// testLoadYear loads a year of the default budget.
func testLoadYear(t TB, a *App, year int) (*BudgetYear, error) {
	t.Helper()
	budget, err := defaultBudget(t.Context(), testDB)
	if err != nil {
		return nil, err
	}
	return a.loadBudget(t.Context(), budget, year)
}

// TestSetupYear creates year in the default budget with the given lines
// ("section/name") and returns all its lines as "section/name" → id.
func TestSetupYear(t TB, a *App, year int, lines ...string) map[string]int64 {
	t.Helper()
	MustStatus(t, TestRequest(a, "POST", fmt.Sprintf("/budget/%d/create", year), url.Values{}, ""), 303)
	for _, l := range lines {
		section, name, _ := strings.Cut(l, "/")
		// Adding gives a placeholder name and points at the new line; rename it like the browser does.
		w := TestRequest(a, "POST", fmt.Sprintf("/budget/%d", year), TestSaveForm(t, a, year, "add:"+section, nil), "")
		MustStatus(t, w, 303)
		_, id, ok := strings.Cut(w.Header().Get("Location"), "?added=")
		if !ok {
			t.Fatalf("adding a line redirected to %q", w.Header().Get("Location"))
		}
		form := TestSaveForm(t, a, year, "save", nil)
		form.Set("n_"+id, name)
		MustStatus(t, TestRequest(a, "POST", fmt.Sprintf("/budget/%d", year), form, ""), 303)
	}
	return TestLineIDs(t, year)
}

// TestAmount is a line's stored amount for a month, 0 when there is none.
func TestAmount(t TB, line int64, month int) Money {
	t.Helper()
	var m Money
	if err := testDB.QueryRow("SELECT amount FROM budget_amounts WHERE line_id = ? AND month = ?", line, month).Scan(&m); err != nil {
		return 0
	}
	return m
}
