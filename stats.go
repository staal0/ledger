package main

import (
	"cmp"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

// Delta is a formatted change between two years. Class is "good", "bad" or
// "" and only colours the text; the arrow and sign carry the meaning.
type Delta struct {
	Text  string
	Class string
}

// delta compares cur with prev. upGood is 1 when a rise is good (income),
// -1 when it is bad (expenses) and 0 when it is neither.
func (l *Locale) delta(cur, prev Money, upGood int) Delta {
	if prev == 0 {
		return Delta{Text: "–"}
	}
	p := float64(cur-prev) / float64(abs(prev)) * 100
	s := strings.Replace(fmt.Sprintf("%+.1f", p), ".", l.Decimal, 1)
	d := Delta{Text: l.T("stats.percent", s)}
	switch {
	case p > 0:
		d.Text = "▲ " + d.Text
	case p < 0:
		d.Text = "▼ " + d.Text
	}
	if p != 0 && upGood != 0 {
		if (p > 0) == (upGood > 0) {
			d.Class = "good"
		} else {
			d.Class = "bad"
		}
	}
	return d
}

func abs(m Money) Money {
	if m < 0 {
		return -m
	}
	return m
}

type yearStats struct {
	Year                                int
	Income, Expense, Net, Allocation    Money
	IncomeDelta, ExpenseDelta, NetDelta Delta
	AllocationDelta                     Delta
}

type lineStats struct {
	Name   string
	Values []Money // one per year, in the order of the page's years
	Delta  Delta   // newest year against the one before
	latest Money
	total  Money
}

// defaultSpan is how many years the statistics show unless a range is chosen.
const defaultSpan = 10

// yearRange reads ?from= and ?to=, limited to the years that have a budget.
// Without them it is the newest defaultSpan years.
func yearRange(r *http.Request, all []int) (from, to int) {
	if len(all) == 0 {
		return 0, 0
	}
	first, last := all[0], all[len(all)-1]
	to = last
	if v, err := strconv.Atoi(r.FormValue("to")); err == nil {
		to = min(max(v, first), last)
	}
	from = max(first, to-defaultSpan+1)
	if v, err := strconv.Atoi(r.FormValue("from")); err == nil {
		from = min(max(v, first), last)
	}
	if from > to {
		from, to = to, from
	}
	return from, to
}

func (a *App) statsPage(w http.ResponseWriter, r *http.Request) {
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
	budget := current.ID
	all, err := a.budgetYears(ctx, budget)
	if err != nil {
		a.fail(w, err)
		return
	}
	slices.Reverse(all) // oldest first
	from, to := yearRange(r, all)

	// Totals per year, section and line. The year before the range is read
	// too, only so the first year in the range gets a change.
	rows, err := a.db.QueryContext(ctx, `SELECT y.year, l.section, l.name, SUM(a.amount)
		FROM budget_years y
		JOIN budget_lines l ON l.year_id = y.id
		JOIN budget_amounts a ON a.line_id = l.id
		WHERE y.budget_id = ? AND y.year BETWEEN ? AND ?
		GROUP BY y.year, l.section, l.name
		ORDER BY y.year`, budget, from-1, to)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer rows.Close()
	var years []*yearStats
	perLine := map[string]map[string]map[int]Money{"expense": {}, "allocation": {}} // section → name → year → sum
	for rows.Next() {
		var year int
		var section, name string
		var sum Money
		if err := rows.Scan(&year, &section, &name, &sum); err != nil {
			a.fail(w, err)
			return
		}
		if len(years) == 0 || years[len(years)-1].Year != year {
			years = append(years, &yearStats{Year: year})
		}
		y := years[len(years)-1]
		switch section {
		case "income":
			y.Income += sum
		case "expense":
			y.Expense += sum
		case "allocation":
			y.Allocation += sum
		}
		if byName := perLine[section]; byName != nil {
			if byName[name] == nil {
				byName[name] = map[int]Money{}
			}
			byName[name][year] += sum
		}
	}
	if err := rows.Err(); err != nil {
		a.fail(w, err)
		return
	}

	prev := &yearStats{} // all zero for the first year, which shows "–"
	for _, y := range years {
		y.Net = y.Income - y.Expense
		y.IncomeDelta = L.delta(y.Income, prev.Income, 1)
		y.ExpenseDelta = L.delta(y.Expense, prev.Expense, -1)
		y.NetDelta = L.delta(y.Net, prev.Net, 1)
		y.AllocationDelta = L.delta(y.Allocation, prev.Allocation, 0)
		prev = y
	}
	// The newest year is compared with the one before it, which may lie
	// before the range when the range is a single year.
	compareTo := 0
	if n := len(years); n > 1 {
		compareTo = years[n-2].Year
	}
	if len(years) > 0 && years[0].Year < from {
		years = years[1:]
	}

	// Lines across years, largest in the newest year first.
	lines := func(section string, upGood int) []*lineStats {
		var out []*lineStats
		for name, byYear := range perLine[section] {
			ls := &lineStats{Name: name}
			for _, y := range years {
				v := byYear[y.Year]
				ls.Values = append(ls.Values, v)
				ls.total += v
			}
			if ls.total == 0 {
				continue // only in the year before the range
			}
			if n := len(years); n > 0 {
				ls.latest = byYear[years[n-1].Year]
				if compareTo != 0 {
					ls.Delta = L.delta(ls.latest, byYear[compareTo], upGood)
				}
			}
			out = append(out, ls)
		}
		slices.SortFunc(out, func(x, y *lineStats) int {
			return cmp.Or(cmp.Compare(y.latest, x.latest), cmp.Compare(y.total, x.total), strings.Compare(x.Name, y.Name))
		})
		return out
	}

	// Raw numbers for the chart, in øre.
	chart := struct {
		Years           []int
		Income, Expense []Money
	}{}
	for _, y := range years {
		chart.Years = append(chart.Years, y.Year)
		chart.Income = append(chart.Income, y.Income)
		chart.Expense = append(chart.Expense, y.Expense)
	}

	a.render(w, r, "stats", map[string]any{
		"Title":    L.T("nav.stats"),
		"AllYears": all,
		"Base":     current.Base(),
		"Current":  current,
		"Budgets":  pickable(visible),
		"From":     from,
		"To":       to,
		"Years":    years,
		"LineTables": []struct {
			Key   string
			Lines []*lineStats
		}{{"expense", lines("expense", -1)}, {"allocation", lines("allocation", 0)}},
		"Chart": chart,
	})
}
