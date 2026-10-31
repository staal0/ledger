package bank

import (
	"html"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/staal0/ledger/internal/app"
)

func TestBankFill(t *testing.T) {
	a := app.TestApp(t, false)
	app.TestExec(t, "INSERT INTO settings (name, value) VALUES ('plugin.bank', '1')")
	lines := app.TestSetupYear(t, a, 2031, "income/Salary", "expense/Food", "expense/Rent")
	form := app.TestSaveForm(t, a, 2031, "save", map[int64]map[int]string{lines["expense/Food"]: {2: "1000"}, lines["expense/Rent"]: {2: "400"}})
	app.MustStatus(t, app.TestRequest(a, "POST", "/budget/2031", form, ""), 303)
	app.MustStatus(t, app.TestRequest(a, "POST", "/bank/accounts", url.Values{"name": {"Budget account"}, "format": {"laanspar"}}, ""), 303)
	account := strconv.FormatInt(app.TestQueryInt(t, "SELECT id FROM bank_accounts"), 10)
	previewAndImport(t, a, account, laanSparSample)
	assign := url.Values{"year": {"2031"}}
	for text, line := range map[string]string{"Netto Butik 123": "expense/Food", "Bager Hansen": "expense/Food", "Løn": "income/Salary"} {
		for _, id := range app.TestQueryInts(t, "SELECT id FROM bank_transactions WHERE text = ?", text) {
			s := strconv.FormatInt(id, 10)
			assign.Add("t", s)
			assign.Set("line_"+s, strconv.FormatInt(lines[line], 10))
		}
	}
	app.MustStatus(t, app.TestRequest(a, "POST", "/bank/transactions", assign, ""), 303)

	// Only empty amounts: Salary is filled, Food's budget is kept, Rent has no transactions.
	page := html.UnescapeString(app.TestRequest(a, "GET", "/bank/fill?year=2031", nil, "").Body.String())
	for _, want := range []string{"Fill 1 amounts", "1 amounts set by hand are kept", "Salary</td><td>Feb</td>"} {
		if !strings.Contains(page, want) {
			t.Errorf("fill preview lacks %q", want)
		}
	}
	app.MustStatus(t, app.TestRequest(a, "POST", "/bank/fill", url.Values{"year": {"2031"}}, ""), 303)
	if got := app.TestAmount(t, lines["income/Salary"], 2); got != 3100000 {
		t.Errorf("Salary in February = %d, want 31000.00", got)
	}
	if got := app.TestAmount(t, lines["expense/Food"], 2); got != 100000 {
		t.Errorf("Food in February = %d, want the budget kept", got)
	}
	if n := app.TestQueryInt(t, "SELECT COUNT(*) FROM budget_history WHERE line_id = ? AND field = 2", lines["income/Salary"]); n != 1 {
		t.Errorf("%d history rows for the filled amount, want 1", n)
	}

	// The rest of the month arrives: the amount the bank filled follows it.
	previewAndImport(t, a, account, "26-02-2031;Løn bonus;1.000,00;DKK;\r\n")
	bonus := strconv.FormatInt(app.TestQueryInt(t, "SELECT id FROM bank_transactions WHERE text = 'Løn bonus'"), 10)
	app.MustStatus(t, app.TestRequest(a, "POST", "/bank/transactions", url.Values{"year": {"2031"}, "t": {bonus}, "line_" + bonus: {strconv.FormatInt(lines["income/Salary"], 10)}}, ""), 303)
	if page := html.UnescapeString(app.TestRequest(a, "GET", "/bank/fill?year=2031", nil, "").Body.String()); !strings.Contains(page, "update from the bank") {
		t.Errorf("the second fill does not offer to update the filled amount: %s", page)
	}
	app.MustStatus(t, app.TestRequest(a, "POST", "/bank/fill", url.Values{"year": {"2031"}}, ""), 303)
	if got := app.TestAmount(t, lines["income/Salary"], 2); got != 3200000 {
		t.Errorf("Salary in February = %d, want 32000.00 after the rest of the month", got)
	}
	// Changed by hand, the amount is no longer the bank's and is kept.
	form = app.TestSaveForm(t, a, 2031, "save", map[int64]map[int]string{lines["income/Salary"]: {2: "30000"}})
	app.MustStatus(t, app.TestRequest(a, "POST", "/budget/2031", form, ""), 303)
	app.MustStatus(t, app.TestRequest(a, "POST", "/bank/fill", url.Values{"year": {"2031"}}, ""), 303)
	if got := app.TestAmount(t, lines["income/Salary"], 2); got != 3000000 {
		t.Errorf("Salary in February = %d, want 30000.00 as changed by hand", got)
	}

	// Overwrite: Food becomes what was spent; Rent keeps its budget; a linked line is left alone.
	app.TestExec(t, "UPDATE budget_lines SET link_budget_id = 1, link_section = 'income', link_name = 'Elsewhere' WHERE id = ?", lines["income/Salary"])
	app.TestExec(t, "DELETE FROM budget_amounts WHERE line_id = ?", lines["income/Salary"])
	app.MustStatus(t, app.TestRequest(a, "POST", "/bank/fill", url.Values{"year": {"2031"}, "mode": {"overwrite"}}, ""), 303)
	if got := app.TestAmount(t, lines["expense/Food"], 2); got != 132450 {
		t.Errorf("Food in February = %d, want 1324.50 spent", got)
	}
	if got := app.TestAmount(t, lines["expense/Rent"], 2); got != 40000 {
		t.Errorf("Rent in February = %d, want the budget kept without transactions", got)
	}
	if got := app.TestAmount(t, lines["income/Salary"], 2); got != 0 {
		t.Errorf("the linked Salary line was filled with %d", got)
	}
	if page := html.UnescapeString(app.TestRequest(a, "GET", "/bank/fill?year=2031&mode=overwrite", nil, "").Body.String()); !strings.Contains(page, "Nothing to fill") {
		t.Errorf("a second overwrite would still change: %s", page)
	}
	app.MustStatus(t, app.TestRequest(a, "GET", "/bank/fill?year=2040", nil, ""), 404)
}
