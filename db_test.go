package main

// Tests that need MariaDB. They run when LEDGER_TEST_DSN points at a disposable
// database whose name contains "test" (it is wiped), and are skipped otherwise:
//
//	LEDGER_TEST_DSN='ledger:ledger@tcp(127.0.0.1:3306)/ledger_test' go test ./...

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/go-sql-driver/mysql"
)

var (
	testDBOnce sync.Once
	testDB     *sql.DB
	testDBErr  error
)

// testApp returns an app on a freshly emptied test database. With login it
// behaves as with OIDC configured; without, it is in local mode.
func testApp(t *testing.T, login bool) *App {
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
		// Start from nothing so the migrations themselves are tested too.
		if _, testDBErr = testDB.Exec(`DROP TABLE IF EXISTS budget_history, budget_amounts, budget_lines, budget_members, budget_groups, user_groups, budget_years, budgets,
			sessions, settings, users, schema_migrations`); testDBErr == nil {
			testDBErr = migrateAll(testDB)
		}
	})
	if testDBErr != nil {
		t.Fatal(testDBErr)
	}
	for _, q := range []string{"DELETE FROM budget_years", "DELETE FROM budgets WHERE NOT is_default", "DELETE FROM sessions", "DELETE FROM settings", "DELETE FROM users"} {
		exec(t, q)
	}

	pages, err := parsePages()
	if err != nil {
		t.Fatal(err)
	}
	a := &App{cfg: Config{BaseURL: "http://localhost"}, db: testDB, locales: testLocales(t), pages: pages}
	if login {
		a.cfg.OIDCIssuer = "http://127.0.0.1:9" // never contacted by these tests
	} else {
		exec(t, "INSERT INTO users (subject, name) VALUES ('local', 'Local')")
	}
	return a
}

func exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := testDB.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func queryInt(t *testing.T, q string, args ...any) int64 {
	t.Helper()
	var n sql.NullInt64
	if err := testDB.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n.Int64
}

// request sends a request through the app's full handler chain.
func request(a *App, method, path string, form url.Values, cookie string) *httptest.ResponseRecorder {
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	r := httptest.NewRequest(method, path, body)
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

func mustStatus(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("status %d, want %d: %s", w.Code, want, w.Body.String())
	}
}

// lineIDs returns the year's lines as "section/name" → id.
func lineIDs(t *testing.T, year int) map[string]int64 {
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

// saveForm is the budget form as the browser posts it: every line's name and
// amounts (line id → month → text), the values the page was loaded with, plus
// the clicked action.
func saveForm(t *testing.T, a *App, year int, action string, amounts map[int64]map[int]string) url.Values {
	t.Helper()
	b, err := loadYear(t, a, year)
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

// loadYear loads a year of the default budget.
func loadYear(t *testing.T, a *App, year int) (*BudgetYear, error) {
	t.Helper()
	budget, err := defaultBudget(t.Context(), testDB)
	if err != nil {
		return nil, err
	}
	return a.loadBudget(t.Context(), budget, year)
}
