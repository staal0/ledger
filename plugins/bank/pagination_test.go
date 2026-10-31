package bank

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/staal0/ledger/internal/app"
)

func TestPagination(t *testing.T) {
	a := app.TestApp(t, false)
	app.TestExec(t, "INSERT INTO settings (name, value) VALUES ('plugin.bank', '1')")
	lines := app.TestSetupYear(t, a, 2031, "expense/Food")
	app.MustStatus(t, app.TestRequest(a, "POST", "/bank/accounts", url.Values{"name": {"Budget account"}, "format": {"laanspar"}}, ""), 303)
	account := app.TestQueryInt(t, "SELECT id FROM bank_accounts")

	// 60 imports, the last with 130 transactions.
	for i := 0; i < 60; i++ {
		app.TestExec(t, "INSERT INTO bank_imports (account_id, imported_at, added, skipped) VALUES (?, NOW(), 1, 0)", account)
	}
	imp := app.TestQueryInt(t, "SELECT MAX(id) FROM bank_imports")
	for i := 0; i < 130; i++ {
		app.TestExec(t, "INSERT INTO bank_transactions (account_id, import_id, booked, text, amount, seq) VALUES (?, ?, ?, ?, -10, 1)",
			account, imp, fmt.Sprintf("2031-01-%02d", i%28+1), fmt.Sprintf("Shop %d", i))
	}
	count := func(path, s string) int {
		return strings.Count(app.TestRequest(a, "GET", path, nil, "").Body.String(), s)
	}

	if n := count("/bank", "/bank/imports/"); n != 25 {
		t.Errorf("%d imports on the first page, want 25", n)
	}
	if n := count("/bank?page=3", "/bank/imports/"); n != 10 {
		t.Errorf("%d imports on the third page, want 10", n)
	}
	if n := count("/bank/transactions?year=2031", `name="t"`); n != 25 {
		t.Errorf("%d transactions on the first page, want 25", n)
	}
	page2 := app.TestRequest(a, "GET", "/bank/transactions?year=2031&size=100&page=2", nil, "").Body.String()
	if n := strings.Count(page2, `name="t"`); n != 30 || !strings.Contains(page2, "Page 2 of 2") || !strings.Contains(page2, `<option selected>100</option>`) {
		t.Errorf("%d transactions on the second page of 100, want 30 with the pager and size", n)
	}

	// The size menu has a form of its own, not inside the list's save form.
	if save := page2[strings.Index(page2, `method="post" action="/bank/transactions"`):]; strings.Index(save, `class="pager-size"`) < strings.Index(save, "</form>") {
		t.Error("the size menu is inside the save form")
	}

	// Saving on page 2 returns to page 2 at the same size.
	id := strconv.FormatInt(app.TestQueryInt(t, "SELECT MIN(id) FROM bank_transactions"), 10)
	w := app.TestRequest(a, "POST", "/bank/transactions", url.Values{"year": {"2031"}, "page": {"2"}, "size": {"100"}, "t": {id},
		"line_" + id: {strconv.FormatInt(lines["expense/Food"], 10)}}, "")
	app.MustStatus(t, w, 303)
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "page=2") || !strings.Contains(loc, "size=100") {
		t.Errorf("saving page 2 went to %q", loc)
	}
	// With one assigned, the unassigned list is 129 long; all 130 with show=all.
	if !strings.Contains(app.TestRequest(a, "GET", "/bank/transactions?year=2031&show=all&page=6", nil, "").Body.String(), "Page 6 of 6") {
		t.Error("show=all is not paged")
	}
	if n := count("/bank/transactions?year=2031&size=50&page=3", `name="t"`); n != 29 {
		t.Errorf("%d unassigned transactions on the third page of 50, want 29", n)
	}
}
