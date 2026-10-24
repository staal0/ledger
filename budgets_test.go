package main

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func TestSeveralBudgets(t *testing.T) {
	a := testApp(t, true)
	a.cfg.EditorGroups = []string{"editors"} // roles on, so editors only see what is shared with them
	user := func(name string, groups ...string) (string, int64) {
		cookie := loginAs(t, name, "UTC_TIMESTAMP() + INTERVAL 1 DAY")
		exec(t, "UPDATE users SET role = 'editor' WHERE subject = ?", name)
		id := queryInt(t, "SELECT id FROM users WHERE subject = ?", name)
		if err := saveGroups(t.Context(), testDB, id, groups); err != nil {
			t.Fatal(err)
		}
		return cookie, id
	}
	alice, aliceID := user("alice")
	bob, _ := user("bob", "family")
	carol, _ := user("carol")
	exec(t, "INSERT INTO budgets (name, slug, owner_id) VALUES ('Alice', 'alice', ?)", aliceID)
	exec(t, "INSERT INTO budgets (name, slug) VALUES ('Shared', 'shared')")
	exec(t, "INSERT INTO budget_groups (budget_id, name) SELECT id, 'family' FROM budgets WHERE slug = 'shared'")

	status := func(cookie, method, path string, form url.Values) int {
		return request(a, method, path, form, cookie).Code
	}
	// Who sees what: the owner, a group, and everyone for the default budget.
	for _, tt := range []struct {
		who, cookie, path string
		want              int
	}{
		{"alice", alice, "/budget/alice/2030", 200},
		{"bob", bob, "/budget/alice/2030", 404},
		{"bob", bob, "/budget/shared/2030", 200},
		{"alice", alice, "/budget/shared/2030", 404},
		{"carol", carol, "/budget/2030", 200},
		{"carol", carol, "/budget/nope/2030", 404},
	} {
		if got := status(tt.cookie, "GET", tt.path, nil); got != tt.want {
			t.Errorf("%s GET %s: %d, want %d", tt.who, tt.path, got, tt.want)
		}
	}

	// The picker shows only with more than one budget; the default budget keeps its addresses.
	if page := request(a, "GET", "/budget/2030", nil, carol).Body.String(); strings.Contains(page, `name="budget"`) || !strings.Contains(page, `href="/budget/2029"`) {
		t.Error("with one budget the page should look as before")
	}
	if page := request(a, "GET", "/budget/2030", nil, alice).Body.String(); !strings.Contains(page, `<option value="alice"`) || strings.Contains(page, `value="shared"`) {
		t.Error("alice's picker should list her budget and not bob's")
	}

	// Creating and saving a year in a budget other than the default.
	w := request(a, "POST", "/budget/alice/2030/create", url.Values{}, alice)
	if w.Code != 303 || w.Header().Get("Location") != "/budget/alice/2030" {
		t.Fatalf("create: %d %q", w.Code, w.Header().Get("Location"))
	}
	mustStatus(t, request(a, "POST", "/budget/alice/2030/create", url.Values{}, bob), 404)
	if n := queryInt(t, "SELECT COUNT(*) FROM budget_years y JOIN budgets b ON b.id = y.budget_id WHERE b.slug = 'alice' AND y.year = 2030"); n != 1 {
		t.Fatal("the year was not created in alice's budget")
	}
	if n := queryInt(t, "SELECT COUNT(*) FROM budget_years y JOIN budgets b ON b.id = y.budget_id WHERE b.is_default"); n != 0 {
		t.Error("creating a year in alice's budget touched the default budget")
	}
	exec(t, "INSERT INTO budget_lines (year_id, section, name) SELECT y.id, 'income', 'Salary' FROM budget_years y JOIN budgets b ON b.id = y.budget_id WHERE b.slug = 'alice'")
	line := queryInt(t, "SELECT l.id FROM budget_lines l JOIN budget_years y ON y.id = l.year_id JOIN budgets b ON b.id = y.budget_id WHERE b.slug = 'alice'")
	b, err := a.loadBudget(t.Context(), queryInt(t, "SELECT id FROM budgets WHERE slug = 'alice'"), 2030)
	if err != nil || b == nil {
		t.Fatal(err)
	}
	form := url.Values{"action": {"save"}, "original": {originalValues(b)}, fmt.Sprintf("n_%d", line): {"Salary"}, fmt.Sprintf("a_%d_1", line): {"500"}}
	w = request(a, "POST", "/budget/alice/2030", form, alice)
	if w.Code != 303 || w.Header().Get("Location") != "/budget/alice/2030" {
		t.Fatalf("save: %d %q", w.Code, w.Header().Get("Location"))
	}
	if amount(t, line, 1) != 50000 {
		t.Error("the amount was not saved in alice's budget")
	}
	mustStatus(t, request(a, "POST", "/budget/alice/2030", form, bob), 404)
	mustStatus(t, request(a, "GET", "/budget/alice/2030/export", nil, alice), 200)
	mustStatus(t, request(a, "GET", "/budget/alice/2030/export", nil, bob), 404)
	mustStatus(t, request(a, "GET", "/stats?budget=alice", nil, alice), 200)
	mustStatus(t, request(a, "GET", "/stats?budget=alice", nil, bob), 404)

	// The pickers go to the chosen budget and year.
	if loc := request(a, "GET", "/budget/?budget=alice&year=2031", nil, alice).Header().Get("Location"); loc != "/budget/alice/2031" {
		t.Errorf("picker went to %q", loc)
	}
	// Without access to the default budget, its addresses lead to the first budget the user can see.
	exec(t, "UPDATE budgets SET everyone = FALSE WHERE is_default")
	if loc := request(a, "GET", "/budget/2030", nil, alice).Header().Get("Location"); loc != "/budget/alice/2030" {
		t.Errorf("default budget address for alice went to %q", loc)
	}
	mustStatus(t, request(a, "GET", "/budget/2030", nil, carol), 404)
	exec(t, "UPDATE budgets SET everyone = TRUE WHERE is_default")
}
