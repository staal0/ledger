package main

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
)

type lineYear struct {
	Year   int
	Months [13]Money // 1–12; 0 is the year total
	Deltas [13]Delta // against the same month the year before
}

// statsLinePage shows one or more lines' months across the chosen years,
// added together. A line is the same line in every year that has its section
// and name.
func (a *App) statsLinePage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	L := a.locale(r)
	section := r.FormValue("section")
	var names []string
	for _, n := range r.Form["name"] {
		if n != "" && !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	if !slices.Contains(sections, section) || len(names) == 0 {
		http.NotFound(w, r)
		return
	}
	name := strings.Join(names, " + ")
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
	args := []any{current.ID, years[0].Year, to, section}
	for _, n := range names {
		args = append(args, n)
	}
	// Each line's own months, for splitting the chart when there are several.
	parts := map[string]map[int]*[13]Money{}
	rows, err := a.db.QueryContext(ctx, `SELECT y.year, l.name, a.month, SUM(a.amount)
		FROM budget_years y
		JOIN budget_lines l ON l.year_id = y.id
		JOIN budget_amounts a ON a.line_id = l.id
		WHERE y.budget_id = ? AND y.year BETWEEN ? AND ? AND l.section = ?
			AND l.name IN (?`+strings.Repeat(", ?", len(names)-1)+`)
		GROUP BY y.year, l.name, a.month`, args...)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var year, month int
		var line string
		var sum Money
		if err := rows.Scan(&year, &line, &month, &sum); err != nil {
			a.fail(w, err)
			return
		}
		if y := byYear[year]; y != nil && month >= 1 && month <= 12 {
			y.Months[month] += sum
			y.Months[0] += sum
			if parts[line] == nil {
				parts[line] = map[int]*[13]Money{}
			}
			if parts[line][year] == nil {
				parts[line][year] = &[13]Money{}
			}
			parts[line][year][month] += sum
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

	// One bar per month; the axis names only each January's year. With several
	// lines, Parts splits each bar into the lines, in the order they were chosen.
	type part struct {
		Name   string
		Values []Money
	}
	chart := struct {
		Labels, Axis, Changes []string
		Values                []Money
		Parts                 []part
	}{}
	if len(names) > 1 {
		for _, n := range names {
			p := part{Name: n}
			for _, y := range years {
				for m := 1; m <= 12; m++ {
					var v Money
					if months := parts[n][y.Year]; months != nil {
						v = months[m]
					}
					p.Values = append(p.Values, v)
				}
			}
			chart.Parts = append(chart.Parts, p)
		}
	}
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
		"Names":    names,
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
