package main

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func TestLinkedLines(t *testing.T) {
	a := testApp(t, false) // local mode: an admin, who sees every budget
	exec(t, "INSERT INTO budgets (name, slug) VALUES ('Dennis', 'dennis')")
	household := setupYear(t, a, 2030, "income/Dennis")["income/Dennis"]
	mustStatus(t, request(a, "POST", "/budget/dennis/2030/create", url.Values{}, ""), 303)
	exec(t, "INSERT INTO budget_lines (year_id, section, name) SELECT y.id, 'allocation', 'Joint account' FROM budget_years y JOIN budgets b ON b.id = y.budget_id WHERE b.slug = 'dennis'")
	source := queryInt(t, "SELECT l.id FROM budget_lines l JOIN budget_years y ON y.id = l.year_id JOIN budgets b ON b.id = y.budget_id WHERE b.slug = 'dennis' AND l.name = 'Joint account'")
	dennisBudget := queryInt(t, "SELECT id FROM budgets WHERE slug = 'dennis'")
	saveDennis := func(year int, amounts map[int64]map[int]string) {
		t.Helper()
		b, err := a.loadBudget(t.Context(), dennisBudget, year)
		if err != nil || b == nil {
			t.Fatal(err)
		}
		f := url.Values{"action": {"save"}, "original": {originalValues(b)}}
		for _, lines := range b.Lines {
			for _, l := range lines {
				f.Set(fmt.Sprintf("n_%d", l.ID), l.Name)
				for m, v := range amounts[l.ID] {
					f.Set(fmt.Sprintf("a_%d_%d", l.ID, m), v)
				}
			}
		}
		mustStatus(t, request(a, "POST", fmt.Sprintf("/budget/dennis/%d", year), f, ""), 303)
	}
	saveDennis(2030, map[int64]map[int]string{source: {1: "15000", 2: "15000"}})

	// The chooser lists Dennis' lines; linking copies the amounts.
	if loc := request(a, "POST", "/budget/2030", saveForm(t, a, 2030, fmt.Sprintf("link:%d", household), nil), "").Header().Get("Location"); loc != fmt.Sprintf("/budget/2030?link=%d", household) {
		t.Fatalf("link action went to %q", loc)
	}
	page := request(a, "GET", fmt.Sprintf("/budget/2030?link=%d", household), nil, "").Body.String()
	value := fmt.Sprintf("%d/allocation/Joint account", dennisBudget)
	if !strings.Contains(page, `value="`+value+`"`) {
		t.Fatal("the link chooser does not offer Dennis' line")
	}
	mustStatus(t, request(a, "POST", "/budget/2030", url.Values{"action": {fmt.Sprintf("setlink:%d", household)}, "source": {value}}, ""), 303)
	if amount(t, household, 1) != 1500000 || amount(t, household, 2) != 1500000 {
		t.Fatalf("linking did not copy the amounts: %d %d", amount(t, household, 1), amount(t, household, 2))
	}
	if page := request(a, "GET", "/budget/2030", nil, ""); !strings.Contains(page.Body.String(), `class="linkmark" title="Linked from Dennis › Allocation › Joint account`) {
		t.Error("the budget page does not show the link")
	}

	// Changing the source updates the linked line; changes made on the linked line are ignored.
	saveDennis(2030, map[int64]map[int]string{source: {1: "16000"}})
	if amount(t, household, 1) != 1600000 {
		t.Errorf("after changing the source the linked amount is %d", amount(t, household, 1))
	}
	mustStatus(t, request(a, "POST", "/budget/2030", saveForm(t, a, 2030, "save", map[int64]map[int]string{household: {1: "1"}}), ""), 303)
	if amount(t, household, 1) != 1600000 {
		t.Error("an amount typed on the linked line was saved")
	}

	// Copying the household's year first keeps the link; creating Dennis' year then fills it.
	mustStatus(t, request(a, "POST", "/budget/2031/create", url.Values{"from": {"2030"}}, ""), 303)
	household31 := lineIDs(t, 2031)["income/Dennis"]
	if n := queryInt(t, "SELECT COUNT(*) FROM budget_lines WHERE id = ? AND link_name = 'Joint account'", household31); n != 1 {
		t.Fatal("copying the year dropped the link")
	}
	mustStatus(t, request(a, "POST", "/budget/dennis/2031/create", url.Values{"from": {"2030"}, "amounts": {"1"}}, ""), 303)
	if amount(t, household31, 1) != 1600000 {
		t.Errorf("creating the source year did not fill the linked line: %d", amount(t, household31, 1))
	}

	// Renaming the source breaks the link; the last amounts stay.
	exec(t, "UPDATE budget_lines SET name = 'Shared account' WHERE id = ?", source)
	saveDennis(2030, map[int64]map[int]string{source: {1: "17000"}})
	if amount(t, household, 1) != 1600000 {
		t.Error("a broken link changed its amounts")
	}
	if page := request(a, "GET", "/budget/2030", nil, "").Body.String(); !strings.Contains(page, "Link broken") {
		t.Error("the broken link is not shown")
	}

	// The manage page lists the link, broken.
	if page := request(a, "GET", "/manage", nil, "").Body.String(); !strings.Contains(page, "Dennis › Allocation › Joint account") || !strings.Contains(page, ">Broken<") {
		t.Error("the manage page does not list the broken link")
	}

	// Unlinking makes the line editable again, with the amounts it had.
	mustStatus(t, request(a, "POST", fmt.Sprintf("/manage/links/%d/unlink", household), url.Values{}, ""), 303)
	mustStatus(t, request(a, "POST", "/budget/2030", saveForm(t, a, 2030, "save", map[int64]map[int]string{household: {1: "1"}}), ""), 303)
	if amount(t, household, 1) != 100 {
		t.Error("an unlinked line is not editable")
	}

	// Unlinking on the budget page too.
	mustStatus(t, request(a, "POST", "/budget/2031", url.Values{"action": {fmt.Sprintf("unlink:%d", household31)}}, ""), 303)
	if n := queryInt(t, "SELECT COUNT(*) FROM budget_lines WHERE id = ? AND link_budget_id IS NULL", household31); n != 1 {
		t.Error("unlinking on the budget page did not remove the link")
	}

	// Not allowed: a line of the same budget, a line that is linked itself, an unknown line.
	other := setupYear(t, a, 2032, "expense/Rent", "income/Other")
	for _, v := range []string{
		fmt.Sprintf("%d/expense/Rent", queryInt(t, "SELECT id FROM budgets WHERE is_default")),
		fmt.Sprintf("%d/allocation/Nope", dennisBudget),
	} {
		f := url.Values{"action": {fmt.Sprintf("setlink:%d", other["income/Other"])}, "source": {v}}
		mustStatus(t, request(a, "POST", "/budget/2032", f, ""), 400)
	}
}
