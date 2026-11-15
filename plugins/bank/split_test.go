package bank

import (
	"html"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/staal0/ledger/internal/app"
)

func TestBankSplit(t *testing.T) {
	a := app.TestApp(t, false)
	app.TestExec(t, "INSERT INTO settings (name, value) VALUES ('plugin.bank', '1')")
	lines := app.TestSetupYear(t, a, 2031, "expense/Food", "expense/Household")
	food, household := strconv.FormatInt(lines["expense/Food"], 10), strconv.FormatInt(lines["expense/Household"], 10)
	app.MustStatus(t, app.TestRequest(a, "POST", "/bank/accounts", url.Values{"name": {"Budget account"}, "format": {"laanspar"}}, ""), 303)
	previewAndImport(t, a, strconv.FormatInt(app.TestQueryInt(t, "SELECT id FROM bank_accounts"), 10), laanSparSample)
	netto := strconv.FormatInt(app.TestQueryInt(t, "SELECT id FROM bank_transactions WHERE text = 'Netto Butik 123'"), 10)
	split := func(form url.Values) string {
		w := app.TestRequest(a, "POST", "/bank/transactions/"+netto+"/split", form, "")
		if w.Code == 303 {
			return ""
		}
		return html.UnescapeString(w.Body.String())
	}

	// 1,000 to Food; Household gets the rest of the 1,234.50.
	app.MustStatus(t, app.TestRequest(a, "GET", "/bank/transactions/"+netto+"/split", nil, ""), 200)
	if page := split(url.Values{"line_0": {food}, "amount_0": {"1000"}, "line_1": {household}, "amount_1": {""}}); page != "" {
		t.Fatalf("split refused: %s", page)
	}
	if n := app.TestQueryInt(t, "SELECT CAST(SUM(amount) * 100 AS SIGNED) FROM bank_splits WHERE line_id = ?", lines["expense/Household"]); n != -23450 {
		t.Errorf("Household's part is %d, want -234.50", n)
	}
	page := html.UnescapeString(app.TestRequest(a, "GET", "/bank/compare?year=2031&period=2", nil, "").Body.String())
	for _, want := range []string{`>Food</a></td><td class="num">0</td><td class="num">1,000</td>`, `>Household</a></td><td class="num">0</td><td class="num">234.50</td>`} {
		if !strings.Contains(page, want) {
			t.Errorf("comparison lacks %q", want)
		}
	}
	if !strings.Contains(app.TestRequest(a, "GET", "/bank/transactions?year=2031", nil, "").Body.String(), "4 transactions in 2031 are not assigned") {
		t.Error("the split transaction still counts as unassigned")
	}
	all := html.UnescapeString(app.TestRequest(a, "GET", "/bank/transactions?year=2031&show=all", nil, "").Body.String())
	if !strings.Contains(all, "Split: Food -1,000 · Household -234.50") || !strings.Contains(all, "/bank/transactions/"+netto+"/split?back=") {
		t.Error("the full list does not show the parts with a link to edit them")
	}

	// Saving the list and new rules leave it alone.
	app.MustStatus(t, app.TestRequest(a, "POST", "/bank/transactions", url.Values{"year": {"2031"}, "t": {netto}, "line_" + netto: {""}}, ""), 303)
	app.MustStatus(t, app.TestRequest(a, "POST", "/bank/rules", url.Values{"new_pattern": {"netto"}, "new_target": {"expense:Food"}}, ""), 303)
	if n := app.TestQueryInt(t, "SELECT COUNT(*) FROM bank_transactions WHERE id = ? AND split AND rule_id IS NULL", netto); n != 1 {
		t.Error("saving the list or a rule changed the split transaction")
	}

	// Wrong sums, two parts without an amount, and a part without a line are refused.
	for form, want := range map[*url.Values]string{
		{"line_0": {food}, "amount_0": {"1000"}, "line_1": {household}, "amount_1": {"100"}}: "The parts add up to 1,100, but the transaction is 1,234.50.",
		{"line_0": {food}, "amount_0": {""}, "line_1": {household}, "amount_1": {""}}:        "Only one part can be left empty",
		{"line_0": {""}, "amount_0": {"1234.50"}}:                                            "Choose a budget line or Ignore",
	} {
		if page := split(*form); !strings.Contains(page, want) {
			t.Errorf("want %q, got %s", want, page)
		}
	}

	// A part the other way, such as a deposit refund: 50 + -5 is the 45 spent.
	bager := strconv.FormatInt(app.TestQueryInt(t, "SELECT MIN(id) FROM bank_transactions WHERE text = 'Bager Hansen'"), 10)
	app.MustStatus(t, app.TestRequest(a, "POST", "/bank/transactions/"+bager+"/split",
		url.Values{"line_0": {food}, "amount_0": {"50"}, "line_1": {"ignore"}, "amount_1": {"-5"}}, ""), 303)
	if n := app.TestQueryInt(t, "SELECT CAST(amount * 100 AS SIGNED) FROM bank_splits WHERE transaction_id = ? AND ignored", bager); n != 500 {
		t.Errorf("the refund part is %d, want +5.00", n)
	}

	// Undo: whole again, and the netto rule now assigns it.
	app.MustStatus(t, app.TestRequest(a, "POST", "/bank/transactions/"+netto+"/split", url.Values{"action": {"undo"}}, ""), 303)
	if n := app.TestQueryInt(t, "SELECT COUNT(*) FROM bank_splits WHERE transaction_id = ?", netto); n != 0 {
		t.Errorf("%d parts left after undo", n)
	}
	if n := app.TestQueryInt(t, "SELECT COUNT(*) FROM bank_transactions WHERE id = ? AND NOT split AND line_id = ?", netto, food); n != 1 {
		t.Error("after undo the rule did not assign the transaction")
	}
}
