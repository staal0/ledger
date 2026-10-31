package main

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func TestHistoryAndRestore(t *testing.T) {
	a := testApp(t, false)
	ids := setupYear(t, a, 2030, "income/Salary")
	salary := ids["income/Salary"]
	save := func(v string) {
		mustStatus(t, request(a, "POST", "/budget/2030", saveForm(t, a, 2030, "save", map[int64]map[int]string{salary: {1: v}}), ""), 303)
	}
	save("100")
	// A first value is not a change: neither the amount in the empty month nor
	// the name that replaced the placeholder gets a mark, though both are recorded.
	if grid := request(a, "GET", "/budget/2030", nil, "").Body.String(); strings.Contains(grid, `class="mark"`) {
		t.Error("a first value is marked as changed")
	}
	save("200")
	if n := queryInt(t, "SELECT COUNT(*) FROM budget_history WHERE line_id = ? AND field = 1", salary); n != 2 {
		t.Fatalf("%d changes recorded, want 2", n)
	}
	// setupYear renamed the placeholder line, so the name has a change too.
	if n := queryInt(t, "SELECT COUNT(*) FROM budget_history WHERE line_id = ? AND field = 0", salary); n != 1 {
		t.Errorf("%d name changes recorded, want 1", n)
	}

	// The budget page shows the earlier values in the field's tooltip, newest first.
	grid := request(a, "GET", "/budget/2030", nil, "").Body.String()
	if !strings.Contains(grid, `class="mark"`) || !strings.Contains(grid, "Previous values:\n100 → 200 · Local") {
		t.Error("the budget page does not show the previous values")
	}

	page := request(a, "GET", "/manage?year=2030", nil, "").Body.String()
	if !strings.Contains(page, "Salary") || !strings.Contains(page, "Restore") {
		t.Error("the manage page does not list the changes")
	}

	// Restoring the newest change (100 → 200) sets the amount back to 100 and is recorded itself.
	newest := queryInt(t, "SELECT MAX(id) FROM budget_history WHERE line_id = ? AND field = 1", salary)
	mustStatus(t, request(a, "POST", fmt.Sprintf("/manage/history/%d/restore", newest), url.Values{}, ""), 303)
	if got := amount(t, salary, 1); got != 10000 {
		t.Errorf("after restore the amount is %d, want 10000", got)
	}
	if n := queryInt(t, "SELECT COUNT(*) FROM budget_history WHERE line_id = ? AND field = 1", salary); n != 3 {
		t.Errorf("%d changes after restore, want 3", n)
	}
	mustStatus(t, request(a, "POST", "/manage/history/999999/restore", url.Values{}, ""), 404)
}

func TestHistoryKeep(t *testing.T) {
	a := testApp(t, false)
	ids := setupYear(t, a, 2030, "income/Salary")
	salary := ids["income/Salary"]
	for _, v := range []string{"1", "2", "3", "4"} {
		mustStatus(t, request(a, "POST", "/budget/2030", saveForm(t, a, 2030, "save", map[int64]map[int]string{salary: {1: v}}), ""), 303)
	}
	// Lowering the number applies at once; later saves keep to it.
	mustStatus(t, request(a, "POST", "/manage/history-keep", url.Values{"keep": {"2"}}, ""), 303)
	count := func() int64 {
		return queryInt(t, "SELECT COUNT(*) FROM budget_history WHERE line_id = ? AND field = 1", salary)
	}
	if n := count(); n != 2 {
		t.Errorf("%d changes kept, want 2", n)
	}
	mustStatus(t, request(a, "POST", "/budget/2030", saveForm(t, a, 2030, "save", map[int64]map[int]string{salary: {1: "5"}}), ""), 303)
	if n := count(); n != 2 {
		t.Errorf("%d changes kept after another save, want 2", n)
	}
	var newest string
	if err := testDB.QueryRow("SELECT new_value FROM budget_history WHERE line_id = ? AND field = 1 ORDER BY id DESC LIMIT 1", salary).Scan(&newest); err != nil || newest != "5" {
		t.Errorf("newest kept change is %q %v, want 5", newest, err)
	}
	mustStatus(t, request(a, "POST", "/manage/history-keep", url.Values{"keep": {"-1"}}, ""), 400)
}

func TestDeleteYear(t *testing.T) {
	a := testApp(t, false)
	setupYear(t, a, 2030, "income/Salary")
	mustStatus(t, request(a, "POST", "/manage/years/2030/delete", url.Values{"confirm": {"2031"}}, ""), 400)
	if n := queryInt(t, "SELECT COUNT(*) FROM budget_years WHERE year = 2030"); n != 1 {
		t.Fatal("a wrong confirmation deleted the year")
	}
	mustStatus(t, request(a, "POST", "/manage/years/2030/delete", url.Values{"confirm": {"2030"}}, ""), 303)
	if n := queryInt(t, "SELECT COUNT(*) FROM budget_years WHERE year = 2030"); n != 0 {
		t.Error("the year was not deleted")
	}
	if n := queryInt(t, "SELECT COUNT(*) FROM budget_history"); n != 0 {
		t.Error("the year's history was not deleted")
	}
}
