package main

import (
	"context"
	"net/http"
	"strconv"
	"time"
)

// budgetRef is a named budget as the pages need it.
type budgetRef struct {
	ID        int64
	Name      string
	Slug      string
	IsDefault bool
}

// Base is the path of the budget's years. The default budget keeps the
// addresses from before there were several budgets.
func (b budgetRef) Base() string {
	if b.IsDefault {
		return "/budget"
	}
	return "/budget/" + b.Slug
}

func (b budgetRef) yearURL(year int) string { return b.Base() + "/" + strconv.Itoa(year) }

// visibleBudgets lists the budgets u may see, the default budget first: all
// of them for an admin, else those marked everyone, owned by u, with u as a
// member or shared with one of u's groups.
func (a *App) visibleBudgets(ctx context.Context, u *User) ([]budgetRef, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT b.id, b.name, b.slug, b.is_default FROM budgets b
		WHERE ? OR b.everyone OR b.owner_id = ?
			OR EXISTS (SELECT 1 FROM budget_members m WHERE m.budget_id = b.id AND m.user_id = ?)
			OR EXISTS (SELECT 1 FROM budget_groups g JOIN user_groups ug ON ug.name = g.name
				WHERE g.budget_id = b.id AND ug.user_id = ?)
		ORDER BY b.is_default DESC, b.name`, u.IsAdmin(), u.ID, u.ID, u.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []budgetRef
	for rows.Next() {
		var b budgetRef
		if err := rows.Scan(&b.ID, &b.Name, &b.Slug, &b.IsDefault); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// pickable is the budgets for a picker: none with only one, so a single budget
// looks as before there were several.
func pickable(visible []budgetRef) []budgetRef {
	if len(visible) < 2 {
		return nil
	}
	return visible
}

// budgetFor is the budget a request is about: the path's {slug}, else the
// ?budget= of a form, else the default budget. ok is false when the user may
// not see it, which the handlers answer as not found.
func (a *App) budgetFor(r *http.Request) (b budgetRef, visible []budgetRef, ok bool, err error) {
	visible, err = a.visibleBudgets(r.Context(), currentUser(r))
	if err != nil {
		return b, nil, false, err
	}
	slug := r.PathValue("slug")
	if slug == "" {
		slug = r.FormValue("budget")
	}
	for _, v := range visible {
		if slug == v.Slug || slug == "" && v.IsDefault {
			return v, visible, true, nil
		}
	}
	return b, visible, false, nil
}

// startBudget is the budget opened when none is chosen: the user's start
// budget, else the default budget, else the first the user may see.
func startBudget(u *User, visible []budgetRef) (budgetRef, bool) {
	for _, v := range visible {
		if v.ID == u.StartBudget {
			return v, true
		}
	}
	for _, v := range visible {
		if v.IsDefault {
			return v, true
		}
	}
	if len(visible) > 0 {
		return visible[0], true
	}
	return budgetRef{}, false
}

// chosenBudget is the budget a request names, by the path's {slug} or ?budget=,
// or else the user's start budget.
func (a *App) chosenBudget(r *http.Request) (budgetRef, []budgetRef, bool, error) {
	b, visible, ok, err := a.budgetFor(r)
	if err == nil && r.PathValue("slug") == "" && r.FormValue("budget") == "" {
		b, ok = startBudget(currentUser(r), visible)
	}
	return b, visible, ok, err
}

// budgetIndex goes to ?year=, or this year, of the chosen budget.
func (a *App) budgetIndex(w http.ResponseWriter, r *http.Request) {
	b, _, ok, err := a.chosenBudget(r)
	if err != nil {
		a.fail(w, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	year := time.Now().Year()
	if y, err := strconv.Atoi(r.FormValue("year")); err == nil {
		year = y
	}
	http.Redirect(w, r, b.yearURL(year), http.StatusFound)
}
