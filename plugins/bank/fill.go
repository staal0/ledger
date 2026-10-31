package bank

import (
	"context"
	"database/sql"
	"github.com/staal0/ledger/internal/app"
	"net/http"
	"net/url"
	"slices"
	"strconv"
)

// bankFillChange is an amount the fill sets: a line's month to what its
// transactions add up to.
type bankFillChange struct {
	Line     *app.Line
	Month    int
	Old, New app.Money
	Update   bool // Old was filled from the bank and is updated without overwrite
}

// bankFill plans filling year b from the bank: for each line and imported
// month with assigned transactions, the amount becomes their sum. Without
// overwrite only empty amounts are set, and those an earlier fill wrote and
// nobody changed since, so a month imported in parts is updated as it fills
// up; kept counts the amounts left alone. Linked lines are skipped, as their
// amounts come from their source.
func bankFill(ctx context.Context, q app.Querier, b *app.BudgetYear, overwrite bool) (changes []bankFillChange, kept int, err error) {
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
	filled := map[cell]app.Money{}
	rows, err := q.QueryContext(ctx, `SELECT f.line_id, f.month, f.amount FROM bank_filled f
		JOIN budget_lines l ON l.id = f.line_id WHERE l.year_id = ?`, b.ID)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var c cell
		var m app.Money
		if err := rows.Scan(&c.line, &c.month, &m); err != nil {
			return nil, 0, err
		}
		filled[c] = m
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	for _, s := range app.Sections {
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
func (a handlers) bankFillPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	L := a.Locale(r)
	current, visible, ok, err := a.ChosenBudget(r)
	if err != nil {
		a.Fail(w, err)
		return
	}
	years, err := a.BudgetYears(ctx, current.ID)
	if err != nil {
		a.Fail(w, err)
		return
	}
	year, hasYear := bankFillYear(r, years)
	if !ok || !hasYear {
		http.NotFound(w, r)
		return
	}
	b, err := app.LoadBudget(ctx, a.DB(), current.ID, year, "")
	if err != nil {
		a.Fail(w, err)
		return
	}
	overwrite := r.FormValue("mode") == "overwrite"
	changes, kept, err := bankFill(ctx, a.DB(), b, overwrite)
	if err != nil {
		a.Fail(w, err)
		return
	}
	imported, err := importedMonths(ctx, a.DB(), current.ID, year)
	if err != nil {
		a.Fail(w, err)
		return
	}
	a.Render(w, r, "bank_fill", map[string]any{
		"Title":     L.T("bank.fill_title", year),
		"Tabs":      bankTabs(L, current, "compare"),
		"Current":   current,
		"Budgets":   app.Pickable(visible),
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
func (a handlers) bankFillSave(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	current, _, ok, err := a.BudgetFor(r)
	if err != nil {
		a.Fail(w, err)
		return
	}
	years, err := a.BudgetYears(ctx, current.ID)
	if err != nil {
		a.Fail(w, err)
		return
	}
	year, hasYear := bankFillYear(r, years)
	if !ok || !hasYear {
		http.NotFound(w, r)
		return
	}
	tx, err := a.DB().BeginTx(ctx, nil)
	if err != nil {
		a.Fail(w, err)
		return
	}
	defer tx.Rollback()
	b, err := app.LoadBudget(ctx, tx, current.ID, year, "FOR UPDATE")
	if err == nil && b == nil {
		err = sql.ErrNoRows
	}
	var changes []bankFillChange
	if err == nil {
		changes, _, err = bankFill(ctx, tx, b, r.FormValue("mode") == "overwrite")
	}
	// The amounts are saved like an edit on the budget page; the fill notes what it wrote.
	var amounts []app.AmountChange
	for _, c := range changes {
		amounts = append(amounts, app.AmountChange{Line: c.Line.ID, Month: c.Month, Old: c.Old, New: c.New})
		if err == nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO bank_filled (line_id, month, amount) VALUES (?, ?, ?)
				ON DUPLICATE KEY UPDATE amount = VALUES(amount)`, c.Line.ID, c.Month, c.New)
		}
	}
	if err == nil {
		err = app.SetAmounts(ctx, tx, current.ID, year, amounts, app.CurrentUser(r).ID)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		a.Fail(w, err)
		return
	}
	http.Redirect(w, r, bankURL("/bank/compare", current, url.Values{"year": {strconv.Itoa(year)}, "filled": {strconv.Itoa(len(changes))}}), http.StatusSeeOther)
}
