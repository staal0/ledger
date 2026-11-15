package bank

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/staal0/ledger/internal/app"
)

const maxFound = 200

// foundItem is a receipt item a search found, with the month it opens.
type foundItem struct {
	Booked time.Time
	Store  string
	Name   string
	Amount app.Money
	URL    string
}

// likePattern is a search as SQL LIKE: contains, with * for any text.
func likePattern(q string) string {
	q = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)
	return "%" + strings.ReplaceAll(q, "*", "%") + "%"
}

// itemFilter narrows a line's receipt items to one store and to names like Query.
type itemFilter struct{ Store, Query string }

func readItemFilter(r *http.Request) itemFilter {
	f := itemFilter{Store: r.FormValue("store"), Query: strings.Join(strings.Fields(r.FormValue("q")), " ")}
	if specificity(f.Query) == 0 {
		f.Query = ""
	}
	return f
}

func (f itemFilter) on() bool { return f.Store != "" || f.Query != "" }

// params is the filter for a link's query, after other parameters.
func (f itemFilter) params() string {
	q := url.Values{}
	if f.Store != "" {
		q.Set("store", f.Store)
	}
	if f.Query != "" {
		q.Set("q", f.Query)
	}
	if len(q) == 0 {
		return ""
	}
	return "&" + q.Encode()
}

// label names what the filter keeps, such as "from Store with “coffee”".
func (f itemFilter) label(L *app.Locale) string {
	var parts []string
	if f.Store != "" {
		parts = append(parts, L.T("bank.filter_store", f.Store))
	}
	if f.Query != "" {
		parts = append(parts, L.T("bank.filter_query", f.Query))
	}
	return strings.Join(parts, " ")
}

// sql is the filter as conditions on items i of transactions t.
func (f itemFilter) sql() (string, []any) {
	where, args := "", []any{}
	if f.Store != "" {
		where, args = where+" AND t.text = ?", append(args, f.Store)
	}
	if f.Query != "" {
		where, args = where+" AND i.name LIKE ?", append(args, likePattern(f.Query))
	}
	return where, args
}

// lineURL is a line's stats page with the filter.
func lineURL(b app.BudgetRef, section string, names []string, from, to int, f itemFilter) string {
	q := url.Values{"section": {section}, "name": names, "from": {strconv.Itoa(from)}, "to": {strconv.Itoa(to)}}
	if f.Store != "" {
		q.Set("store", f.Store)
	}
	if f.Query != "" {
		q.Set("q", f.Query)
	}
	if !b.IsDefault {
		q.Set("budget", b.Slug)
	}
	return "/stats/line?" + q.Encode()
}

// filteredActuals are the filtered receipt items on the lines, per year and month, named by the filter.
func filteredActuals(r *http.Request, core *app.App, lines string, args []any, section string, f itemFilter) (map[int]map[int]app.Money, string, error) {
	where, fargs := f.sql()
	rows, err := core.DB().QueryContext(r.Context(), `SELECT YEAR(t.booked), MONTH(t.booked), SUM(i.amount)
		FROM bank_receipt_items i JOIN bank_transactions t ON t.id = i.transaction_id
		WHERE COALESCE(i.line_id, t.line_id) IN (`+lines+`)`+where+` GROUP BY YEAR(t.booked), MONTH(t.booked)`,
		append(append([]any{}, args...), fargs...)...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := map[int]map[int]app.Money{}
	for rows.Next() {
		var y, m int
		var sum app.Money
		if err := rows.Scan(&y, &m, &sum); err != nil {
			return nil, "", err
		}
		if out[y] == nil {
			out[y] = map[int]app.Money{}
		}
		out[y][m] = -sum * actualSign(section) // items are positive for a purchase
	}
	L := core.Locale(r)
	return out, L.T("bank.receipts") + " " + f.label(L), rows.Err()
}

// searchItems finds the items on the lines the query lines selects, by the
// filter, newest first; more is true when there are more than it returns.
func searchItems(ctx context.Context, q app.Querier, lines string, args []any, f itemFilter) (found []foundItem, more bool, err error) {
	where, fargs := f.sql()
	rows, err := q.QueryContext(ctx, `SELECT t.booked, t.text, i.name, i.amount
		FROM bank_receipt_items i JOIN bank_transactions t ON t.id = i.transaction_id
		WHERE COALESCE(i.line_id, t.line_id) IN (`+lines+`)`+where+`
		ORDER BY t.booked DESC, i.id LIMIT ?`, append(append(append([]any{}, args...), fargs...), maxFound+1)...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var f foundItem
		if err := rows.Scan(&f.Booked, &f.Store, &f.Name, &f.Amount); err != nil {
			return nil, false, err
		}
		found = append(found, f)
	}
	if len(found) > maxFound {
		return found[:maxFound], true, rows.Err()
	}
	return found, false, rows.Err()
}
