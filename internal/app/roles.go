package app

import (
	"net/http"
	"slices"
	"strings"
)

// Roles, lowest first. A user gets the highest role whose group they are in.
const (
	RoleViewer = "viewer" // reads budgets and statistics
	RoleEditor = "editor" // also changes budgets
	RoleAdmin  = "admin"  // also the manage page
)

var roleRank = map[string]int{RoleViewer: 1, RoleEditor: 2, RoleAdmin: 3}

func (u *User) can(role string) bool { return u != nil && roleRank[u.Role] >= roleRank[role] }

func (u *User) CanEdit() bool { return u.can(RoleEditor) }
func (u *User) IsAdmin() bool { return u.can(RoleAdmin) }

// rolesConfigured is false when no role groups are set. Everyone who can log
// in is then an admin, as before roles existed.
func (c Config) rolesConfigured() bool {
	return len(c.AdminGroups)+len(c.EditorGroups)+len(c.ViewerGroups) > 0
}

// roleFor is the role for a user's groups, or "" for no access. Keycloak can
// send groups as paths ("/ledger-admins"), so a leading slash is ignored.
func (c Config) roleFor(groups []string) string {
	if !c.rolesConfigured() {
		return RoleAdmin
	}
	in := func(list []string) bool {
		return slices.ContainsFunc(groups, func(g string) bool { return slices.Contains(list, strings.TrimPrefix(g, "/")) })
	}
	switch {
	case in(c.AdminGroups):
		return RoleAdmin
	case in(c.EditorGroups):
		return RoleEditor
	case in(c.ViewerGroups):
		return RoleViewer
	}
	return ""
}

// claimStrings reads a claim as a list of strings. path may be nested with
// dots, e.g. "realm_access.roles" for Keycloak realm roles.
func claimStrings(claims map[string]any, path string) []string {
	var v any = claims
	for _, k := range strings.Split(path, ".") {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[k]
	}
	switch v := v.(type) {
	case string:
		return []string{v}
	case []any:
		out := []string{}
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// splitList splits a comma-separated setting, dropping blanks.
func splitList(s string) []string {
	var out []string
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// Only lets users with at least role through.
func (a *App) Only(role string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !CurrentUser(r).can(role) {
			http.Error(w, a.Locale(r).T("auth.forbidden"), http.StatusForbidden)
			return
		}
		h(w, r)
	}
}
