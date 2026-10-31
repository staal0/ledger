package app

import (
	"net/url"
	"strings"
	"testing"
)

// loginAs creates a user with a session and returns the cookie value.
func loginAs(t *testing.T, subject, expires string) string {
	t.Helper()
	exec(t, "INSERT INTO users (subject, name, email) VALUES (?, ?, ?)", subject, "Test "+subject, subject+"@example.com")
	token := "token-" + subject
	exec(t, "INSERT INTO sessions (token_hash, user_id, expires_at) SELECT ?, id, "+expires+" FROM users WHERE subject = ?",
		hashToken(token), subject)
	return token
}

func TestSessions(t *testing.T) {
	a := testApp(t, true)
	valid := loginAs(t, "alice", "UTC_TIMESTAMP() + INTERVAL 1 DAY")
	expired := loginAs(t, "bob", "UTC_TIMESTAMP() - INTERVAL 1 MINUTE")

	for _, tt := range []struct {
		name, cookie string
		status       int
	}{
		{"no cookie", "", 302},
		{"unknown cookie", "made-up", 302},
		{"expired session", expired, 302},
		{"valid session", valid, 302}, // "/" redirects on to the budget
	} {
		w := request(a, "GET", "/", nil, tt.cookie)
		loc := w.Header().Get("Location")
		if w.Code != tt.status || (tt.cookie == valid) == (loc == "/login") {
			t.Errorf("%s: %d → %q", tt.name, w.Code, loc)
		}
	}
	page := request(a, "GET", "/settings", nil, valid).Body.String()
	if !strings.Contains(page, "Test alice") || !strings.Contains(page, "Log out") {
		t.Error("settings page should show the logged-in user and a log out button")
	}

	// Cookies are stored hashed: the raw token is not in the database.
	if n := queryInt(t, "SELECT COUNT(*) FROM sessions WHERE token_hash = ?", valid); n != 0 {
		t.Error("a session token is stored in plain text")
	}

	// Logging out deletes the session, so the old cookie no longer works.
	mustStatus(t, request(a, "POST", "/logout", url.Values{}, valid), 303)
	if w := request(a, "GET", "/settings", nil, valid); w.Header().Get("Location") != "/login" {
		t.Errorf("after logout the session still works: %d", w.Code)
	}
}

func TestLocalModeNeedsNoLogin(t *testing.T) {
	a := testApp(t, false)
	w := request(a, "GET", "/settings", nil, "")
	mustStatus(t, w, 200)
	if strings.Contains(w.Body.String(), "Log out") {
		t.Error("local mode should not offer to log out")
	}
	if loc := request(a, "GET", "/login", nil, "").Header().Get("Location"); loc != "/" {
		t.Errorf("/login in local mode redirects to %q, want /", loc)
	}
}

func TestSettings(t *testing.T) {
	a := testApp(t, true)
	alice := loginAs(t, "alice", "UTC_TIMESTAMP() + INTERVAL 1 DAY")
	bob := loginAs(t, "bob", "UTC_TIMESTAMP() + INTERVAL 1 DAY")

	mustStatus(t, request(a, "POST", "/settings", url.Values{"lang": {"da"}, "theme": {"dark"}, "number_format": {"comma"}}, alice), 303)
	page := request(a, "GET", "/budget/2030", nil, alice).Body.String()
	for _, want := range []string{`<html lang="da" data-theme="dark">`, `data-decimal=","`, "Indstillinger"} {
		if !strings.Contains(page, want) {
			t.Errorf("alice's page lacks %q", want)
		}
	}
	if page := request(a, "GET", "/settings", nil, alice).Body.String(); !strings.Contains(page, "Version dev") {
		t.Error("settings page lacks the version")
	}
	// Settings are per user.
	if page := request(a, "GET", "/budget/2030", nil, bob).Body.String(); !strings.Contains(page, `<html lang="en">`) {
		t.Error("bob got alice's settings")
	}

	// The settings form always posts, so a missing checkbox means off.
	if page := request(a, "GET", "/settings", nil, bob).Body.String(); !strings.Contains(page, `name="stats_changes" value="1" checked`) {
		t.Error("changes in the statistics tables are not on by default")
	}
	mustStatus(t, request(a, "POST", "/settings", url.Values{"lang": {"da"}, "theme": {"dark"}, "number_format": {"comma"}, "stats_month_changes": {"1"}, "stats_years": {"3"}}, alice), 303)
	var changes, monthChanges bool
	var years int
	if err := testDB.QueryRow("SELECT stats_changes, stats_month_changes, stats_years FROM users WHERE subject = 'alice'").Scan(&changes, &monthChanges, &years); err != nil || changes || !monthChanges || years != 3 {
		t.Errorf("after saving: stats_changes %v, stats_month_changes %v, stats_years %d, %v", changes, monthChanges, years, err)
	}
	mustStatus(t, request(a, "POST", "/settings", url.Values{"lang": {"da"}, "theme": {"dark"}, "number_format": {"comma"}, "stats_years": {"0"}}, alice), 303)
	if n := queryInt(t, "SELECT stats_years FROM users WHERE subject = 'alice'"); n != 3 {
		t.Errorf("0 years was saved as %d", n)
	}

	// Unknown values are refused as a whole.
	mustStatus(t, request(a, "POST", "/settings", url.Values{"lang": {"xx"}, "theme": {"pink"}, "number_format": {"comma"}}, alice), 303)
	var lang, theme string
	if err := testDB.QueryRow("SELECT lang, theme FROM users WHERE subject = 'alice'").Scan(&lang, &theme); err != nil || lang != "da" || theme != "dark" {
		t.Errorf("after invalid settings: %q %q %v", lang, theme, err)
	}

	// The header toggle only changes the theme and goes back to the page.
	w := request(a, "POST", "/theme", url.Values{"theme": {"light"}}, alice)
	mustStatus(t, w, 303)
	if loc := w.Header().Get("Location"); loc != "/" {
		t.Errorf("theme toggle without referer goes to %q", loc)
	}
	if err := testDB.QueryRow("SELECT theme FROM users WHERE subject = 'alice'").Scan(&theme); err != nil || theme != "light" {
		t.Errorf("theme after toggle = %q %v", theme, err)
	}
}
