package bank

import (
	"context"
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"github.com/staal0/ledger/internal/app"
)

type linePanelRow struct {
	Name   string
	Count  int
	Amount app.Money
	Months []monthLink // newest first
	More   string      // how many months are left out
}

// monthLink is a month a row was bought in, linking to the line's month page.
type monthLink struct{ Label, URL string }

const shownMonths = 6

// lineQuery selects the ids of the lines named names in section, in the years from to to.
func lineQuery(b app.BudgetRef, section string, names []string, from, to int) (string, []any) {
	args := []any{b.ID, from, to, section}
	for _, n := range names {
		args = append(args, n)
	}
	return `SELECT l.id FROM budget_lines l JOIN budget_years y ON y.id = l.year_id
		WHERE y.budget_id = ? AND y.year BETWEEN ? AND ? AND l.section = ? AND l.name IN (?` + strings.Repeat(", ?", len(names)-1) + `)`, args
}

// LineActuals is what the bank and receipts have on the lines, per year and
// month, named "Receipts" when it all comes from receipts.
func (bankPlugin) LineActuals(core *app.App, r *http.Request, b app.BudgetRef, section string, names []string, from, to int) (map[int]map[int]app.Money, string, error) {
	ctx := r.Context()
	q, args := lineQuery(b, section, names, from, to)
	if f := readItemFilter(r); f.on() {
		return filteredActuals(r, core, q, args, section, f)
	}
	ids, err := lineIDs(ctx, core.DB(), q, args)
	if err != nil || len(ids) == 0 {
		return nil, "", err
	}
	// Bank rows on the lines, whole or in part: then it is not receipts alone.
	var bankRows int
	if err := core.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM bank_transactions t
		LEFT JOIN bank_splits s ON s.transaction_id = t.id AND NOT s.ignored
		WHERE t.receipt IS NULL AND COALESCE(s.line_id, t.line_id) IN (`+q+`)`, args...).Scan(&bankRows); err != nil {
		return nil, "", err
	}
	name := ""
	if bankRows == 0 {
		name = core.Locale(r).T("bank.receipts")
	}
	out := map[int]map[int]app.Money{}
	for year := from; year <= to; year++ {
		sums, err := lineActuals(ctx, core.DB(), b.ID, year)
		if err != nil {
			return nil, "", err
		}
		for id, months := range sums {
			if !ids[id] {
				continue
			}
			if out[year] == nil {
				out[year] = map[int]app.Money{}
			}
			for m, v := range months {
				out[year][m] += v * actualSign(section)
			}
		}
	}
	return out, name, nil
}

func lineIDs(ctx context.Context, q app.Querier, query string, args []any) (map[int64]bool, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids[id] = true
	}
	return ids, rows.Err()
}

// LinePanel shows what the receipts on the lines were spent on, by store and by item.
func (bankPlugin) LinePanel(core *app.App, r *http.Request, b app.BudgetRef, section string, names []string, from, to int) (template.HTML, error) {
	ctx := r.Context()
	lines, args := lineQuery(b, section, names, from, to)
	// An item counts on the line it was split to, else on its receipt's line.
	onLine := ` FROM bank_receipt_items i JOIN bank_transactions t ON t.id = i.transaction_id
		WHERE COALESCE(i.line_id, t.line_id) IN (` + lines + `)`
	filter := readItemFilter(r)
	sign := actualSign(section)
	L := core.Locale(r)
	keep := "&from=" + strconv.Itoa(from) + "&to=" + strconv.Itoa(to) + filter.params()
	top := func(group, count string, limit int, f itemFilter) ([]linePanelRow, error) {
		where, fargs := f.sql()
		rows, err := core.DB().QueryContext(ctx, `SELECT `+group+`, `+count+`, SUM(i.amount),
				GROUP_CONCAT(DISTINCT DATE_FORMAT(t.booked, '%Y-%m') ORDER BY DATE_FORMAT(t.booked, '%Y-%m') DESC)`+onLine+where+`
			GROUP BY `+group+` ORDER BY SUM(i.amount) DESC LIMIT `+strconv.Itoa(limit), append(append([]any{}, args...), fargs...)...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []linePanelRow
		for rows.Next() {
			var row linePanelRow
			var months string
			if err := rows.Scan(&row.Name, &row.Count, &row.Amount, &months); err != nil {
				return nil, err
			}
			for i, ym := range strings.Split(months, ",") {
				if i == shownMonths {
					row.More = L.T("bank.months_more", len(strings.Split(months, ","))-shownMonths)
					break
				}
				y, _ := strconv.Atoi(ym[:4])
				m, _ := strconv.Atoi(ym[5:])
				row.Months = append(row.Months, monthLink{L.T("month."+strconv.Itoa(m)) + " " + ym[:4],
					lineMonthURL(b, section, names, y, m) + keep})
			}
			row.Amount *= -sign // items are positive for a purchase
			out = append(out, row)
		}
		return out, rows.Err()
	}
	stores, err := top("t.text", "COUNT(DISTINCT t.id)", 10, itemFilter{})
	if err != nil || len(stores) == 0 {
		return "", err
	}
	items, err := top("i.name", "COUNT(*)", 20, itemFilter{Store: filter.Store})
	if err != nil {
		return "", err
	}
	// A search lists each purchase of the matching items, with its date.
	var found []foundItem
	more := false
	if filter.Query != "" {
		if found, more, err = searchItems(ctx, core.DB(), lines, args, filter); err != nil {
			return "", err
		}
		for i := range found {
			found[i].Amount *= -sign
			found[i].URL = lineMonthURL(b, section, names, found[i].Booked.Year(), int(found[i].Booked.Month())) + keep
		}
	}
	return core.RenderPart(r, "bank_line", map[string]any{"Stores": stores, "Items": items,
		"Current": b, "Section": section, "Names": names, "From": from, "To": to,
		"Query": filter.Query, "Store": filter.Store, "Found": found, "More": more, "Max": maxFound,
		"StoreURL":   func(store string) string { return lineURL(b, section, names, from, to, itemFilter{Store: store}) },
		"NoStoreURL": lineURL(b, section, names, from, to, itemFilter{Query: filter.Query}),
		"NoQueryURL": lineURL(b, section, names, from, to, itemFilter{Store: filter.Store}) + "#receipt-items"})
}
