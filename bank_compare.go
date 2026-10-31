package main

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"
)

// bankCompareRow is a line's budget and actual amount for the period. Diff is
// positive when the line did better than budgeted: spent less, or earned more.
type bankCompareRow struct {
	Name                 string
	Budget, Actual, Diff Money
}

type bankCompareSection struct {
	Key   string
	Rows  []bankCompareRow
	Total bankCompareRow
}

// actualSign turns a sum of transactions into the line's actual amount: money
// going out is spent on expense and allocation lines, money coming in is income.
func actualSign(section string) Money {
	if section == "income" {
		return 1
	}
	return -1
}

func compareRow(section, name string, budget, actual Money) bankCompareRow {
	diff := budget - actual
	if section == "income" {
		diff = actual - budget
	}
	return bankCompareRow{name, budget, actual, diff}
}

// bankComparePage shows each line's budget beside what the assigned
// transactions add up to, for the whole year, the year to date or one month.
func (a *App) bankComparePage(w http.ResponseWriter, r *http.Request) {
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
	years, err := a.budgetYears(ctx, current.ID)
	if err != nil {
		a.fail(w, err)
		return
	}
	now := time.Now()
	year := now.Year()
	if !slices.Contains(years, year) && len(years) > 0 {
		year = years[0]
	}
	if y, err := strconv.Atoi(r.FormValue("year")); err == nil && slices.Contains(years, y) {
		year = y
	}

	// The months with imported transactions, which the comparison covers by
	// default: a budgeted month without them would look as if nothing was spent.
	imported, err := importedMonths(ctx, a.db, current.ID, year)
	if err != nil {
		a.fail(w, err)
		return
	}

	// The months compared: those imported, the year, the year to date (the
	// whole year for a past year) or one month.
	period := r.FormValue("period")
	var in []int
	span := func(from, to int) {
		for m := from; m <= to; m++ {
			in = append(in, m)
		}
	}
	switch m, err := strconv.Atoi(period); {
	case err == nil && m >= 1 && m <= 12:
		in = []int{m}
	case period == "year":
		span(1, 12)
	case period == "ytd":
		if year == now.Year() {
			span(1, int(now.Month()))
		} else {
			span(1, 12)
		}
	default:
		period = "imported"
		in = imported
	}

	page := map[string]any{
		"Title":   L.T("nav.bank"),
		"Tabs":    bankTabs(L, current, "compare"),
		"Current": current,
		"Budgets": pickable(visible),
		"Years":   years,
		"Year":    year,
		"Period":  period,
		"Months":  months,
		"In":      in,
	}
	b, err := loadBudget(ctx, a.db, current.ID, year, "")
	if err != nil {
		a.fail(w, err)
		return
	}
	if b == nil {
		a.render(w, r, "bank_compare", page)
		return
	}

	// Sums per line and month, of which the period's months are added up below.
	sums, err := lineActuals(ctx, a.db, current.ID, year)
	if err != nil {
		a.fail(w, err)
		return
	}
	actual := map[int64]Money{}
	for id, byMonth := range sums {
		for month, sum := range byMonth {
			if slices.Contains(in, month) {
				actual[id] += sum
			}
		}
	}

	var unassigned int
	var unassignedSum Money
	rows, err := a.db.QueryContext(ctx, `SELECT MONTH(t.booked), COUNT(*), SUM(t.amount) FROM bank_transactions t
		JOIN bank_accounts a ON a.id = t.account_id
		WHERE a.budget_id = ? AND YEAR(t.booked) = ? AND t.line_id IS NULL AND NOT t.ignored
		GROUP BY MONTH(t.booked)`, current.ID, year)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var month, n int
		var sum Money
		if err := rows.Scan(&month, &n, &sum); err != nil {
			a.fail(w, err)
			return
		}
		if slices.Contains(in, month) {
			unassigned += n
			unassignedSum += sum
		}
	}
	if err := rows.Err(); err != nil {
		a.fail(w, err)
		return
	}

	var list []bankCompareSection
	for _, s := range sections {
		sec := bankCompareSection{Key: s}
		var budgetSum, actualSum Money
		for _, l := range b.Lines[s] {
			var budget Money
			for _, m := range in {
				budget += l.Amounts[m]
			}
			act := actual[l.ID] * actualSign(s)
			sec.Rows = append(sec.Rows, compareRow(s, l.Name, budget, act))
			budgetSum += budget
			actualSum += act
		}
		if len(sec.Rows) > 0 {
			sec.Total = compareRow(s, L.T("stats.total"), budgetSum, actualSum)
			list = append(list, sec)
		}
	}
	page["HasYear"] = true
	page["Sections"] = list
	page["Unassigned"] = unassigned
	page["UnassignedSum"] = unassignedSum
	page["UnassignedURL"] = bankURL("/bank/transactions", current, map[string][]string{"year": {strconv.Itoa(year)}})
	page["YearURL"] = current.yearURL(year)
	page["Imported"] = imported
	page["FillURL"] = bankURL("/bank/fill", current, url.Values{"year": {strconv.Itoa(year)}})
	page["Filled"] = r.FormValue("filled")
	a.render(w, r, "bank_compare", page)
}

// importedMonths lists the months of year with transactions in the budget's accounts.
func importedMonths(ctx context.Context, q querier, budget int64, year int) ([]int, error) {
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT MONTH(t.booked) FROM bank_transactions t
		JOIN bank_accounts a ON a.id = t.account_id WHERE a.budget_id = ? AND YEAR(t.booked) = ? ORDER BY 1`, budget, year)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var m int
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// lineActuals sums the year's assigned transactions per line and month, as the
// bank has them: negative for money going out. A month is present when the
// line has transactions in it, even when they add up to 0.
func lineActuals(ctx context.Context, q querier, budget int64, year int) (map[int64]map[int]Money, error) {
	rows, err := q.QueryContext(ctx, `SELECT t.line_id, MONTH(t.booked), SUM(t.amount) FROM bank_transactions t
		JOIN bank_accounts a ON a.id = t.account_id
		WHERE a.budget_id = ? AND YEAR(t.booked) = ? AND t.line_id IS NOT NULL AND NOT t.ignored
		GROUP BY t.line_id, MONTH(t.booked)`, budget, year)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]map[int]Money{}
	for rows.Next() {
		var id int64
		var month int
		var sum Money
		if err := rows.Scan(&id, &month, &sum); err != nil {
			return nil, err
		}
		if out[id] == nil {
			out[id] = map[int]Money{}
		}
		out[id][month] = sum
	}
	return out, rows.Err()
}
