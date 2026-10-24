package main

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
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
	type yearInfo struct{ Year, Lines int }
	var years []yearInfo
	rows, err := a.db.QueryContext(ctx, `SELECT b.year, COUNT(l.id) FROM budgets b
		LEFT JOIN budget_lines l ON l.budget_id = b.id GROUP BY b.year ORDER BY b.year DESC`)
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

	year := 0
	if len(years) > 0 {
		year = years[0].Year
	}
	if y, err := strconv.Atoi(r.FormValue("year")); err == nil {
		year = y
	}
	changes, err := a.changes(ctx, year, L)
	if err != nil {
		a.fail(w, err)
		return
	}
	keep, err := historyKeep(ctx, a.db)
	if err != nil {
		a.fail(w, err)
		return
	}
	a.render(w, r, "manage", map[string]any{
		"Title":   L.T("nav.manage"),
		"Years":   years,
		"Year":    year,
		"Changes": changes,
		"Limited": len(changes) == historyShown,
		"Shown":   historyShown,
		"Keep":    keep,
	})
}

// changes lists the newest recorded changes of year, formatted for display.
func (a *App) changes(ctx context.Context, year int, L *Locale) ([]change, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT h.id, h.changed_at, COALESCE(NULLIF(u.name, ''), u.email, ''),
			l.name, h.field, h.old_value, h.new_value
		FROM budget_history h
		JOIN budget_lines l ON l.id = h.line_id
		JOIN budgets b ON b.id = l.budget_id
		LEFT JOIN users u ON u.id = h.user_id
		WHERE b.year = ? ORDER BY h.id DESC LIMIT ?`, year, historyShown)
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

// previousValues is, per form field of a budget ("n_<line>" or "a_<line>_<month>"),
// the tooltip listing its recorded changes, newest first.
func (a *App) previousValues(ctx context.Context, budget int64, L *Locale) (map[string]string, error) {
	const most = 5
	rows, err := a.db.QueryContext(ctx, `SELECT h.line_id, h.field, h.old_value, h.new_value,
			COALESCE(NULLIF(u.name, ''), u.email, ''), h.changed_at
		FROM budget_history h
		JOIN budget_lines l ON l.id = h.line_id
		LEFT JOIN users u ON u.id = h.user_id
		WHERE l.budget_id = ? ORDER BY h.id DESC`, budget)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
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
		if len(lists[key]) < most {
			lists[key] = append(lists[key], fmt.Sprintf("%s → %s · %s · %s",
				L.historyValue(field, old), L.historyValue(field, new), cmp.Or(who, "–"), when.In(time.Local).Format("2006-01-02 15:04")))
		}
	}
	tips := map[string]string{}
	for key, list := range lists {
		tips[key] = L.T("budget.previous") + "\n" + strings.Join(list, "\n")
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
	if _, err := a.db.ExecContext(r.Context(), "DELETE FROM budgets WHERE year = ?", year); err != nil {
		a.fail(w, err)
		return
	}
	http.Redirect(w, r, "/manage", http.StatusSeeOther)
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
	err = tx.QueryRowContext(ctx, `SELECT h.line_id, h.field, h.old_value, b.year
		FROM budget_history h JOIN budget_lines l ON l.id = h.line_id JOIN budgets b ON b.id = l.budget_id
		WHERE h.id = ? FOR UPDATE`, r.PathValue("id")).Scan(&line, &field, &old, &year)
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
	http.Redirect(w, r, fmt.Sprintf("/manage?year=%d#history", year), http.StatusSeeOther)
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
