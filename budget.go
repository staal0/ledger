package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
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

// querier is a *sql.DB, or a *sql.Tx to read inside a transaction.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (a *App) loadBudget(ctx context.Context, year int) (*Budget, error) {
	return loadBudget(ctx, a.db, year, "")
}

// loadBudget returns nil when there is no budget for year. lock is appended to
// the query of the budget row, e.g. "FOR UPDATE".
func loadBudget(ctx context.Context, q querier, year int, lock string) (*Budget, error) {
	b := &Budget{Year: year, Lines: map[string][]*Line{}}
	err := q.QueryRowContext(ctx, "SELECT id FROM budgets WHERE year = ? "+lock, year).Scan(&b.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	rows, err := q.QueryContext(ctx, "SELECT id, section, name FROM budget_lines WHERE budget_id = ? ORDER BY sort, id", b.ID)
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

	amounts, err := q.QueryContext(ctx, `SELECT a.line_id, a.month, a.amount
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
	data["Original"] = originalValues(b)
	if data["Previous"], err = a.previousValues(ctx, b.ID, L); err != nil {
		a.fail(w, err)
		return
	}
	data["Conflicts"] = a.takeConflicts(w, r)
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
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	L := a.locale(r)
	var original map[string]string // see originalValues
	_ = json.Unmarshal([]byte(r.PostFormValue("original")), &original)

	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer tx.Rollback()
	// The lock makes saves of the same year run one after the other, so each merges with the one before.
	b, err := loadBudget(ctx, tx, year, "FOR UPDATE")
	if err != nil {
		a.fail(w, err)
		return
	}
	if b == nil {
		http.NotFound(w, r)
		return
	}
	exec := func(q string, args ...any) {
		if err == nil {
			_, err = tx.ExecContext(ctx, q, args...)
		}
	}

	// Only fields changed on this page are written. A field that was also changed
	// elsewhere since the page was loaded keeps that value and is reported.
	var conflicts []string
	current := map[string]bool{}
	var changed []int64 // lines with recorded changes, to prune their history
	set := func(l *Line, field int, old, new string) {
		if err == nil {
			err = setField(ctx, tx, l.ID, field, old, new, currentUser(r).ID)
		}
		if len(changed) == 0 || changed[len(changed)-1] != l.ID {
			changed = append(changed, l.ID)
		}
	}
	for _, s := range sections {
		for _, l := range b.Lines[s] {
			key := fmt.Sprintf("n_%d", l.ID)
			current[key] = true
			name, posted := r.PostForm[key]
			if !posted {
				continue // line was added elsewhere after this page was loaded
			}
			if n := strings.TrimSpace(name[0]); n != "" {
				o, known := original[key]
				switch {
				case known && n == o: // not changed here
				case known && l.Name != o && l.Name != n:
					conflicts = append(conflicts, L.T("budget.conflict_name", o, l.Name, n))
				case n != l.Name:
					set(l, 0, l.Name, n)
				}
			}
			for _, m := range months {
				key := fmt.Sprintf("a_%d_%d", l.ID, m)
				v, perr := L.ParseMoney(r.PostFormValue(key))
				if perr != nil {
					http.Error(w, L.T("budget.invalid_amount", l.Name, L.T(fmt.Sprintf("month.%d", m))), http.StatusBadRequest)
					return
				}
				o, known := original[key]
				ov, _ := parseMoney(o, ".", "")
				cur := l.Amounts[m]
				switch {
				case known && v == ov: // not changed here
				case known && cur != ov && cur != v:
					conflicts = append(conflicts, L.T("budget.conflict_amount", l.Name, L.T(fmt.Sprintf("month.%d", m)), L.Money(v), L.Money(cur)))
				case v != cur:
					set(l, m, dbValue(cur), dbValue(v))
				}
			}
		}
	}
	if len(changed) > 0 && err == nil {
		var keep int
		if keep, err = historyKeep(ctx, tx); err == nil {
			err = pruneHistory(ctx, tx, changed, keep)
		}
	}
	// Lines deleted elsewhere: only worth mentioning if they were edited here.
	var deleted []string
	for key := range r.PostForm {
		id, isLine := strings.CutPrefix(key, "n_")
		if isLine && !current[key] && editedHere(r, original, id, L) {
			deleted = append(deleted, L.T("budget.conflict_deleted", original[key]))
		}
	}
	slices.Sort(deleted)
	conflicts = append(conflicts, deleted...)

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
	if len(conflicts) > 0 {
		a.setConflicts(w, conflicts, L)
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// originalValues is every field of the budget form as the page shows it, sent
// back with a save so the save can tell which fields were changed on the page.
// Amounts are in the database format.
func originalValues(b *Budget) string {
	v := map[string]string{}
	for _, lines := range b.Lines {
		for _, l := range lines {
			v[fmt.Sprintf("n_%d", l.ID)] = l.Name
			for _, m := range months {
				v[fmt.Sprintf("a_%d_%d", l.ID, m)] = dbValue(l.Amounts[m])
			}
		}
	}
	js, _ := json.Marshal(v)
	return string(js)
}

// editedHere reports whether the page changed any field of line id.
func editedHere(r *http.Request, original map[string]string, id string, L *Locale) bool {
	if strings.TrimSpace(r.PostFormValue("n_"+id)) != original["n_"+id] {
		return true
	}
	for _, m := range months {
		key := fmt.Sprintf("a_%s_%d", id, m)
		v, _ := L.ParseMoney(r.PostFormValue(key))
		if o, _ := parseMoney(original[key], ".", ""); v != o {
			return true
		}
	}
	return false
}

const conflictCookie = "ledger_conflicts"

// setConflicts hands the conflict messages to the page the save redirects to.
func (a *App) setConflicts(w http.ResponseWriter, msgs []string, L *Locale) {
	const most = 10 // keeps the cookie well under the 4 KB browsers allow
	if len(msgs) > most {
		msgs = append(msgs[:most], L.T("budget.conflict_more", len(msgs)-most))
	}
	js, _ := json.Marshal(msgs)
	a.setCookie(w, conflictCookie, base64.RawURLEncoding.EncodeToString(js), "/budget/", time.Minute)
}

// takeConflicts returns the messages set by the last save, once.
func (a *App) takeConflicts(w http.ResponseWriter, r *http.Request) []string {
	c, err := r.Cookie(conflictCookie)
	if err != nil {
		return nil
	}
	a.setCookie(w, conflictCookie, "", "/budget/", -time.Second)
	var msgs []string
	if js, err := base64.RawURLEncoding.DecodeString(c.Value); err == nil {
		_ = json.Unmarshal(js, &msgs)
	}
	return msgs
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
