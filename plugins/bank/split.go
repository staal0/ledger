package bank

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/staal0/ledger/internal/app"
)

// bankSplitPart is one part of a split transaction, as the bank has it:
// negative for money going out.
type bankSplitPart struct {
	LineID  int64
	Ignored bool
	Amount  app.Money
	Name    string // the line's name
}

func splitParts(ctx context.Context, q app.Querier, transaction int64) ([]bankSplitPart, error) {
	rows, err := q.QueryContext(ctx, `SELECT COALESCE(s.line_id, 0), s.ignored, s.amount, COALESCE(l.name, '')
		FROM bank_splits s LEFT JOIN budget_lines l ON l.id = s.line_id WHERE s.transaction_id = ? ORDER BY s.id`, transaction)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []bankSplitPart
	for rows.Next() {
		var p bankSplitPart
		if err := rows.Scan(&p.LineID, &p.Ignored, &p.Amount, &p.Name); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// splitRow is a row of the split form, with amounts in the transaction's direction.
type splitRow struct {
	Choice string // a line id, "ignore" or ""
	Amount string
}

type splitTransaction struct {
	ID      int64
	Booked  time.Time
	Text    string
	Amount  app.Money
	Split   bool
	budget  app.BudgetRef
	account int64
}

// sign is 1 for money coming in and -1 for money going out.
func (t splitTransaction) sign() app.Money {
	if t.Amount < 0 {
		return -1
	}
	return 1
}

// splitFor is the transaction of the request's {id}, when the user may see its budget.
func (a handlers) splitFor(r *http.Request) (t splitTransaction, ok bool, err error) {
	err = a.DB().QueryRowContext(r.Context(), `SELECT t.id, t.booked, t.text, t.amount, t.split, t.account_id
		FROM bank_transactions t WHERE t.id = ?`, r.PathValue("id")).Scan(&t.ID, &t.Booked, &t.Text, &t.Amount, &t.Split, &t.account)
	if errors.Is(err, sql.ErrNoRows) {
		return t, false, nil
	}
	if err != nil {
		return t, false, err
	}
	_, t.budget, ok, err = a.bankAccountFor(r, strconv.FormatInt(t.account, 10))
	return t, ok, err
}

// splitBack is where the split page returns to: the list it was opened from.
func splitBack(r *http.Request, b app.BudgetRef) string {
	if back := r.FormValue("back"); strings.HasPrefix(back, "/bank/transactions") {
		return back
	}
	return bankURL("/bank/transactions", b, nil)
}

func (a handlers) bankSplitPage(w http.ResponseWriter, r *http.Request) {
	t, ok, err := a.splitFor(r)
	if err != nil {
		a.Fail(w, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	parts, err := splitParts(r.Context(), a.DB(), t.ID)
	if err != nil {
		a.Fail(w, err)
		return
	}
	L := a.Locale(r)
	var rows []splitRow
	for _, p := range parts {
		c := strconv.FormatInt(p.LineID, 10)
		if p.Ignored {
			c = "ignore"
		}
		rows = append(rows, splitRow{c, L.Input(p.Amount * t.sign())})
	}
	a.renderSplit(w, r, t, rows, "")
}

func (a handlers) renderSplit(w http.ResponseWriter, r *http.Request, t splitTransaction, rows []splitRow, problem string) {
	for len(rows) < 2 || rows[len(rows)-1] != (splitRow{}) {
		rows = append(rows, splitRow{})
	}
	lines, _, err := a.bankYearLines(r, t.budget.ID, t.Booked.Year())
	if err != nil {
		a.Fail(w, err)
		return
	}
	type picker struct {
		Choice   string
		L        *app.Locale
		Sections []bankSection
	}
	var pickers []picker
	for _, row := range rows {
		pickers = append(pickers, picker{row.Choice, a.Locale(r), groupLines(lines)})
	}
	L := a.Locale(r)
	a.Render(w, r, "bank_split", map[string]any{
		"Title":       L.T("bank.split_title"),
		"Transaction": t,
		"Rows":        rows,
		"Pickers":     pickers,
		"Problem":     problem,
		"Back":        splitBack(r, t.budget),
		"Pattern":     L.InputPattern(),
	})
}

// bankSplitSave stores the parts, or with action "undo" makes the transaction whole again.
func (a handlers) bankSplitSave(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	L := a.Locale(r)
	t, ok, err := a.splitFor(r)
	if err != nil {
		a.Fail(w, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	tx, err := a.DB().BeginTx(ctx, nil)
	if err != nil {
		a.Fail(w, err)
		return
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM bank_splits WHERE transaction_id = ?`, t.ID); err != nil {
		a.Fail(w, err)
		return
	}

	if r.FormValue("action") == "undo" {
		_, err = tx.ExecContext(ctx, `UPDATE bank_transactions SET split = FALSE, line_id = NULL, ignored = FALSE, rule_id = NULL, by_default = FALSE WHERE id = ?`, t.ID)
		if err == nil {
			_, err = applyBankRules(ctx, tx, t.budget.ID)
		}
		if err == nil {
			err = tx.Commit()
		}
		if err != nil {
			a.Fail(w, err)
			return
		}
		http.Redirect(w, r, splitBack(r, t.budget), http.StatusSeeOther)
		return
	}

	// The rows with a line or an amount; one without an amount takes the rest.
	var rows []splitRow
	for i := 0; r.Form.Has("line_" + strconv.Itoa(i)); i++ {
		row := splitRow{r.FormValue("line_" + strconv.Itoa(i)), strings.TrimSpace(r.FormValue("amount_" + strconv.Itoa(i)))}
		if row != (splitRow{}) {
			rows = append(rows, row)
		}
	}
	lines, _, err := a.bankYearLines(r, t.budget.ID, t.Booked.Year())
	if err != nil {
		a.Fail(w, err)
		return
	}
	known := map[string]bool{"ignore": true}
	for _, l := range lines {
		known[strconv.FormatInt(l.ID, 10)] = true
	}
	total := t.Amount * t.sign() // the whole, in the transaction's direction
	var sum app.Money
	rest := -1
	amounts := make([]app.Money, len(rows))
	for i, row := range rows {
		if !known[row.Choice] {
			a.renderSplit(w, r, t, rows, L.T("bank.split_err_line"))
			return
		}
		if row.Amount == "" {
			if rest >= 0 {
				a.renderSplit(w, r, t, rows, L.T("bank.split_err_rest"))
				return
			}
			rest = i
			continue
		}
		if amounts[i], err = L.ParseMoney(row.Amount); err != nil || amounts[i] == 0 {
			a.renderSplit(w, r, t, rows, L.T("bank.split_err_amount", row.Amount))
			return
		}
		sum += amounts[i]
	}
	if rest >= 0 {
		amounts[rest] = total - sum
		sum = total
	}
	if len(rows) == 0 || sum != total || rest >= 0 && amounts[rest] == 0 {
		a.renderSplit(w, r, t, rows, L.T("bank.split_err_sum", L.Money(sum), L.Money(total)))
		return
	}

	for i, row := range rows {
		var line any
		if row.Choice != "ignore" {
			line, _ = strconv.ParseInt(row.Choice, 10, 64)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO bank_splits (transaction_id, line_id, ignored, amount) VALUES (?, ?, ?, ?)`,
			t.ID, line, row.Choice == "ignore", amounts[i]*t.sign()); err != nil {
			a.Fail(w, err)
			return
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE bank_transactions SET split = TRUE, line_id = NULL, ignored = FALSE, rule_id = NULL, by_default = FALSE WHERE id = ?`, t.ID); err != nil {
		a.Fail(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		a.Fail(w, err)
		return
	}
	http.Redirect(w, r, splitBack(r, t.budget), http.StatusSeeOther)
}
