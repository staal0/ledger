package app

import (
	"strings"
	"testing"
)

func TestHistoryPagination(t *testing.T) {
	a := testApp(t, false)
	lines := setupYear(t, a, 2031, "expense/Food")
	count := func(path, s string) int { return strings.Count(request(a, "GET", path, nil, "").Body.String(), s) }

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
