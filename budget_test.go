package main

import (
	"fmt"
	"html"
	"net/url"
	"strings"
	"testing"
)

// setupYear creates a budget through the app with the given lines ("section/name").
func setupYear(t *testing.T, a *App, year int, lines ...string) map[string]int64 {
	t.Helper()
	mustStatus(t, request(a, "POST", fmt.Sprintf("/budget/%d/create", year), url.Values{}, ""), 303)
	for _, l := range lines {
		section, name, _ := strings.Cut(l, "/")
		// Adding gives a placeholder name and points at the new line; rename it like the browser does.
		w := request(a, "POST", fmt.Sprintf("/budget/%d", year), saveForm(t, a, year, "add:"+section, nil), "")
		mustStatus(t, w, 303)
		_, id, ok := strings.Cut(w.Header().Get("Location"), "?added=")
		if !ok {
			t.Fatalf("adding a line redirected to %q", w.Header().Get("Location"))
		}
		form := saveForm(t, a, year, "save", nil)
		form.Set("n_"+id, name)
		mustStatus(t, request(a, "POST", fmt.Sprintf("/budget/%d", year), form, ""), 303)
	}
	return lineIDs(t, year)
}

func amount(t *testing.T, line int64, month int) Money {
	t.Helper()
	var m Money
	err := testDB.QueryRow("SELECT amount FROM budget_amounts WHERE line_id = ? AND month = ?", line, month).Scan(&m)
	if err != nil {
		return 0
	}
	return m
}

func TestBudgetLifecycle(t *testing.T) {
	a := testApp(t, false)
	ids := setupYear(t, a, 2030, "income/Salary", "expense/Rent", "expense/Power", "allocation/Savings")
	if len(ids) != 4 {
		t.Fatalf("lines after adding: %v", ids)
	}

	// Save amounts the way the browser sends them, grouped and with decimals.
	form := saveForm(t, a, 2030, "save", map[int64]map[int]string{
		ids["income/Salary"]:      {1: "30,000.50", 2: "30000"},
		ids["expense/Rent"]:       {1: "10,000"},
		ids["expense/Power"]:      {1: "1234.5"},
		ids["allocation/Savings"]: {1: "15,000"},
	})
	mustStatus(t, request(a, "POST", "/budget/2030", form, ""), 303)
	if got := amount(t, ids["income/Salary"], 1); got != 3000050 {
		t.Errorf("salary January = %d, want 3000050", got)
	}

	b, _ := a.loadBudget(t.Context(), 2030)
	in, ex, al := b.total("income"), b.total("expense"), b.total("allocation")
	if diff := in[1] - ex[1] - al[1]; diff != 376600 { // 30000.50 − 11234.50 − 15000
		t.Errorf("January difference = %d, want 376600", diff)
	}
	if in[0] != 6000050 {
		t.Errorf("income year total = %d, want 6000050", in[0])
	}

	// The page shows the totals in the chosen format.
	page := request(a, "GET", "/budget/2030", nil, "").Body.String()
	for _, want := range []string{`value="30,000.50"`, "3,766", "Total income"} {
		if !strings.Contains(page, want) {
			t.Errorf("budget page lacks %q", want)
		}
	}

	// An invalid amount is rejected and nothing is changed.
	form = saveForm(t, a, 2030, "save", map[int64]map[int]string{ids["income/Salary"]: {1: "30.5,0"}})
	mustStatus(t, request(a, "POST", "/budget/2030", form, ""), 400)
	if got := amount(t, ids["income/Salary"], 1); got != 3000050 {
		t.Errorf("after an invalid save, salary January = %d", got)
	}

	// Clearing a field removes the amount.
	form = saveForm(t, a, 2030, "save", map[int64]map[int]string{ids["expense/Power"]: {1: ""}})
	mustStatus(t, request(a, "POST", "/budget/2030", form, ""), 303)
	if n := queryInt(t, "SELECT COUNT(*) FROM budget_amounts WHERE line_id = ?", ids["expense/Power"]); n != 0 {
		t.Errorf("cleared line still has %d amounts", n)
	}

	// Renaming, and an empty name is ignored.
	form = saveForm(t, a, 2030, "save", nil)
	form.Set(fmt.Sprintf("n_%d", ids["expense/Rent"]), "Housing")
	form.Set(fmt.Sprintf("n_%d", ids["expense/Power"]), " ")
	mustStatus(t, request(a, "POST", "/budget/2030", form, ""), 303)
	ids2 := lineIDs(t, 2030)
	if ids2["expense/Housing"] != ids["expense/Rent"] || ids2["expense/Power"] != ids["expense/Power"] {
		t.Errorf("after renaming: %v", ids2)
	}

	// Moving Power above Housing, then deleting it.
	mustStatus(t, request(a, "POST", "/budget/2030", saveForm(t, a, 2030, fmt.Sprintf("up:%d", ids["expense/Power"]), nil), ""), 303)
	b, _ = a.loadBudget(t.Context(), 2030)
	if b.Lines["expense"][0].ID != ids["expense/Power"] {
		t.Errorf("Power was not moved up: first expense is %q", b.Lines["expense"][0].Name)
	}
	mustStatus(t, request(a, "POST", "/budget/2030", saveForm(t, a, 2030, fmt.Sprintf("delete:%d", ids["expense/Power"]), nil), ""), 303)
	if _, ok := lineIDs(t, 2030)["expense/Power"]; ok {
		t.Error("Power was not deleted")
	}

	// A line in another year cannot be deleted through this year's form.
	other := setupYear(t, a, 2031, "income/Other")
	mustStatus(t, request(a, "POST", "/budget/2030", saveForm(t, a, 2030, fmt.Sprintf("delete:%d", other["income/Other"]), nil), ""), 303)
	if _, ok := lineIDs(t, 2031)["income/Other"]; !ok {
		t.Error("a line of 2031 was deleted from the 2030 form")
	}
}

func TestBudgetNotFound(t *testing.T) {
	a := testApp(t, false)
	mustStatus(t, request(a, "GET", "/budget/2030", nil, ""), 200) // offers to create it
	mustStatus(t, request(a, "POST", "/budget/2030", url.Values{"action": {"save"}}, ""), 404)
	mustStatus(t, request(a, "GET", "/budget/2030/export", nil, ""), 404)
	mustStatus(t, request(a, "GET", "/budget/12", nil, ""), 404)
}

func TestCopyYear(t *testing.T) {
	a := testApp(t, false)
	ids := setupYear(t, a, 2030, "income/Salary", "expense/Rent")
	form := saveForm(t, a, 2030, "save", map[int64]map[int]string{ids["income/Salary"]: {3: "100"}})
	mustStatus(t, request(a, "POST", "/budget/2030", form, ""), 303)

	mustStatus(t, request(a, "POST", "/budget/2031/create", url.Values{"from": {"2030"}}, ""), 303)
	mustStatus(t, request(a, "POST", "/budget/2032/create", url.Values{"from": {"2030"}, "amounts": {"1"}}, ""), 303)
	for year, want := range map[int]Money{2031: 0, 2032: 10000} {
		lines := lineIDs(t, year)
		if len(lines) != 2 {
			t.Errorf("%d has lines %v, want the two of 2030", year, lines)
		}
		if got := amount(t, lines["income/Salary"], 3); got != want {
			t.Errorf("%d salary in March = %d, want %d", year, got, want)
		}
	}
	// Creating an existing year again changes nothing.
	mustStatus(t, request(a, "POST", "/budget/2032/create", url.Values{}, ""), 303)
	if n := queryInt(t, "SELECT COUNT(*) FROM budgets WHERE year = 2032"); n != 1 {
		t.Errorf("%d budgets for 2032", n)
	}
}

func TestStatsAndExport(t *testing.T) {
	a := testApp(t, false)
	for _, y := range []int{2029, 2030} {
		ids := setupYear(t, a, y, "income/Salary", "expense/Rent")
		salary := map[int]string{2029: "1000", 2030: "1100"}[y]
		form := saveForm(t, a, y, "save", map[int64]map[int]string{ids["income/Salary"]: {1: salary}, ids["expense/Rent"]: {1: "400"}})
		mustStatus(t, request(a, "POST", fmt.Sprintf("/budget/%d", y), form, ""), 303)
	}
	page := html.UnescapeString(request(a, "GET", "/stats", nil, "").Body.String()) // as the browser shows it
	for _, want := range []string{"1,100", "▲ +10.0%", `"Years":[2029,2030]`} {
		if !strings.Contains(page, want) {
			t.Errorf("stats page lacks %q", want)
		}
	}
	page = html.UnescapeString(request(a, "GET", "/stats?from=2030&to=2030", nil, "").Body.String())
	if strings.Contains(page, `"Years":[2029`) || !strings.Contains(page, "▲ +10.0%") {
		t.Error("a one-year range should show only 2030, compared with 2029")
	}

	w := request(a, "GET", "/budget/2030/export", nil, "")
	mustStatus(t, w, 200)
	if ct := w.Header().Get("Content-Type"); ct != "application/vnd.oasis.opendocument.spreadsheet" {
		t.Errorf("export content type %q", ct)
	}
	cells := readSheet(t, w.Body.Bytes())
	if v := cells["C4"].value; v != 1100 { // first income line, January
		t.Errorf("exported salary January = %v, want 1100", v)
	}
}

func TestDemo(t *testing.T) {
	a := testApp(t, false)
	if err := seedDemo(testDB, "da"); err != nil {
		t.Fatal(err)
	}
	if n := queryInt(t, "SELECT COUNT(*) FROM budgets"); n != 3 {
		t.Errorf("demo created %d budgets, want 3", n)
	}
	if n := queryInt(t, "SELECT COUNT(*) FROM budget_lines WHERE name = 'Madkonto'"); n != 3 {
		t.Errorf("Danish demo lines: found Madkonto %d times, want 3", n)
	}
	if err := seedDemo(testDB, "en"); err == nil {
		t.Error("demo ran on a database that already has budgets")
	}
	mustStatus(t, request(a, "GET", "/stats", nil, ""), 200)
}
