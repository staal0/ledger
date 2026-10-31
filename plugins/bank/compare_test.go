package bank

import (
	"html"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/staal0/ledger/internal/app"
)

func TestBankCompare(t *testing.T) {
	a := app.TestApp(t, false)
	app.TestExec(t, "INSERT INTO settings (name, value) VALUES ('plugin.bank', '1')")
	lines := app.TestSetupYear(t, a, 2031, "income/Salary", "expense/Food")
	form := app.TestSaveForm(t, a, 2031, "save", map[int64]map[int]string{lines["income/Salary"]: {2: "30000", 3: "30000"}, lines["expense/Food"]: {2: "1000", 3: "1000"}})
	app.MustStatus(t, app.TestRequest(a, "POST", "/budget/2031", form, ""), 303)
	app.MustStatus(t, app.TestRequest(a, "POST", "/bank/accounts", url.Values{"name": {"Budget account"}, "format": {"laanspar"}}, ""), 303)
	account := strconv.FormatInt(app.TestQueryInt(t, "SELECT id FROM bank_accounts"), 10)
	previewAndImport(t, a, account, laanSparSample)

	// Food gets the shop and the two bakery rows, Salary the pay; the loan stays unassigned.
	assign := url.Values{"year": {"2031"}}
	for text, line := range map[string]string{"Netto Butik 123": "expense/Food", "Bager Hansen": "expense/Food", "Løn": "income/Salary"} {
		for _, id := range app.TestQueryInts(t, "SELECT id FROM bank_transactions WHERE text = ?", text) {
			s := strconv.FormatInt(id, 10)
			assign.Add("t", s)
			assign.Set("line_"+s, strconv.FormatInt(lines[line], 10))
		}
	}
	app.MustStatus(t, app.TestRequest(a, "POST", "/bank/transactions", assign, ""), 303)

	page := html.UnescapeString(app.TestRequest(a, "GET", "/bank/compare?year=2031&period=2", nil, "").Body.String())
	for _, want := range []string{
		`<td>Food</td><td class="num">1,000</td><td class="num">1,324.50</td><td class="num neg">-324.50</td>`,
		`<td>Salary</td><td class="num">30,000</td><td class="num">31,000</td><td class="num pos">1,000</td>`,
		"1 transactions in the period, -2,500 in all, are not assigned",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("February lacks %q", want)
		}
	}
	// By default only the months with transactions are compared: February, not March.
	page = html.UnescapeString(app.TestRequest(a, "GET", "/bank/compare?year=2031", nil, "").Body.String())
	for _, want := range []string{"Compared: Feb<", `<td>Food</td><td class="num">1,000</td><td class="num">1,324.50</td>`} {
		if !strings.Contains(page, want) {
			t.Errorf("imported months lack %q", want)
		}
	}
	// The whole year sums both months of budget; March has no transactions.
	page = html.UnescapeString(app.TestRequest(a, "GET", "/bank/compare?year=2031&period=year", nil, "").Body.String())
	if !strings.Contains(page, `<td>Food</td><td class="num">2,000</td><td class="num">1,324.50</td><td class="num pos">675.50</td>`) {
		t.Errorf("the whole year: %s", page)
	}
	// Ignored transactions are not counted.
	loan := strconv.FormatInt(app.TestQueryInt(t, "SELECT id FROM bank_transactions WHERE text LIKE 'Overf%'"), 10)
	app.MustStatus(t, app.TestRequest(a, "POST", "/bank/transactions", url.Values{"year": {"2031"}, "t": {loan}, "line_" + loan: {"ignore"}}, ""), 303)
	if page := app.TestRequest(a, "GET", "/bank/compare?year=2031&period=2", nil, "").Body.String(); strings.Contains(page, "not assigned") {
		t.Error("an ignored transaction is still counted as unassigned")
	}
}
