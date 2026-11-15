package bank

import (
	"net/url"
	"strconv"
	"testing"

	"github.com/staal0/ledger/internal/app"
)

func TestBankDefaultLine(t *testing.T) {
	a := app.TestApp(t, false)
	app.TestExec(t, "INSERT INTO settings (name, value) VALUES ('plugin.bank', '1')")
	lines := app.TestSetupYear(t, a, 2031, "allocation/Food account", "expense/Household")
	app.MustStatus(t, app.TestRequest(a, "POST", "/bank/accounts", url.Values{"name": {"Food account"}, "format": {"laanspar"}}, ""), 303)
	account := strconv.FormatInt(app.TestQueryInt(t, "SELECT id FROM bank_accounts"), 10)
	app.MustStatus(t, app.TestRequest(a, "POST", "/bank/rules", url.Values{"new_pattern": {"overførsel"}, "new_target": {"ignore"}}, ""), 303)
	previewAndImport(t, a, account, laanSparSample)
	setDefault := func(target string) {
		app.MustStatus(t, app.TestRequest(a, "POST", "/bank/accounts/"+account,
			url.Values{"name": {"Food account"}, "format": {"laanspar"}, "default": {target}}, ""), 303)
	}
	count := func(q string, args ...any) int64 { return app.TestQueryInt(t, q, args...) }

	// Everything no rule takes goes to the default line; the transfer stays ignored.
	setDefault("allocation:Food account")
	if n := count("SELECT COUNT(*) FROM bank_transactions WHERE by_default AND line_id = ?", lines["allocation/Food account"]); n != 4 {
		t.Errorf("%d transactions on the default line, want 4", n)
	}
	if n := count("SELECT COUNT(*) FROM bank_transactions WHERE ignored AND rule_id IS NOT NULL"); n != 1 {
		t.Errorf("%d transfers ignored by the rule, want 1", n)
	}

	// A new rule takes over from the default; a choice by hand stays.
	netto := strconv.FormatInt(count("SELECT id FROM bank_transactions WHERE text = 'Netto Butik 123'"), 10)
	app.MustStatus(t, app.TestRequest(a, "POST", "/bank/transactions", url.Values{"year": {"2031"}, "show": {"all"}, "t": {netto},
		"line_" + netto: {strconv.FormatInt(lines["expense/Household"], 10)}}, ""), 303)
	app.MustStatus(t, app.TestRequest(a, "POST", "/bank/rules", url.Values{"new_pattern": {"bager"}, "new_target": {"expense:Household"}}, ""), 303)
	if n := count("SELECT COUNT(*) FROM bank_transactions WHERE text = 'Bager Hansen' AND rule_id IS NOT NULL AND NOT by_default"); n != 2 {
		t.Errorf("%d bakery rows taken by the new rule, want 2", n)
	}

	// Without a default, what it assigned is unassigned again; the rest stays.
	setDefault("")
	if n := count("SELECT COUNT(*) FROM bank_transactions WHERE by_default"); n != 0 {
		t.Errorf("%d still by default after removing it", n)
	}
	if n := count("SELECT COUNT(*) FROM bank_transactions WHERE text = 'Løn' AND line_id IS NULL"); n != 1 {
		t.Error("the row the default assigned is not unassigned again")
	}
	if n := count("SELECT COUNT(*) FROM bank_transactions WHERE id = ? AND line_id = ?", netto, lines["expense/Household"]); n != 1 {
		t.Error("the choice by hand changed")
	}

	// A new import is assigned by the default at once.
	setDefault("allocation:Food account")
	previewAndImport(t, a, account, "02-03-2031;Grocer Main Street;-80,00;DKK;\r\n")
	if n := count("SELECT COUNT(*) FROM bank_transactions WHERE text = 'Grocer Main Street' AND by_default"); n != 1 {
		t.Error("the new import was not assigned by the default")
	}
	app.MustStatus(t, app.TestRequest(a, "POST", "/bank/accounts/"+account, url.Values{"name": {"x"}, "format": {"laanspar"}, "default": {"ignore"}}, ""), 400)
}
