package bank

import (
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/staal0/ledger/internal/app"
)

type linePanelYear struct {
	Year                     int
	Budget, Actual, Receipts app.Money
	HasActual                bool
	URL                      string // budget vs. actual for the year
}

type linePanelRow struct {
	Name   string
	Count  int
	Amount app.Money
}

// LinePanel adds the actual amounts from bank and receipts to a line's stats
// page, with what the receipts on the line were spent on.
func (bankPlugin) LinePanel(core *app.App, r *http.Request, b app.BudgetRef, section string, names []string, from, to int) (template.HTML, error) {
	ctx := r.Context()
	db := core.DB()
	in := "(?" + strings.Repeat(", ?", len(names)-1) + ")"
	args := []any{b.ID, from, to, section}
	for _, n := range names {
		args = append(args, n)
	}
	lines := `SELECT l.id FROM budget_lines l JOIN budget_years y ON y.id = l.year_id
		WHERE y.budget_id = ? AND y.year BETWEEN ? AND ? AND l.section = ? AND l.name IN ` + in

	// Budget per year, and each year's line id for the link when there is one line.
	var years []*linePanelYear
	byYear := map[int]*linePanelYear{}
	rows, err := db.QueryContext(ctx, `SELECT y.year, MIN(l.id), COUNT(DISTINCT l.id), COALESCE(SUM(a.amount), 0)
		FROM budget_lines l JOIN budget_years y ON y.id = l.year_id LEFT JOIN budget_amounts a ON a.line_id = l.id
		WHERE y.budget_id = ? AND y.year BETWEEN ? AND ? AND l.section = ? AND l.name IN `+in+`
		GROUP BY y.year ORDER BY y.year`, args...)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	ids := map[int64]bool{}
	for rows.Next() {
		var y linePanelYear
		var id int64
		var n int
		if err := rows.Scan(&y.Year, &id, &n, &y.Budget); err != nil {
			return "", err
		}
		q := url.Values{"year": {strconv.Itoa(y.Year)}}
		if n == 1 {
			q.Set("line", strconv.FormatInt(id, 10))
		}
		y.URL = bankURL("/bank/compare", b, q) + "#line"
		years = append(years, &y)
		byYear[y.Year] = &y
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	idRows, err := db.QueryContext(ctx, lines, args...)
	if err != nil {
		return "", err
	}
	defer idRows.Close()
	for idRows.Next() {
		var id int64
		if err := idRows.Scan(&id); err != nil {
			return "", err
		}
		ids[id] = true
	}
	if err := idRows.Err(); err != nil {
		return "", err
	}

	sign := actualSign(section)
	found := false
	for _, y := range years {
		sums, err := lineActuals(ctx, db, b.ID, y.Year)
		if err != nil {
			return "", err
		}
		for id, months := range sums {
			if !ids[id] {
				continue
			}
			for _, v := range months {
				y.Actual += v * sign
				y.HasActual, found = true, true
			}
		}
	}
	if !found {
		return "", nil
	}

	// Receipt items count on the line they were split to, else on their receipt's line.
	onLine := ` FROM bank_receipt_items i JOIN bank_transactions t ON t.id = i.transaction_id
		WHERE COALESCE(i.line_id, t.line_id) IN (` + lines + `)`
	rows, err = db.QueryContext(ctx, `SELECT YEAR(t.booked), SUM(i.amount)`+onLine+` GROUP BY YEAR(t.booked)`, args...)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	hasReceipts := false
	for rows.Next() {
		var year int
		var sum app.Money
		if err := rows.Scan(&year, &sum); err != nil {
			return "", err
		}
		if y := byYear[year]; y != nil {
			y.Receipts = -sum * sign
			hasReceipts = true
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	top := func(group, count string, limit int) ([]linePanelRow, error) {
		rows, err := db.QueryContext(ctx, `SELECT `+group+`, `+count+`, SUM(i.amount)`+onLine+`
			GROUP BY `+group+` ORDER BY SUM(i.amount) DESC LIMIT `+strconv.Itoa(limit), args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []linePanelRow
		for rows.Next() {
			var row linePanelRow
			if err := rows.Scan(&row.Name, &row.Count, &row.Amount); err != nil {
				return nil, err
			}
			row.Amount *= -sign
			out = append(out, row)
		}
		return out, rows.Err()
	}
	var stores, items []linePanelRow
	if hasReceipts {
		if stores, err = top("t.text", "COUNT(DISTINCT t.id)", 10); err != nil {
			return "", err
		}
		if items, err = top("i.name", "COUNT(*)", 20); err != nil {
			return "", err
		}
	}
	return core.RenderPart(r, "bank_line", map[string]any{
		"Years":       years,
		"HasReceipts": hasReceipts,
		"Stores":      stores,
		"Items":       items,
	})
}
