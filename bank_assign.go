package main

import (
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// bankTransaction is a row of the transactions list.
type bankTransaction struct {
	ID      int64
	Booked  time.Time
	Account string
	Text    string
	Amount  Money
	LineID  int64
	Ignored bool
	ByRule  bool
}

// Choice is the transaction's line in the picker: a line id, "ignore" or "".
func (t bankTransaction) Choice() string {
	switch {
	case t.Ignored:
		return "ignore"
	case t.LineID != 0:
		return strconv.FormatInt(t.LineID, 10)
	}
	return ""
}

// bankListFilter is what the transactions list shows: one account or all
// (Account 0), one year, only the unassigned unless All, and which page of
// what size.
type bankListFilter struct {
	Account    int64
	Year       int
	All        bool
	Page, Size int
}

func (f bankListFilter) query() url.Values {
	q := url.Values{"year": {strconv.Itoa(f.Year)}}
	if f.Account != 0 {
		q.Set("account", strconv.FormatInt(f.Account, 10))
	}
	if f.All {
		q.Set("show", "all")
	}
	if f.Page > 1 {
		q.Set("page", strconv.Itoa(f.Page))
	}
	if f.Size != 0 && f.Size != pageSizeDefault {
		q.Set("size", strconv.Itoa(f.Size))
	}
	return q
}

// bankTransactionsPage lists one year's transactions with a line picker each.
func (a *App) bankTransactionsPage(w http.ResponseWriter, r *http.Request) {
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
	accounts, err := a.bankAccounts(ctx, current.ID)
	if err != nil {
		a.fail(w, err)
		return
	}
	f := bankListFilter{All: r.FormValue("show") == "all"}
	for _, acc := range accounts {
		if r.FormValue("account") == strconv.FormatInt(acc.ID, 10) {
			f.Account = acc.ID
		}
	}
	scope := ` AND (? = 0 OR t.account_id = ?)`

	// The years with transactions, newest first; the newest is shown unless one is chosen.
	var years []int
	rows, err := a.db.QueryContext(ctx, `SELECT DISTINCT YEAR(t.booked) FROM bank_transactions t
		JOIN bank_accounts a ON a.id = t.account_id WHERE a.budget_id = ?`+scope+` ORDER BY 1 DESC`, current.ID, f.Account, f.Account)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var y int
		if err := rows.Scan(&y); err != nil {
			a.fail(w, err)
			return
		}
		years = append(years, y)
	}
	if err := rows.Err(); err != nil {
		a.fail(w, err)
		return
	}
	f.Year = time.Now().Year()
	if len(years) > 0 {
		f.Year = years[0]
	}
	if y, err := strconv.Atoi(r.FormValue("year")); err == nil && slices.Contains(years, y) {
		f.Year = y
	}

	from := ` FROM bank_transactions t JOIN bank_accounts a ON a.id = t.account_id
		WHERE a.budget_id = ? AND YEAR(t.booked) = ?` + scope
	args := []any{current.ID, f.Year, f.Account, f.Account}
	var unassigned, total int
	if err := a.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(t.line_id IS NULL AND NOT t.ignored), 0)`+from, args...).Scan(&total, &unassigned); err != nil {
		a.fail(w, err)
		return
	}
	if !f.All {
		from += ` AND t.line_id IS NULL AND NOT t.ignored`
		total = unassigned
	}
	pages := newPager(r, L, "page", total, "")
	f.Page, f.Size = pages.Page, pages.Size

	var list []bankTransaction
	rows, err = a.db.QueryContext(ctx, `SELECT t.id, t.booked, a.name, t.text, t.amount, COALESCE(t.line_id, 0), t.ignored, t.rule_id IS NOT NULL`+
		from+` ORDER BY t.booked DESC, t.id DESC LIMIT ? OFFSET ?`, append(args, pages.Size, pages.Offset())...)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var t bankTransaction
		if err := rows.Scan(&t.ID, &t.Booked, &t.Account, &t.Text, &t.Amount, &t.LineID, &t.Ignored, &t.ByRule); err != nil {
			a.fail(w, err)
			return
		}
		list = append(list, t)
	}
	if err := rows.Err(); err != nil {
		a.fail(w, err)
		return
	}

	lines, hasYear, err := a.bankYearLines(r, current.ID, f.Year)
	if err != nil {
		a.fail(w, err)
		return
	}
	a.render(w, r, "bank_transactions", map[string]any{
		"Title":        L.T("nav.bank"),
		"Tabs":         bankTabs(L, current, "transactions"),
		"Current":      current,
		"Budgets":      pickable(visible),
		"Accounts":     accounts,
		"Filter":       f,
		"Years":        years,
		"Transactions": list,
		"Unassigned":   unassigned,
		"Pages":        pages,
		"Sections":     groupLines(lines),
		"HasYear":      hasYear,
		"YearURL":      current.yearURL(f.Year),
	})
}

// bankYearLines lists the lines of the budget's year, in the budget's order;
// hasYear is false when the budget has no such year.
func (a *App) bankYearLines(r *http.Request, budget int64, year int) (lines []bankLineChoice, hasYear bool, err error) {
	b, err := loadBudget(r.Context(), a.db, budget, year, "")
	if err != nil || b == nil {
		return nil, false, err
	}
	for _, s := range sections {
		for _, l := range b.Lines[s] {
			lines = append(lines, bankLineChoice{ID: l.ID, Section: s, Name: l.Name})
		}
	}
	return lines, true, nil
}

// bankTransactionsSave stores the line picked for each transaction of the
// list. A transaction with "remember" ticked teaches a rule from its text,
// which is then applied to the unassigned ones. The delete button of a row
// saves the list too, and then deletes that transaction.
func (a *App) bankTransactionsSave(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	b, _, ok, err := a.budgetFor(r)
	if err != nil {
		a.fail(w, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	f := bankListFilter{All: r.FormValue("show") == "all"}
	f.Account, _ = strconv.ParseInt(r.FormValue("account"), 10, 64)
	f.Year, _ = strconv.Atoi(r.FormValue("year"))
	f.Page, _ = strconv.Atoi(r.FormValue("page"))
	f.Size, _ = strconv.Atoi(r.FormValue("size"))

	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer tx.Rollback()

	// The budget's lines, to check that a picked line is in the transaction's year.
	type lineInfo struct {
		year          int
		section, name string
	}
	lines := map[int64]lineInfo{}
	rows, err := tx.QueryContext(ctx, `SELECT l.id, y.year, l.section, l.name FROM budget_lines l
		JOIN budget_years y ON y.id = l.year_id WHERE y.budget_id = ?`, b.ID)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var l lineInfo
		if err := rows.Scan(&id, &l.year, &l.section, &l.name); err != nil {
			a.fail(w, err)
			return
		}
		lines[id] = l
	}
	if err := rows.Err(); err != nil {
		a.fail(w, err)
		return
	}

	type posted struct {
		bankTransaction
		year int
	}
	var list []posted
	for _, s := range r.PostForm["t"] {
		var t posted
		err := tx.QueryRowContext(ctx, `SELECT t.id, t.text, YEAR(t.booked), COALESCE(t.line_id, 0), t.ignored
			FROM bank_transactions t JOIN bank_accounts a ON a.id = t.account_id WHERE t.id = ? AND a.budget_id = ?`, s, b.ID).
			Scan(&t.ID, &t.Text, &t.year, &t.LineID, &t.Ignored)
		if errors.Is(err, sql.ErrNoRows) {
			continue // deleted meanwhile
		}
		if err != nil {
			a.fail(w, err)
			return
		}
		list = append(list, t)
	}

	var learned []int64
	for _, t := range list {
		id := strconv.FormatInt(t.ID, 10)
		choice := r.FormValue("line_" + id)
		var line any
		ignored := choice == "ignore"
		section, name := "", ""
		if choice != "" && !ignored {
			lid, _ := strconv.ParseInt(choice, 10, 64)
			l, found := lines[lid]
			if !found || l.year != t.year {
				http.Error(w, "invalid line", http.StatusBadRequest)
				return
			}
			line, section, name = lid, l.section, l.name
		}
		if choice != t.Choice() {
			if _, err := tx.ExecContext(ctx, `UPDATE bank_transactions SET line_id = ?, ignored = ?, rule_id = NULL WHERE id = ?`, line, ignored, t.ID); err != nil {
				a.fail(w, err)
				return
			}
			if choice != "" && r.FormValue("remember_"+id) != "" {
				res, err := tx.ExecContext(ctx, `INSERT INTO bank_rules (budget_id, pattern, section, line_name, ignored) VALUES (?, ?, NULLIF(?, ''), NULLIF(?, ''), ?)
					ON DUPLICATE KEY UPDATE id = LAST_INSERT_ID(id), section = VALUES(section), line_name = VALUES(line_name), ignored = VALUES(ignored)`,
					b.ID, rulePattern(t.Text), section, name, ignored)
				if err != nil {
					a.fail(w, err)
					return
				}
				rule, err := res.LastInsertId()
				if err != nil {
					a.fail(w, err)
					return
				}
				learned = append(learned, rule)
			}
		}
	}
	// A rule learned again may point elsewhere now: what it assigned follows.
	if err := unassignByRules(ctx, tx, learned); err != nil {
		a.fail(w, err)
		return
	}
	if del, found := strings.CutPrefix(r.FormValue("action"), "delete:"); found {
		if _, err := tx.ExecContext(ctx, `DELETE t FROM bank_transactions t JOIN bank_accounts a ON a.id = t.account_id
			WHERE t.id = ? AND a.budget_id = ?`, del, b.ID); err != nil {
			a.fail(w, err)
			return
		}
	}
	if _, err := applyBankRules(ctx, tx, b.ID); err != nil {
		a.fail(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		a.fail(w, err)
		return
	}
	http.Redirect(w, r, bankURL("/bank/transactions", b, f.query()), http.StatusSeeOther)
}
