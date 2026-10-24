package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

var (
	sections = []string{"income", "expense", "allocation"}
	months   = []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
)

// Totals holds one value per month at index 1-12 and the year total at index 0.
type Totals [13]Money

func (t Totals) Months() []Money { return t[1:] }

type Line struct {
	ID      int64
	Section string
	Name    string
	Amounts Totals
}

type Budget struct {
	ID    int64
	Year  int
	Lines map[string][]*Line // by section, in display order
}

func (b *Budget) total(section string) (t Totals) {
	for _, l := range b.Lines[section] {
		for i := range t {
			t[i] += l.Amounts[i]
		}
	}
	return t
}

// loadBudget returns nil when there is no budget for year.
func (a *App) loadBudget(ctx context.Context, year int) (*Budget, error) {
	b := &Budget{Year: year, Lines: map[string][]*Line{}}
	err := a.db.QueryRowContext(ctx, "SELECT id FROM budgets WHERE year = ?", year).Scan(&b.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	rows, err := a.db.QueryContext(ctx, "SELECT id, section, name FROM budget_lines WHERE budget_id = ? ORDER BY sort, id", b.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := map[int64]*Line{}
	for rows.Next() {
		l := &Line{}
		if err := rows.Scan(&l.ID, &l.Section, &l.Name); err != nil {
			return nil, err
		}
		b.Lines[l.Section] = append(b.Lines[l.Section], l)
		byID[l.ID] = l
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	amounts, err := a.db.QueryContext(ctx, `SELECT a.line_id, a.month, a.amount
		FROM budget_amounts a JOIN budget_lines l ON l.id = a.line_id WHERE l.budget_id = ?`, b.ID)
	if err != nil {
		return nil, err
	}
	defer amounts.Close()
	for amounts.Next() {
		var id int64
		var month int
		var v Money
		if err := amounts.Scan(&id, &month, &v); err != nil {
			return nil, err
		}
		if l := byID[id]; l != nil && month >= 1 && month <= 12 {
			l.Amounts[month] = v
			l.Amounts[0] += v
		}
	}
	return b, amounts.Err()
}

// budgetIndex goes to ?year= (from the year picker), or else the current year.
func (a *App) budgetIndex(w http.ResponseWriter, r *http.Request) {
	year := time.Now().Year()
	if y, err := strconv.Atoi(r.FormValue("year")); err == nil {
		year = y
	}
	http.Redirect(w, r, fmt.Sprintf("/budget/%d", year), http.StatusFound)
}

// budgetYears lists the years that have a budget, newest first.
func (a *App) budgetYears(ctx context.Context) ([]int, error) {
	rows, err := a.db.QueryContext(ctx, "SELECT year FROM budgets ORDER BY year DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var years []int
	for rows.Next() {
		var y int
		if err := rows.Scan(&y); err != nil {
			return nil, err
		}
		years = append(years, y)
	}
	return years, rows.Err()
}

func pathYear(r *http.Request) (int, bool) {
	y, err := strconv.Atoi(r.PathValue("year"))
	return y, err == nil && y >= 1900 && y <= 9999
}

func (a *App) budgetPage(w http.ResponseWriter, r *http.Request) {
	year, ok := pathYear(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	b, err := a.loadBudget(ctx, year)
	if err != nil {
		a.fail(w, err)
		return
	}
	years, err := a.budgetYears(ctx)
	if err != nil {
		a.fail(w, err)
		return
	}
	if !slices.Contains(years, year) { // show the viewed year in the picker even without a budget
		years = append(years, year)
		slices.SortFunc(years, func(x, y int) int { return y - x })
	}
	L := a.locale(r)
	data := map[string]any{
		"Title":   L.T("budget.title", year),
		"Years":   years,
		"Year":    year,
		"Prev":    year - 1,
		"Next":    year + 1,
		"Months":  months,
		"Pattern": L.InputPattern(),
	}
	if b == nil {
		var n int
		if err := a.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM budgets WHERE year = ?", year-1).Scan(&n); err != nil {
			a.fail(w, err)
			return
		}
		data["PrevExists"] = n > 0
		a.render(w, r, "budget", data)
		return
	}

	type section struct {
		Key   string
		Lines []*Line
		Total Totals
	}
	type calc struct {
		Key    string
		Values Totals
	}
	var secs []section
	for _, s := range sections {
		secs = append(secs, section{s, b.Lines[s], b.total(s)})
	}
	income, expense, allocation := secs[0].Total, secs[1].Total, secs[2].Total
	var net, diff Totals
	for i := range net {
		net[i] = income[i] - expense[i]
		diff[i] = net[i] - allocation[i]
	}
	data["Budget"] = b
	data["Sections"] = secs
	data["Summary"] = []calc{{"income", income}, {"expense", expense}, {"net", net}}
	data["Difference"] = diff
	a.render(w, r, "budget", data)
}

// budgetSave stores the whole grid and then performs the clicked action
// (add, delete or move a line), so unsaved edits are never lost.
func (a *App) budgetSave(w http.ResponseWriter, r *http.Request) {
	year, ok := pathYear(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	b, err := a.loadBudget(ctx, year)
	if err != nil {
		a.fail(w, err)
		return
	}
	if b == nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	L := a.locale(r)

	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer tx.Rollback()
	exec := func(q string, args ...any) {
		if err == nil {
			_, err = tx.ExecContext(ctx, q, args...)
		}
	}

	for _, s := range sections {
		for _, l := range b.Lines[s] {
			name, posted := r.PostForm[fmt.Sprintf("n_%d", l.ID)]
			if !posted {
				continue // line was added elsewhere after this page was loaded
			}
			if n := strings.TrimSpace(name[0]); n != "" && n != l.Name {
				exec("UPDATE budget_lines SET name = ? WHERE id = ?", n, l.ID)
			}
			exec("DELETE FROM budget_amounts WHERE line_id = ?", l.ID)
			for _, m := range months {
				v, perr := L.ParseMoney(r.PostFormValue(fmt.Sprintf("a_%d_%d", l.ID, m)))
				if perr != nil {
					http.Error(w, L.T("budget.invalid_amount", l.Name, L.T(fmt.Sprintf("month.%d", m))), http.StatusBadRequest)
					return
				}
				if v != 0 {
					exec("INSERT INTO budget_amounts (line_id, month, amount) VALUES (?, ?, ?)", l.ID, m, v)
				}
			}
		}
	}

	back := fmt.Sprintf("/budget/%d", year)
	verb, arg, _ := strings.Cut(r.PostFormValue("action"), ":")
	switch verb {
	case "add":
		// The line gets a placeholder name; the page then selects it so it can be renamed at once.
		if slices.Contains(sections, arg) && err == nil {
			var res sql.Result
			res, err = tx.ExecContext(ctx, `INSERT INTO budget_lines (budget_id, section, name, sort)
				SELECT ?, ?, ?, COALESCE(MAX(sort), 0) + 1 FROM budget_lines WHERE budget_id = ?`, b.ID, arg, L.T("budget.new_line"), b.ID)
			if err == nil {
				var id int64
				id, err = res.LastInsertId()
				back += fmt.Sprintf("?added=%d", id) // a query, not a #fragment, so the browser does not jump to it
			}
		}
	case "delete":
		exec("DELETE FROM budget_lines WHERE id = ? AND budget_id = ?", arg, b.ID)
	case "up", "down":
		for _, lines := range b.Lines {
			i := slices.IndexFunc(lines, func(l *Line) bool { return strconv.FormatInt(l.ID, 10) == arg })
			j := i - 1
			if verb == "down" {
				j = i + 1
			}
			if i < 0 || j < 0 || j >= len(lines) {
				continue
			}
			lines[i], lines[j] = lines[j], lines[i]
			for n, l := range lines {
				exec("UPDATE budget_lines SET sort = ? WHERE id = ?", n, l.ID)
			}
		}
	}

	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// budgetCreate creates an empty budget, or a copy of another year's lines
// (and optionally amounts) when the form has a "from" year.
func (a *App) budgetCreate(w http.ResponseWriter, r *http.Request) {
	year, ok := pathYear(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	back := fmt.Sprintf("/budget/%d", year)
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, "INSERT INTO budgets (year) VALUES (?)", year)
	var me *mysql.MySQLError
	if errors.As(err, &me) && me.Number == 1062 { // already exists, e.g. a double submit
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	id, _ := res.LastInsertId()
	if from, perr := strconv.Atoi(r.PostFormValue("from")); perr == nil {
		err = copyLines(ctx, tx, from, id, r.PostFormValue("amounts") != "")
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func copyLines(ctx context.Context, tx *sql.Tx, fromYear int, toBudget int64, withAmounts bool) error {
	rows, err := tx.QueryContext(ctx, `SELECT l.id, l.section, l.name, l.sort
		FROM budget_lines l JOIN budgets b ON b.id = l.budget_id WHERE b.year = ? ORDER BY l.sort, l.id`, fromYear)
	if err != nil {
		return err
	}
	type src struct {
		id            int64
		section, name string
		sort          int
	}
	var lines []src
	for rows.Next() {
		var s src
		if err := rows.Scan(&s.id, &s.section, &s.name, &s.sort); err != nil {
			rows.Close()
			return err
		}
		lines = append(lines, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, s := range lines {
		res, err := tx.ExecContext(ctx, "INSERT INTO budget_lines (budget_id, section, name, sort) VALUES (?, ?, ?, ?)",
			toBudget, s.section, s.name, s.sort)
		if err != nil {
			return err
		}
		if !withAmounts {
			continue
		}
		id, _ := res.LastInsertId()
		_, err = tx.ExecContext(ctx, `INSERT INTO budget_amounts (line_id, month, amount)
			SELECT ?, month, amount FROM budget_amounts WHERE line_id = ?`, id, s.id)
		if err != nil {
			return err
		}
	}
	return nil
}
