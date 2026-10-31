package main

import (
	"html"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestRulePattern(t *testing.T) {
	for text, want := range map[string]string{
		"Visa/Dankort 12.03 HOFOR":       "Visa/Dankort HOFOR",
		"Overførsel til lån 1111222233":  "Overførsel til lån 1111222233",
		"Betaling 01-02-2031 Netto  123": "Betaling Netto 123",
		"12.03":                          "12.03",
	} {
		if got := rulePattern(text); got != want {
			t.Errorf("rulePattern(%q) = %q, want %q", text, got, want)
		}
	}
	rules := []bankRule{{ID: 1, Pattern: "netto"}, {ID: 2, Pattern: "NETTO BUTIK"}, {ID: 3, Pattern: "løn"}}
	if r := matchRule(rules, "Netto Butik 123"); r == nil || r.ID != 2 {
		t.Errorf("matched %+v, want the longest rule", r)
	}
	if r := matchRule(rules, "Bager"); r != nil {
		t.Errorf("matched %+v, want none", r)
	}
	for _, tc := range []struct {
		pattern, text string
		want          bool
	}{
		{"Netto*", "NETTO Butik 456", true},
		{"overførsel*1111222233", "Overførsel til lån 1111222233", true},
		{"1111222233*overførsel", "Overførsel til lån 1111222233", false},
		{"lån*lån", "Overførsel til lån 1111222233", false},
		{"*", "anything", true},
	} {
		if got := patternMatches(tc.text, tc.pattern); got != tc.want {
			t.Errorf("patternMatches(%q, %q) = %v, want %v", tc.text, tc.pattern, got, tc.want)
		}
	}
	if r := matchRule([]bankRule{{ID: 1, Pattern: "overførsel*"}, {ID: 2, Pattern: "*lån 1111222233"}}, "Overførsel til lån 1111222233"); r == nil || r.ID != 2 {
		t.Errorf("matched %+v, want the rule with more fixed text", r)
	}
}

func TestBankAssign(t *testing.T) {
	a := testApp(t, false)
	exec(t, "INSERT INTO settings (name, value) VALUES ('plugin.bank', '1')")
	lines := setupYear(t, a, 2031, "income/Salary", "expense/Food", "expense/Car loan")
	mustStatus(t, request(a, "POST", "/bank/accounts", url.Values{"name": {"Budget account"}, "format": {"laanspar"}}, ""), 303)
	account := strconv.FormatInt(queryInt(t, "SELECT id FROM bank_accounts"), 10)
	previewAndImport(t, a, account, laanSparSample)
	tid := func(text string) string {
		return strconv.FormatInt(queryInt(t, "SELECT MIN(id) FROM bank_transactions WHERE text = ?", text), 10)
	}
	lineOf := func(text string) int64 {
		return queryInt(t, "SELECT MIN(COALESCE(line_id, 0)) FROM bank_transactions WHERE text = ?", text)
	}
	page := html.UnescapeString(request(a, "GET", "/bank/transactions", nil, "").Body.String())
	if !strings.Contains(page, "5 transactions in 2031 are not assigned") {
		t.Errorf("transactions page: %s", page)
	}

	// Assign two by hand and remember them; ignore one without remembering.
	netto, loan, salary := tid("Netto Butik 123"), tid("Overførsel til lån 1111222233"), tid("Løn")
	form := url.Values{"year": {"2031"}, "t": {netto, loan, salary},
		"line_" + netto: {strconv.FormatInt(lines["expense/Food"], 10)}, "remember_" + netto: {"1"},
		"line_" + loan: {strconv.FormatInt(lines["expense/Car loan"], 10)}, "remember_" + loan: {"1"},
		"line_" + salary: {"ignore"}}
	mustStatus(t, request(a, "POST", "/bank/transactions", form, ""), 303)
	if lineOf("Netto Butik 123") != lines["expense/Food"] || lineOf("Overførsel til lån 1111222233") != lines["expense/Car loan"] {
		t.Error("the transactions were not assigned")
	}
	if n := queryInt(t, "SELECT COUNT(*) FROM bank_transactions WHERE ignored"); n != 1 {
		t.Errorf("%d ignored, want 1", n)
	}
	if n := queryInt(t, "SELECT COUNT(*) FROM bank_rules"); n != 2 {
		t.Errorf("%d rules learned, want 2", n)
	}
	if n := queryInt(t, "SELECT COUNT(*) FROM bank_transactions WHERE rule_id IS NOT NULL"); n != 0 {
		t.Errorf("%d transactions marked as by rule after assigning by hand", n)
	}

	// A line of another year is refused.
	setupYear(t, a, 2030, "expense/Food")
	other := lineIDs(t, 2030)["expense/Food"]
	bager := tid("Bager Hansen")
	mustStatus(t, request(a, "POST", "/bank/transactions", url.Values{"t": {bager}, "line_" + bager: {strconv.FormatInt(other, 10)}}, ""), 400)

	// The loan rule shortened to the loan number; a new file then assigns by the rules.
	rule := strconv.FormatInt(queryInt(t, "SELECT id FROM bank_rules WHERE pattern LIKE 'Overf%'"), 10)
	mustStatus(t, request(a, "POST", "/bank/rules", url.Values{"pattern_" + rule: {"1111222233"}, "target_" + rule: {"expense:Car loan"}}, ""), 303)
	later := "02-03-2031;Netto Butik 123;-80,00;DKK;\r\n" +
		"01-03-2031;Afdrag 1111222233 marts;-2.500,00;DKK;\r\n"
	previewAndImport(t, a, account, later)
	if lineOf("Afdrag 1111222233 marts") != lines["expense/Car loan"] {
		t.Error("the shortened rule did not assign the new loan payment")
	}
	if n := queryInt(t, "SELECT COUNT(*) FROM bank_transactions WHERE rule_id IS NOT NULL"); n != 2 {
		t.Errorf("%d transactions assigned by rule after the import, want 2", n)
	}

	// Changing one by hand takes it from the rule; deleting the rule unassigns the rest.
	payment := tid("Afdrag 1111222233 marts")
	mustStatus(t, request(a, "POST", "/bank/transactions", url.Values{"year": {"2031"}, "show": {"all"}, "t": {payment},
		"line_" + payment: {strconv.FormatInt(lines["expense/Food"], 10)}}, ""), 303)
	nettoRule := strconv.FormatInt(queryInt(t, "SELECT id FROM bank_rules WHERE pattern = 'Netto Butik 123'"), 10)
	mustStatus(t, request(a, "POST", "/bank/rules", url.Values{"action": {"delete:" + nettoRule}}, ""), 303)
	if n := queryInt(t, "SELECT COUNT(*) FROM bank_transactions WHERE text = 'Netto Butik 123' AND line_id IS NULL"); n != 1 {
		t.Errorf("%d Netto rows unassigned after deleting its rule, want the one it assigned", n)
	}
	if lineOf("Afdrag 1111222233 marts") != lines["expense/Food"] {
		t.Error("the row changed by hand lost its line when rules changed")
	}

	// A new rule needs a line, and two rules cannot share a text.
	w := request(a, "POST", "/bank/rules", url.Values{"new_pattern": {"bager"}}, "")
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "error=target") {
		t.Errorf("new rule without a line went to %q", loc)
	}
	w = request(a, "POST", "/bank/rules", url.Values{"new_pattern": {"1111222233"}, "new_target": {"ignore"}}, "")
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "error=duplicate") {
		t.Errorf("duplicate rule went to %q", loc)
	}
	mustStatus(t, request(a, "POST", "/bank/rules", url.Values{"new_pattern": {"bager"}, "new_target": {"ignore"}}, ""), 303)
	if n := queryInt(t, "SELECT COUNT(*) FROM bank_transactions WHERE text = 'Bager Hansen' AND ignored"); n != 2 {
		t.Errorf("%d Bager rows ignored by the new rule, want 2", n)
	}
	page = html.UnescapeString(request(a, "GET", "/bank/rules", nil, "").Body.String())
	for _, want := range []string{`value="1111222233"`, `value="bager"`, "Car loan", "Rules"} {
		if !strings.Contains(page, want) {
			t.Errorf("rules page lacks %q", want)
		}
	}
}
