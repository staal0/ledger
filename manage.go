package main

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// History holds the old and new value of every change to a line name (field 0)
// or a month's amount (field 1-12). Amounts are in the database format, "" for none.

const (
	historyKeepSetting = "history_keep"
	historyKeepDefault = 10
	historyShown       = 200
)

// dbValue is an amount as stored in budget_history.
func dbValue(m Money) string {
	if m == 0 {
		return ""
	}
	return m.format(".", "")
}

// setField writes a line name or amount and records the change.
func setField(ctx context.Context, tx *sql.Tx, line int64, field int, old, new string, user int64) error {
	var err error
	switch {
	case field == 0:
		_, err = tx.ExecContext(ctx, "UPDATE budget_lines SET name = ? WHERE id = ?", new, line)
	case new == "":
		_, err = tx.ExecContext(ctx, "DELETE FROM budget_amounts WHERE line_id = ? AND month = ?", line, field)
	default:
		_, err = tx.ExecContext(ctx, `INSERT INTO budget_amounts (line_id, month, amount) VALUES (?, ?, ?)
			ON DUPLICATE KEY UPDATE amount = VALUES(amount)`, line, field, new)
	}
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO budget_history (line_id, field, old_value, new_value, user_id, changed_at)
		VALUES (?, ?, ?, ?, ?, UTC_TIMESTAMP())`, line, field, old, new, user)
	return err
}

// historyKeep is how many changes are kept per field.
func historyKeep(ctx context.Context, q querier) (int, error) {
	var v string
	err := q.QueryRowContext(ctx, "SELECT value FROM settings WHERE name = ?", historyKeepSetting).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return historyKeepDefault, nil
	}
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(v)
}

// pruneHistory drops all but the newest keep changes of each field of lines.
func pruneHistory(ctx context.Context, tx *sql.Tx, lines []int64, keep int) error {
	if len(lines) == 0 {
		return nil
	}
	args := []any{}
	for _, l := range lines {
		args = append(args, l)
	}
	args = append(args, keep)
	_, err := tx.ExecContext(ctx, `DELETE h FROM budget_history h JOIN (
		SELECT id, ROW_NUMBER() OVER (PARTITION BY line_id, field ORDER BY id DESC) AS n
		FROM budget_history WHERE line_id IN (?`+strings.Repeat(", ?", len(lines)-1)+`)
	) AS old ON old.id = h.id WHERE old.n > ?`, args...)
	return err
}

type change struct {
	ID       int64
	When     time.Time
	Who      string
	Line     string
	Field    string
	Old, New string
}

func (a *App) managePage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	L := a.locale(r)
	current, visible, ok, err := a.chosenBudget(r)
	if err != nil {
		a.fail(w, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	budget := current.ID
	type yearInfo struct{ Year, Lines int }
	var years []yearInfo
	rows, err := a.db.QueryContext(ctx, `SELECT y.year, COUNT(l.id) FROM budget_years y
		LEFT JOIN budget_lines l ON l.year_id = y.id WHERE y.budget_id = ? GROUP BY y.year ORDER BY y.year DESC`, budget)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var y yearInfo
		if err := rows.Scan(&y.Year, &y.Lines); err != nil {
			a.fail(w, err)
			return
		}
		years = append(years, y)
	}
	if err := rows.Err(); err != nil {
		a.fail(w, err)
		return
	}

	// The current year if it has a budget, else the newest one.
	year := 0
	if len(years) > 0 {
		year = years[0].Year
	}
	for _, y := range years {
		if y.Year == time.Now().Year() {
			year = y.Year
		}
	}
	if y, err := strconv.Atoi(r.FormValue("year")); err == nil {
		year = y
	}
	changes, err := a.changes(ctx, budget, year, L)
	if err != nil {
		a.fail(w, err)
		return
	}
	keep, err := historyKeep(ctx, a.db)
	if err != nil {
		a.fail(w, err)
		return
	}
	all, users, err := a.budgetsAdmin(ctx)
	if err != nil {
		a.fail(w, err)
		return
	}
	links, err := a.allLinks(ctx)
	if err != nil {
		a.fail(w, err)
		return
	}
	type pluginInfo struct {
		ID string
		On bool
	}
	var plugs []pluginInfo
	for _, p := range plugins {
		on, err := a.pluginOn(ctx, p.ID())
		if err != nil {
			a.fail(w, err)
			return
		}
		plugs = append(plugs, pluginInfo{p.ID(), on})
	}
	a.render(w, r, "manage", map[string]any{
		"Title":      L.T("nav.manage"),
		"Current":    current,
		"Budgets":    pickable(visible),
		"Base":       current.Base(),
		"AllBudgets": all,
		"Links":      links,
		"Open":       openBudget(r, current),
		"Users":      users,
		"Years":      years,
		"Year":       year,
		"Changes":    changes,
		"Limited":    len(changes) == historyShown,
		"Shown":      historyShown,
		"Keep":       keep,
		"Plugins":    plugs,
	})
}

// changes lists the newest recorded changes of budget's year, formatted for display.
func (a *App) changes(ctx context.Context, budget int64, year int, L *Locale) ([]change, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT h.id, h.changed_at, COALESCE(NULLIF(u.name, ''), u.email, ''),
			l.name, h.field, h.old_value, h.new_value
		FROM budget_history h
		JOIN budget_lines l ON l.id = h.line_id
		JOIN budget_years y ON y.id = l.year_id
		LEFT JOIN users u ON u.id = h.user_id
		WHERE y.budget_id = ? AND y.year = ? ORDER BY h.id DESC LIMIT ?`, budget, year, historyShown)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []change
	for rows.Next() {
		var c change
		var field int
		if err := rows.Scan(&c.ID, &c.When, &c.Who, &c.Line, &field, &c.Old, &c.New); err != nil {
			return nil, err
		}
		c.When = c.When.In(time.Local)
		c.Field = L.T("manage.name")
		if field > 0 {
			c.Field = L.T(fmt.Sprintf("month.%d", field))
		}
		c.Old, c.New = L.historyValue(field, c.Old), L.historyValue(field, c.New)
		out = append(out, c)
	}
	return out, rows.Err()
}

// historyValue formats a recorded value: a name as is, an amount in the user's format.
func (l *Locale) historyValue(field int, v string) string {
	if field == 0 {
		return v
	}
	m, err := parseMoney(v, ".", "")
	if err != nil || m == 0 {
		return "–"
	}
	return l.Money(m)
}

// previousValues is, per form field of a budget year ("n_<line>" or "a_<line>_<month>"),
// the tooltip listing its recorded changes, newest first.
func (a *App) previousValues(ctx context.Context, year int64, L *Locale) (map[string]string, error) {
	const most = 5
	rows, err := a.db.QueryContext(ctx, `SELECT h.line_id, h.field, h.old_value, h.new_value,
			COALESCE(NULLIF(u.name, ''), u.email, ''), h.changed_at
		FROM budget_history h
		JOIN budget_lines l ON l.id = h.line_id
		LEFT JOIN users u ON u.id = h.user_id
		WHERE l.year_id = ? ORDER BY h.id DESC`, year)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// A field gets a mark once a value in it was replaced. Its first value is
	// not a change: an amount typed into an empty month, or the name given to a
	// line added as "New line" (in any language).
	placeholder := map[string]bool{}
	for _, l := range a.locales {
		placeholder[l.T("budget.new_line")] = true
	}
	changed := map[string]bool{}
	lists := map[string][]string{}
	for rows.Next() {
		var line int64
		var field int
		var old, new, who string
		var when time.Time
		if err := rows.Scan(&line, &field, &old, &new, &who, &when); err != nil {
			return nil, err
		}
		key := fmt.Sprintf("a_%d_%d", line, field)
		if field == 0 {
			key = fmt.Sprintf("n_%d", line)
		}
		if old != "" && (field != 0 || !placeholder[old]) {
			changed[key] = true
		}
		if len(lists[key]) < most {
			lists[key] = append(lists[key], fmt.Sprintf("%s → %s · %s · %s",
				L.historyValue(field, old), L.historyValue(field, new), cmp.Or(who, "–"), when.In(time.Local).Format("2006-01-02 15:04")))
		}
	}
	tips := map[string]string{}
	for key, list := range lists {
		if changed[key] {
			tips[key] = L.T("budget.previous") + "\n" + strings.Join(list, "\n")
		}
	}
	return tips, rows.Err()
}

// manageDeleteYear deletes a year with its lines, amounts and history, once
// the year has been typed to confirm.
func (a *App) manageDeleteYear(w http.ResponseWriter, r *http.Request) {
	year, ok := pathYear(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if r.PostFormValue("confirm") != strconv.Itoa(year) {
		http.Error(w, a.locale(r).T("manage.delete_wrong", year), http.StatusBadRequest)
		return
	}
	current, _, ok, err := a.budgetFor(r)
	if err == nil && ok {
		_, err = a.db.ExecContext(r.Context(), "DELETE FROM budget_years WHERE budget_id = ? AND year = ?", current.ID, year)
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, manageURL(current, 0, "#years"), http.StatusSeeOther)
}

// manageRestore sets a field back to its value before a change. The restore is
// itself recorded, so it can be undone the same way.
func (a *App) manageRestore(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer tx.Rollback()
	var line int64
	var field, year int
	var old string
	var budget budgetRef
	err = tx.QueryRowContext(ctx, `SELECT h.line_id, h.field, h.old_value, y.year, b.id, b.slug, b.is_default
		FROM budget_history h JOIN budget_lines l ON l.id = h.line_id JOIN budget_years y ON y.id = l.year_id
		JOIN budgets b ON b.id = y.budget_id
		WHERE h.id = ? FOR UPDATE`, r.PathValue("id")).Scan(&line, &field, &old, &year, &budget.ID, &budget.Slug, &budget.IsDefault)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	var current string
	if field == 0 {
		err = tx.QueryRowContext(ctx, "SELECT name FROM budget_lines WHERE id = ?", line).Scan(&current)
	} else {
		var m Money
		err = tx.QueryRowContext(ctx, "SELECT amount FROM budget_amounts WHERE line_id = ? AND month = ?", line, field).Scan(&m)
		current = dbValue(m)
		if errors.Is(err, sql.ErrNoRows) {
			err = nil
		}
	}
	if err == nil && current != old {
		err = setField(ctx, tx, line, field, current, old, currentUser(r).ID)
	}
	if err == nil && field > 0 {
		err = syncFromSource(ctx, tx, budget.ID, year)
	}
	var keep int
	if err == nil {
		keep, err = historyKeep(ctx, tx)
	}
	if err == nil {
		err = pruneHistory(ctx, tx, []int64{line}, keep)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	http.Redirect(w, r, manageURL(budget, year, "#history"), http.StatusSeeOther)
}

// manageKeep sets how many changes are kept per field and applies it at once.
func (a *App) manageKeep(w http.ResponseWriter, r *http.Request) {
	keep, err := strconv.Atoi(r.PostFormValue("keep"))
	if err != nil || keep < 0 || keep > 1000 {
		http.Error(w, "invalid number", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO settings (name, value) VALUES (?, ?)
		ON DUPLICATE KEY UPDATE value = VALUES(value)`, historyKeepSetting, strconv.Itoa(keep))
	if err == nil {
		_, err = tx.ExecContext(ctx, `DELETE h FROM budget_history h JOIN (
			SELECT id, ROW_NUMBER() OVER (PARTITION BY line_id, field ORDER BY id DESC) AS n FROM budget_history
		) AS old ON old.id = h.id WHERE old.n > ?`, keep)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	http.Redirect(w, r, "/manage", http.StatusSeeOther)
}

// managePlugins turns each plugin on or off: on when its box is ticked.
func (a *App) managePlugins(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer tx.Rollback()
	for _, p := range plugins {
		on := "0"
		if slices.Contains(r.PostForm["on"], p.ID()) {
			on = "1"
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO settings (name, value) VALUES (?, ?)
			ON DUPLICATE KEY UPDATE value = VALUES(value)`, pluginSetting(p.ID()), on); err != nil {
			break
		}
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	http.Redirect(w, r, "/manage#plugins", http.StatusSeeOther)
}
