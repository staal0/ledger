package main

import (
	"net/http"
	"slices"
	"strconv"
)

type lineYear struct {
	Year   int
	Months [13]Money // 1–12; 0 is the year total
	Deltas [13]Delta // against the same month the year before
}

// statsLinePage shows one line's months across the chosen years. A line is
// the same line in every year that has its section and name.
func (a *App) statsLinePage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	L := a.locale(r)
	section, name := r.FormValue("section"), r.FormValue("name")
	if !slices.Contains(sections, section) || name == "" {
		http.NotFound(w, r)
		return
	}
	current, _, ok, err := a.chosenBudget(r)
	if err != nil {
		a.fail(w, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	all, err := a.budgetYears(ctx, current.ID)
	if err != nil {
		a.fail(w, err)
		return
	}
	slices.Reverse(all) // oldest first
	from, to := yearRange(r, all, currentUser(r).StatsYears)

	// The year before the range is read only so the first year gets a change.
	var years []*lineYear
	for _, y := range all {
		if y > to {
			break
		}
		if y < from {
			years = years[:0]
		}
		years = append(years, &lineYear{Year: y})
	}
	if len(years) == 0 {
		http.NotFound(w, r)
		return
	}
	byYear := map[int]*lineYear{}
	for _, y := range years {
		byYear[y.Year] = y
	}
	rows, err := a.db.QueryContext(ctx, `SELECT y.year, a.month, SUM(a.amount)
		FROM budget_years y
		JOIN budget_lines l ON l.year_id = y.id
		JOIN budget_amounts a ON a.line_id = l.id
		WHERE y.budget_id = ? AND y.year BETWEEN ? AND ? AND l.section = ? AND l.name = ?
		GROUP BY y.year, a.month`, current.ID, years[0].Year, to, section, name)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var year, month int
		var sum Money
		if err := rows.Scan(&year, &month, &sum); err != nil {
			a.fail(w, err)
			return
		}
		if y := byYear[year]; y != nil && month >= 1 && month <= 12 {
			y.Months[month] += sum
			y.Months[0] += sum
		}
	}
	if err := rows.Err(); err != nil {
		a.fail(w, err)
		return
	}

	upGood := map[string]int{"income": 1, "expense": -1}[section]
	for i, y := range years {
		for m := range y.Months {
			y.Deltas[m] = Delta{Text: "–"}
			if i > 0 {
				y.Deltas[m] = L.delta(y.Months[m], years[i-1].Months[m], upGood)
			}
		}
	}
	if years[0].Year < from {
		years = years[1:]
	}

	// One bar per month; the axis names only each January's year.
	chart := struct {
		Labels, Axis, Changes []string
		Values                []Money
	}{}
	for _, y := range years {
		for m := 1; m <= 12; m++ {
			chart.Labels = append(chart.Labels, L.T("month."+strconv.Itoa(m))+" "+strconv.Itoa(y.Year))
			axis := ""
			if m == 1 {
				axis = strconv.Itoa(y.Year)
			}
			chart.Axis = append(chart.Axis, axis)
			chart.Values = append(chart.Values, y.Months[m])
			chart.Changes = append(chart.Changes, y.Deltas[m].Text)
		}
	}

	a.render(w, r, "stats_line", map[string]any{
		"Title":    name,
		"Name":     name,
		"Section":  section,
		"AllYears": all,
		"Base":     current.Base(),
		"Current":  current,
		"From":     from,
		"To":       to,
		"Years":    years,
		"Months":   months,
		"Chart":    chart,
	})
}
