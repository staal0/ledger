package main

import (
	"context"
	"database/sql"
	"net/http"
	"net/url"
	"slices"
	"strconv"
)

// bankFillChange is an amount the fill sets: a line's month to what its
// transactions add up to.
type bankFillChange struct {
	Line     *Line
	Month    int
	Old, New Money
	Update   bool // Old was filled from the bank and is updated without overwrite
}

// bankFill plans filling year b from the bank: for each line and imported
// month with assigned transactions, the amount becomes their sum. Without
// overwrite only empty amounts are set, and those an earlier fill wrote and
// nobody changed since, so a month imported in parts is updated as it fills
// up; kept counts the amounts left alone. Linked lines are skipped, as their
// amounts come from their source.
func bankFill(ctx context.Context, q querier, b *BudgetYear, overwrite bool) (changes []bankFillChange, kept int, err error) {
	imported, err := importedMonths(ctx, q, b.BudgetID, b.Year)
	if err != nil {
		return nil, 0, err
	}
	sums, err := lineActuals(ctx, q, b.BudgetID, b.Year)
	if err != nil {
		return nil, 0, err
	}
	type cell struct {
		line  int64
		month int
	}
	filled := map[cell]Money{}
	rows, err := q.QueryContext(ctx, `SELECT f.line_id, f.month, f.amount FROM bank_filled f
		JOIN budget_lines l ON l.id = f.line_id WHERE l.year_id = ?`, b.ID)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var c cell
		var m Money
		if err := rows.Scan(&c.line, &c.month, &m); err != nil {
			return nil, 0, err
		}
		filled[c] = m
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	for _, s := range sections {
		for _, l := range b.Lines[s] {
			if l.Link != nil {
				continue
			}
			for _, m := range imported {
				sum, has := sums[l.ID][m]
				if !has {
					continue
				}
				c := bankFillChange{Line: l, Month: m, Old: l.Amounts[m], New: sum * actualSign(s)}
				was, fromBank := filled[cell{l.ID, m}]
				c.Update = fromBank && was == c.Old && c.Old != 0
				switch {
				case c.New == c.Old:
				case c.Old != 0 && !c.Update && !overwrite:
					kept++
				default:
					changes = append(changes, c)
				}
			}
		}
	}
	return changes, kept, nil
}

// bankFillYear is the year a fill request is for, when the budget has it.
func bankFillYear(r *http.Request, years []int) (int, bool) {
	y, err := strconv.Atoi(r.FormValue("year"))
	return y, err == nil && slices.Contains(years, y)
}

// bankFillPage shows what filling a year from the bank would change.
func (a *App) bankFillPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	L := a.locale(r)
	current, visible, ok, err := a.chosenBudget(r)
	if err != nil {
		a.fail(w, err)
		return
	}
	years, err := a.budgetYears(ctx, current.ID)
	if err != nil {
		a.fail(w, err)
		return
	}
	year, hasYear := bankFillYear(r, years)
	if !ok || !hasYear {
		http.NotFound(w, r)
		return
	}
	b, err := loadBudget(ctx, a.db, current.ID, year, "")
	if err != nil {
		a.fail(w, err)
		return
	}
	overwrite := r.FormValue("mode") == "overwrite"
	changes, kept, err := bankFill(ctx, a.db, b, overwrite)
	if err != nil {
		a.fail(w, err)
		return
	}
	imported, err := importedMonths(ctx, a.db, current.ID, year)
	if err != nil {
		a.fail(w, err)
		return
	}
	a.render(w, r, "bank_fill", map[string]any{
		"Title":     L.T("bank.fill_title", year),
		"Tabs":      bankTabs(L, current, "compare"),
		"Current":   current,
		"Budgets":   pickable(visible),
		"Year":      year,
		"Overwrite": overwrite,
		"Changes":   changes,
		"Kept":      kept,
		"In":        imported,
		"Back":      bankURL("/bank/compare", current, url.Values{"year": {strconv.Itoa(year)}}),
	})
}

// bankFillSave fills the year as the preview showed, planned again under a
// lock. Each amount is saved like an edit on the budget page, with history.
func (a *App) bankFillSave(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	current, _, ok, err := a.budgetFor(r)
	if err != nil {
		a.fail(w, err)
		return
	}
	years, err := a.budgetYears(ctx, current.ID)
	if err != nil {
		a.fail(w, err)
		return
	}
	year, hasYear := bankFillYear(r, years)
	if !ok || !hasYear {
		http.NotFound(w, r)
		return
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer tx.Rollback()
	b, err := loadBudget(ctx, tx, current.ID, year, "FOR UPDATE")
	if err == nil && b == nil {
		err = sql.ErrNoRows
	}
	var changes []bankFillChange
	if err == nil {
		changes, _, err = bankFill(ctx, tx, b, r.FormValue("mode") == "overwrite")
	}
	var lines []int64
	for _, c := range changes {
		if err != nil {
			break
		}
		err = setField(ctx, tx, c.Line.ID, c.Month, dbValue(c.Old), dbValue(c.New), currentUser(r).ID)
		if err == nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO bank_filled (line_id, month, amount) VALUES (?, ?, ?)
				ON DUPLICATE KEY UPDATE amount = VALUES(amount)`, c.Line.ID, c.Month, c.New)
		}
		if !slices.Contains(lines, c.Line.ID) {
			lines = append(lines, c.Line.ID)
		}
	}
	if len(lines) > 0 && err == nil {
		var keep int
		if keep, err = historyKeep(ctx, tx); err == nil {
			err = pruneHistory(ctx, tx, lines, keep)
		}
		if err == nil {
			err = syncFromSource(ctx, tx, current.ID, year) // lines elsewhere that link to these
		}
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	http.Redirect(w, r, bankURL("/bank/compare", current, url.Values{"year": {strconv.Itoa(year)}, "filled": {strconv.Itoa(len(changes))}}), http.StatusSeeOther)
}
