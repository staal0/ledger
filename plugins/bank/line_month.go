package bank

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/staal0/ledger/internal/app"
)

// lineMonthURL is the month page for the lines named names in section.
func lineMonthURL(b app.BudgetRef, section string, names []string, year, month int) string {
	return bankURL("/bank/line-month", b, url.Values{"section": {section}, "name": names,
		"year": {strconv.Itoa(year)}, "month": {strconv.Itoa(month)}})
}

// LineMonthURL links a line's stats to what was spent on it in a month.
func (bankPlugin) LineMonthURL(b app.BudgetRef, section string, names []string, year, month int) string {
	return lineMonthURL(b, section, names, year, month)
}

type monthItem struct {
	Name   string
	Amount app.Money
	Line   string // the item's line, when not this one
	Here   bool
}

type monthRow struct {
	Booked  time.Time
	Account string
	Text    string
	Amount  app.Money // on the lines
	Total   app.Money // the whole transaction
	Receipt bool
	Items   []monthItem
}

// bankLineMonth lists what is on lines in one month, receipts with their items.
func (a handlers) bankLineMonth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	L := a.Locale(r)
	current, _, ok, err := a.ChosenBudget(r)
	if err != nil {
		a.Fail(w, err)
		return
	}
	section := r.FormValue("section")
	var names []string
	for _, n := range r.Form["name"] {
		if n != "" && !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	year, _ := strconv.Atoi(r.FormValue("year"))
	month, _ := strconv.Atoi(r.FormValue("month"))
	if !ok || !slices.Contains(app.Sections, section) || len(names) == 0 || month < 1 || month > 12 {
		http.NotFound(w, r)
		return
	}
	lines, args := lineQuery(current, section, names, year, year)
	ids, err := lineIDs(ctx, a.DB(), lines, args)
	if err != nil {
		a.Fail(w, err)
		return
	}
	if len(ids) == 0 {
		http.NotFound(w, r)
		return
	}

	rules, err := loadBankRules(ctx, a.DB(), current.ID)
	if err != nil {
		a.Fail(w, err)
		return
	}
	sign := actualSign(section)
	rows, err := a.DB().QueryContext(ctx, `SELECT t.id, t.booked, a.name, t.text, t.amount, t.receipt IS NOT NULL, SUM(x.amount) FROM (
			SELECT id, amount FROM bank_transactions WHERE line_id IN (`+lines+`) AND NOT ignored AND NOT split
			UNION ALL
			SELECT transaction_id, amount FROM bank_splits WHERE line_id IN (`+lines+`) AND NOT ignored
		) x JOIN bank_transactions t ON t.id = x.id JOIN bank_accounts a ON a.id = t.account_id
		WHERE YEAR(t.booked) = ? AND MONTH(t.booked) = ?
		GROUP BY t.id, t.booked, a.name, t.text, t.amount, t.receipt ORDER BY t.booked, t.id`,
		append(append(append([]any{}, args...), args...), year, month)...)
	if err != nil {
		a.Fail(w, err)
		return
	}
	defer rows.Close()
	var list []monthRow
	var txIDs []int64
	var sum app.Money
	for rows.Next() {
		var row monthRow
		var id int64
		if err := rows.Scan(&id, &row.Booked, &row.Account, &row.Text, &row.Total, &row.Receipt, &row.Amount); err != nil {
			a.Fail(w, err)
			return
		}
		row.Text = displayName(rules, row.Text)
		row.Amount *= sign
		row.Total *= sign
		sum += row.Amount
		list = append(list, row)
		txIDs = append(txIDs, id)
	}
	if err := rows.Err(); err != nil {
		a.Fail(w, err)
		return
	}
	receipts := 0
	for i := range list {
		if !list[i].Receipt {
			continue
		}
		receipts++
		items, err := a.DB().QueryContext(ctx, `SELECT i.name, i.amount, COALESCE(i.line_id, t.line_id, 0), COALESCE(l.name, '')
			FROM bank_receipt_items i JOIN bank_transactions t ON t.id = i.transaction_id
			LEFT JOIN budget_lines l ON l.id = COALESCE(i.line_id, t.line_id) WHERE i.transaction_id = ? ORDER BY i.id`, txIDs[i])
		if err != nil {
			a.Fail(w, err)
			return
		}
		for items.Next() {
			var it monthItem
			var line int64
			if err := items.Scan(&it.Name, &it.Amount, &line, &it.Line); err != nil {
				items.Close()
				a.Fail(w, err)
				return
			}
			it.Amount *= -sign // items are positive for a purchase
			it.Here = ids[line]
			list[i].Items = append(list[i].Items, it)
		}
		items.Close()
		if err := items.Err(); err != nil {
			a.Fail(w, err)
			return
		}
	}

	prev, next := time.Date(year, time.Month(month)-1, 1, 0, 0, 0, 0, time.UTC), time.Date(year, time.Month(month)+1, 1, 0, 0, 0, 0, time.UTC)
	back := url.Values{"section": {section}, "name": names, "from": {strconv.Itoa(year)}, "to": {strconv.Itoa(year)}}
	if !current.IsDefault {
		back.Set("budget", current.Slug)
	}
	// Links to the months around, while the line exists in their year.
	around := func(t time.Time) (string, error) {
		if t.Year() != year {
			q, args := lineQuery(current, section, names, t.Year(), t.Year())
			if got, err := lineIDs(ctx, a.DB(), q, args); err != nil || len(got) == 0 {
				return "", err
			}
		}
		return lineMonthURL(current, section, names, t.Year(), int(t.Month())), nil
	}
	prevURL, err := around(prev)
	if err != nil {
		a.Fail(w, err)
		return
	}
	nextURL, err := around(next)
	if err != nil {
		a.Fail(w, err)
		return
	}
	name := strings.Join(names, " + ")
	a.Render(w, r, "bank_line_month", map[string]any{
		"Title":    name + " · " + L.T("month."+strconv.Itoa(month)) + " " + strconv.Itoa(year),
		"Name":     name,
		"Section":  section,
		"Month":    L.T("month."+strconv.Itoa(month)) + " " + strconv.Itoa(year),
		"Rows":     list,
		"Sum":      sum,
		"Receipts": receipts,
		"Back":     "/stats/line?" + back.Encode(),
		"Prev":     prevURL,
		"Next":     nextURL,
	})
}
