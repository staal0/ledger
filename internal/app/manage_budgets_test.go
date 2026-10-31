package app

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func TestSlugify(t *testing.T) {
	for name, want := range map[string]string{
		"Fælles ferie": "faelles-ferie",
		"Alex' budget": "alex-budget",
		"  Ø  ":        "oe",
		"2026":         "budget-2026", // only digits would read as a year
		"!!!":          "budget",
	} {
		if got := slugify(name); got != want {
			t.Errorf("slugify(%q) = %q, want %q", name, got, want)
		}
	}
	for s, want := range map[string]bool{"ferie": true, "a-1": true, "2026": false, "Ferie": false, "a--b": false, "-a": false, "": false} {
		if validSlug(s) != want {
			t.Errorf("validSlug(%q) = %v", s, !want)
		}
	}
}

func TestManageBudgets(t *testing.T) {
	a := testApp(t, false) // local mode: an admin
	post := func(path string, form url.Values) *httptestResult {
		w := request(a, "POST", path, form, "")
		return &httptestResult{w.Code, w.Header().Get("Location"), w.Body.String()}
	}

	// Creating: the address comes from the name, the creator owns it, and it is private.
	if r := post("/manage/budgets", url.Values{"name": {"Fælles ferie"}}); r.code != 303 || r.location != "/manage?budget=faelles-ferie#budgets" {
		t.Fatalf("create: %+v", r)
	}
	id := queryInt(t, "SELECT id FROM budgets WHERE slug = 'faelles-ferie' AND NOT everyone AND owner_id IS NOT NULL")
	if id == 0 {
		t.Fatal("the new budget is not private with an owner")
	}
	if r := post("/manage/budgets", url.Values{"name": {"Again"}, "slug": {"faelles-ferie"}}); r.code != 400 {
		t.Errorf("a taken address: %d", r.code)
	}
	if r := post("/manage/budgets", url.Values{"name": {"Year"}, "slug": {"2026"}}); r.code != 400 {
		t.Errorf("an address of only digits: %d", r.code)
	}

	// Editing: name, address, members, groups and everyone.
	exec(t, "INSERT INTO users (subject, name) VALUES ('wife', 'Wife')")
	wife := queryInt(t, "SELECT id FROM users WHERE subject = 'wife'")
	form := url.Values{"name": {"Ferie"}, "slug": {"ferie"}, "owner": {""}, "member": {fmt.Sprint(wife)}, "groups": {"family, /travel"}}
	if r := post(fmt.Sprintf("/manage/budgets/%d", id), form); r.code != 303 {
		t.Fatalf("update: %+v", r)
	}
	if n := queryInt(t, "SELECT COUNT(*) FROM budgets WHERE id = ? AND name = 'Ferie' AND slug = 'ferie' AND owner_id IS NULL AND NOT everyone", id); n != 1 {
		t.Error("name, address or owner not saved")
	}
	if n := queryInt(t, "SELECT COUNT(*) FROM budget_members WHERE budget_id = ? AND user_id = ?", id, wife); n != 1 {
		t.Error("member not saved")
	}
	if n := queryInt(t, "SELECT COUNT(*) FROM budget_groups WHERE budget_id = ? AND name IN ('family', 'travel')", id); n != 2 {
		t.Error("groups not saved")
	}
	form.Set("everyone", "1")
	form.Del("member")
	post(fmt.Sprintf("/manage/budgets/%d", id), form)
	if n := queryInt(t, "SELECT COUNT(*) FROM budget_members WHERE budget_id = ?", id); n != 0 {
		t.Error("an unticked member was kept")
	}
	if r := post("/manage/budgets/999999", form); r.code != 404 {
		t.Errorf("updating a missing budget: %d", r.code)
	}

	// The manage page shows the budgets, and years and history follow the chosen budget.
	mustStatus(t, request(a, "POST", "/budget/ferie/2030/create", url.Values{}, ""), 303)
	page := request(a, "GET", "/manage?budget=ferie", nil, "").Body.String()
	if !strings.Contains(page, ">Ferie<") || !strings.Contains(page, `href="/budget/ferie/2030"`) {
		t.Error("the manage page does not show the chosen budget's years")
	}
	if r := post("/manage/years/2030/delete", url.Values{"budget": {"ferie"}, "confirm": {"2030"}}); r.code != 303 || r.location != "/manage?budget=ferie#years" {
		t.Errorf("deleting a year: %+v", r)
	}
	if n := queryInt(t, "SELECT COUNT(*) FROM budget_years WHERE budget_id = ?", id); n != 0 {
		t.Error("the year was not deleted from the chosen budget")
	}

	// Deleting: only with the name typed, and never the default budget.
	if r := post(fmt.Sprintf("/manage/budgets/%d/delete", id), url.Values{"confirm": {"ferie"}}); r.code != 400 {
		t.Errorf("deleting with a wrong confirmation: %d", r.code)
	}
	if r := post(fmt.Sprintf("/manage/budgets/%d/delete", id), url.Values{"confirm": {"Ferie"}}); r.code != 303 {
		t.Errorf("deleting: %+v", r)
	}
	if n := queryInt(t, "SELECT COUNT(*) FROM budgets WHERE id = ?", id); n != 0 {
		t.Error("the budget was not deleted")
	}
	def := queryInt(t, "SELECT id FROM budgets WHERE is_default")
	if r := post(fmt.Sprintf("/manage/budgets/%d/delete", def), url.Values{"confirm": {"Budget"}}); r.code != 400 {
		t.Errorf("deleting the default budget: %d", r.code)
	}
}

type httptestResult struct {
	code     int
	location string
	body     string
}
