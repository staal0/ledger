package app

// Short names for the test support in testsupport.go, as the core's tests use them.

import (
	"net/http/httptest"
	"net/url"
	"testing"
)

func testApp(t *testing.T, login bool) *App { t.Helper(); return TestApp(t, login) }

func exec(t *testing.T, q string, args ...any) { t.Helper(); TestExec(t, q, args...) }

func queryInt(t *testing.T, q string, args ...any) int64 {
	t.Helper()
	return TestQueryInt(t, q, args...)
}

func request(a *App, method, path string, form url.Values, cookie string) *httptest.ResponseRecorder {
	return TestRequest(a, method, path, form, cookie)
}

func mustStatus(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	MustStatus(t, w, want)
}

func lineIDs(t *testing.T, year int) map[string]int64 { t.Helper(); return TestLineIDs(t, year) }

func saveForm(t *testing.T, a *App, year int, action string, amounts map[int64]map[int]string) url.Values {
	t.Helper()
	return TestSaveForm(t, a, year, action, amounts)
}

func loadYear(t *testing.T, a *App, year int) (*BudgetYear, error) {
	t.Helper()
	return testLoadYear(t, a, year)
}
