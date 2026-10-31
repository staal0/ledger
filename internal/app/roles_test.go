package app

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestRoleFor(t *testing.T) {
	cfg := Config{AdminGroups: []string{"ledger-admins"}, EditorGroups: []string{"household", "family"}, ViewerGroups: []string{"ledger-viewers"}}
	for _, tt := range []struct {
		groups []string
		role   string
	}{
		{[]string{"ledger-admins", "household"}, RoleAdmin}, // the highest role wins
		{[]string{"family", "ledger-viewers"}, RoleEditor},
		{[]string{"ledger-viewers"}, RoleViewer},
		{[]string{"/ledger-admins"}, RoleAdmin}, // a Keycloak group path
		{[]string{"other"}, ""},
		{nil, ""},
	} {
		if got := cfg.roleFor(tt.groups); got != tt.role {
			t.Errorf("roleFor(%v) = %q, want %q", tt.groups, got, tt.role)
		}
	}
	if got := (Config{}).roleFor(nil); got != RoleAdmin {
		t.Errorf("without role groups everyone is %q, want admin", got)
	}
}

func TestClaimStrings(t *testing.T) {
	var claims map[string]any
	if err := json.Unmarshal([]byte(`{"groups": ["a", "b"], "group": "c", "realm_access": {"roles": ["d"]}}`), &claims); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string][]string{
		"groups":             {"a", "b"},
		"group":              {"c"},
		"realm_access.roles": {"d"},
		"missing":            nil,
		"groups.deeper":      nil,
	} {
		if got := claimStrings(claims, path); !slices.Equal(got, want) {
			t.Errorf("claimStrings(%q) = %v, want %v", path, got, want)
		}
	}
	if got := splitList(" a, ,b ,"); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("splitList = %v", got)
	}
}

func TestRoles(t *testing.T) {
	a := testApp(t, true)
	a.cfg.ViewerGroups = []string{"viewers"} // any role group switches roles on
	exec(t, "INSERT INTO budget_years (budget_id, year) SELECT id, 2030 FROM budgets WHERE is_default")
	exec(t, "INSERT INTO budget_lines (year_id, section, name) SELECT id, 'income', 'Salary' FROM budget_years WHERE year = 2030")
	user := func(name, role string) string {
		cookie := loginAs(t, name, "UTC_TIMESTAMP() + INTERVAL 1 DAY")
		exec(t, "UPDATE users SET role = ? WHERE subject = ?", role, name)
		return cookie
	}
	viewer, editor, admin, before := user("v", RoleViewer), user("e", RoleEditor), user("a", RoleAdmin), user("old", "")

	page := request(a, "GET", "/budget/2030", nil, viewer).Body.String()
	if !strings.Contains(page, "readonly") || strings.Contains(page, `value="save"`) || strings.Contains(page, "/manage") {
		t.Error("a viewer's budget page should be read-only, without Save or the manage link")
	}
	mustStatus(t, request(a, "POST", "/budget/2030", saveForm(t, a, 2030, "save", nil), viewer), 403)
	mustStatus(t, request(a, "POST", "/budget/2031/create", nil, viewer), 403)

	mustStatus(t, request(a, "POST", "/budget/2030", saveForm(t, a, 2030, "save", nil), editor), 303)
	mustStatus(t, request(a, "GET", "/manage", nil, editor), 403)
	if strings.Contains(request(a, "GET", "/budget/2030", nil, editor).Body.String(), "/manage") {
		t.Error("an editor sees the manage link")
	}

	mustStatus(t, request(a, "GET", "/manage", nil, admin), 200)
	if !strings.Contains(request(a, "GET", "/budget/2030", nil, admin).Body.String(), `href="/manage"`) {
		t.Error("an admin does not see the manage link")
	}

	// A session from before roles were set up has to log in again to get a role.
	if w := request(a, "GET", "/budget/2030", nil, before); w.Header().Get("Location") != "/login" {
		t.Errorf("a session without a role: %d %q", w.Code, w.Header().Get("Location"))
	}
	// Without role groups, everyone is an admin, as before roles existed.
	a.cfg.ViewerGroups = nil
	mustStatus(t, request(a, "GET", "/manage", nil, viewer), 200)
}

func TestSaveGroups(t *testing.T) {
	testApp(t, true)
	loginAs(t, "alice", "UTC_TIMESTAMP() + INTERVAL 1 DAY")
	id := queryInt(t, "SELECT id FROM users WHERE subject = 'alice'")
	for _, groups := range [][]string{{"household", "/ledger-admins", "household"}, {"family"}} {
		if err := saveGroups(t.Context(), testDB, id, groups); err != nil {
			t.Fatal(err)
		}
	}
	// The last login's groups replace the earlier ones.
	if n := queryInt(t, "SELECT COUNT(*) FROM user_groups WHERE user_id = ? AND name = 'family'", id); n != 1 {
		t.Error("the groups of the last login were not saved")
	}
	if n := queryInt(t, "SELECT COUNT(*) FROM user_groups WHERE user_id = ?", id); n != 1 {
		t.Errorf("%d groups saved, want 1", n)
	}
}
