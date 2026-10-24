package main

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
)

// lineLink is where a linked line takes its amounts from: the line with this
// section and name in the same year of the budget. The linked line keeps a copy
// of them (see syncLinks), so everything reading amounts needs no change.
type lineLink struct {
	BudgetID   int64
	BudgetName string
	Section    string
	Name       string
	Broken     bool // no such line in that year of the budget
}

// syncLinks copies the source amounts into the linked lines that scope selects:
// a condition on t (the linked line) and ty (its year). Lines whose source is
// not found keep their amounts.
func syncLinks(ctx context.Context, tx *sql.Tx, scope string, args ...any) error {
	source := `FROM budget_lines t
		JOIN budget_years ty ON ty.id = t.year_id
		JOIN budget_years sy ON sy.budget_id = t.link_budget_id AND sy.year = ty.year
		JOIN budget_lines s ON s.year_id = sy.id AND s.section = t.link_section AND s.name = t.link_name
		WHERE ` + scope
	if _, err := tx.ExecContext(ctx, `DELETE a FROM budget_amounts a JOIN (SELECT DISTINCT t.id `+source+`) linked ON linked.id = a.line_id`, args...); err != nil {
		return err
	}
	// INSERT IGNORE: with two source lines of the same name, the first wins.
	_, err := tx.ExecContext(ctx, `INSERT IGNORE INTO budget_amounts (line_id, month, amount)
		SELECT t.id, sa.month, sa.amount `+strings.Replace(source, "WHERE", "JOIN budget_amounts sa ON sa.line_id = s.id WHERE", 1), args...)
	return err
}

// syncFromSource refreshes the lines linked to budget's year.
func syncFromSource(ctx context.Context, tx *sql.Tx, budget int64, year int) error {
	return syncLinks(ctx, tx, "t.link_budget_id = ? AND ty.year = ?", budget, year)
}

// syncYear refreshes the linked lines of a budget year.
func syncYear(ctx context.Context, tx *sql.Tx, yearID int64) error {
	return syncLinks(ctx, tx, "t.year_id = ?", yearID)
}

// linkOption is a line another budget's line can link to.
type linkOption struct {
	BudgetID   int64
	BudgetName string
	Section    string
	Name       string
}

// Value identifies the option in a form.
func (o linkOption) Value() string {
	return strconv.FormatInt(o.BudgetID, 10) + "/" + o.Section + "/" + o.Name
}

// linkOptions lists the lines in year of the budgets in visible, other than
// current, that a line can link to.
func (a *App) linkOptions(ctx context.Context, current int64, visible []budgetRef, year int) ([]linkOption, error) {
	var out []linkOption
	for _, v := range visible {
		if v.ID == current {
			continue
		}
		rows, err := a.db.QueryContext(ctx, `SELECT s.section, s.name FROM budget_lines s
			JOIN budget_years y ON y.id = s.year_id
			WHERE y.budget_id = ? AND y.year = ? AND s.link_budget_id IS NULL -- so links never chain
			ORDER BY FIELD(s.section, 'income', 'expense', 'allocation'), s.sort, s.id`, v.ID, year)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			o := linkOption{BudgetID: v.ID, BudgetName: v.Name}
			if err := rows.Scan(&o.Section, &o.Name); err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, o)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

var errBadLink = errors.New("not a line this line can link to")

// setLink links line in b to the source named by value (see linkOption.Value),
// which must be one of options, and copies its amounts.
func setLink(ctx context.Context, tx *sql.Tx, b *BudgetYear, line int64, value string, options []linkOption) error {
	i := -1
	for n, o := range options {
		if o.Value() == value {
			i = n
		}
	}
	if i < 0 {
		return errBadLink
	}
	o := options[i]
	res, err := tx.ExecContext(ctx, `UPDATE budget_lines SET link_budget_id = ?, link_section = ?, link_name = ?
		WHERE id = ? AND year_id = ?`, o.BudgetID, o.Section, o.Name, line, b.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errBadLink
	}
	return syncYear(ctx, tx, b.ID)
}
