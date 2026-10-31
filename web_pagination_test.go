package main

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestPagination(t *testing.T) {
	a := testApp(t, false)
	exec(t, "INSERT INTO settings (name, value) VALUES ('plugin.bank', '1')")
	lines := setupYear(t, a, 2031, "expense/Food")
	mustStatus(t, request(a, "POST", "/bank/accounts", url.Values{"name": {"Budget account"}, "format": {"laanspar"}}, ""), 303)
	account := queryInt(t, "SELECT id FROM bank_accounts")

	// 60 imports, the last with 130 transactions.
	for i := 0; i < 60; i++ {
		exec(t, "INSERT INTO bank_imports (account_id, imported_at, added, skipped) VALUES (?, NOW(), 1, 0)", account)
	}
	imp := queryInt(t, "SELECT MAX(id) FROM bank_imports")
	for i := 0; i < 130; i++ {
		exec(t, "INSERT INTO bank_transactions (account_id, import_id, booked, text, amount, seq) VALUES (?, ?, ?, ?, -10, 1)",
			account, imp, fmt.Sprintf("2031-01-%02d", i%28+1), fmt.Sprintf("Shop %d", i))
	}
	count := func(path, s string) int { return strings.Count(request(a, "GET", path, nil, "").Body.String(), s) }

	if n := count("/bank", "/bank/imports/"); n != 25 {
		t.Errorf("%d imports on the first page, want 25", n)
	}
	if n := count("/bank?page=3", "/bank/imports/"); n != 10 {
		t.Errorf("%d imports on the third page, want 10", n)
	}
	if n := count("/bank/transactions?year=2031", `name="t"`); n != 25 {
		t.Errorf("%d transactions on the first page, want 25", n)
	}
	page2 := request(a, "GET", "/bank/transactions?year=2031&size=100&page=2", nil, "").Body.String()
	if n := strings.Count(page2, `name="t"`); n != 30 || !strings.Contains(page2, "Page 2 of 2") || !strings.Contains(page2, `<option selected>100</option>`) {
		t.Errorf("%d transactions on the second page of 100, want 30 with the pager and size", n)
	}

	// The size menu has a form of its own, not inside the list's save form.
	if save := page2[strings.Index(page2, `method="post" action="/bank/transactions"`):]; strings.Index(save, `class="pager-size"`) < strings.Index(save, "</form>") {
		t.Error("the size menu is inside the save form")
	}

	// Saving on page 2 returns to page 2 at the same size.
	id := strconv.FormatInt(queryInt(t, "SELECT MIN(id) FROM bank_transactions"), 10)
	w := request(a, "POST", "/bank/transactions", url.Values{"year": {"2031"}, "page": {"2"}, "size": {"100"}, "t": {id},
		"line_" + id: {strconv.FormatInt(lines["expense/Food"], 10)}}, "")
	mustStatus(t, w, 303)
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "page=2") || !strings.Contains(loc, "size=100") {
		t.Errorf("saving page 2 went to %q", loc)
	}
	// With one assigned, the unassigned list is 129 long; all 130 with show=all.
	if !strings.Contains(request(a, "GET", "/bank/transactions?year=2031&show=all&page=6", nil, "").Body.String(), "Page 6 of 6") {
		t.Error("show=all is not paged")
	}
	if n := count("/bank/transactions?year=2031&size=50&page=3", `name="t"`); n != 29 {
		t.Errorf("%d unassigned transactions on the third page of 50, want 29", n)
	}

	// The manage page pages its history; the year picker keeps every year.
	for i := 0; i < 60; i++ {
		exec(t, "INSERT INTO budget_history (line_id, field, old_value, new_value, changed_at) VALUES (?, 1, '1', '2', NOW())", lines["expense/Food"])
	}
	if n := count("/manage?year=2031", "/manage/history/"); n != 25 {
		t.Errorf("%d changes on the first page, want 25", n)
	}
	if n := count("/manage?year=2031&history_page=3", "/manage/history/"); n != 11 {
		t.Errorf("%d changes on the third page, want the other 11", n)
	}
}
