package app

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

var themes = []string{"", "light", "dark"}

const maxStatsYears = 100

// example is the amount shown for each number format choice.
const example Money = 123456

func (a *App) settingsPage(w http.ResponseWriter, r *http.Request) {
	type numberFormat struct{ ID, Example string }
	formats := []numberFormat{{"", a.language(r).Money(example)}}
	for _, id := range []string{"comma", "point"} {
		f := numberFormats[id]
		formats = append(formats, numberFormat{id, example.Format(f[0], f[1])})
	}
	visible, err := a.VisibleBudgets(r.Context(), CurrentUser(r))
	if err != nil {
		a.Fail(w, err)
		return
	}
	start, _ := startBudget(CurrentUser(r), visible)
	a.Render(w, r, "settings", map[string]any{
		"Budgets":       Pickable(visible),
		"Start":         start.ID,
		"Title":         a.Locale(r).T("settings.title"),
		"Langs":         a.sortedLocales(),
		"Themes":        themes,
		"NumberFormats": formats,
		"Version":       Version,
		"MaxStatsYears": maxStatsYears,
	})
}

func (a *App) settingsSave(w http.ResponseWriter, r *http.Request) {
	// Empty values mean "follow the browser/system/language".
	lang, theme, nf := r.PostFormValue("lang"), r.PostFormValue("theme"), r.PostFormValue("number_format")
	_, nfOK := numberFormats[nf]
	if (lang == "" || a.locales[lang] != nil) && slices.Contains(themes, theme) && (nf == "" || nfOK) {
		_, err := a.db.ExecContext(r.Context(), "UPDATE users SET lang = ?, theme = ?, number_format = ? WHERE id = ?",
			lang, theme, nf, CurrentUser(r).ID)
		if err != nil {
			a.Fail(w, err)
			return
		}
	}
	if years, err := strconv.Atoi(r.PostFormValue("stats_years")); err == nil && years >= 1 && years <= maxStatsYears {
		if _, err := a.db.ExecContext(r.Context(), "UPDATE users SET stats_years = ? WHERE id = ?", years, CurrentUser(r).ID); err != nil {
			a.Fail(w, err)
			return
		}
	}
	// The form always posts, so an unchecked box is off.
	if _, err := a.db.ExecContext(r.Context(), "UPDATE users SET stats_changes = ?, stats_month_changes = ? WHERE id = ?",
		r.PostFormValue("stats_changes") != "", r.PostFormValue("stats_month_changes") != "", CurrentUser(r).ID); err != nil {
		a.Fail(w, err)
		return
	}
	if start, err := strconv.ParseInt(r.PostFormValue("start_budget"), 10, 64); err == nil {
		visible, err := a.VisibleBudgets(r.Context(), CurrentUser(r))
		if err != nil {
			a.Fail(w, err)
			return
		}
		if slices.ContainsFunc(visible, func(b BudgetRef) bool { return b.ID == start }) {
			if _, err := a.db.ExecContext(r.Context(), "UPDATE users SET start_budget = ? WHERE id = ?", start, CurrentUser(r).ID); err != nil {
				a.Fail(w, err)
				return
			}
		}
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// themeSave stores the header toggle. JavaScript calls it in the background;
// without JavaScript the form posts and we go back to the page it came from.
func (a *App) themeSave(w http.ResponseWriter, r *http.Request) {
	if theme := r.PostFormValue("theme"); slices.Contains(themes, theme) {
		if _, err := a.db.ExecContext(r.Context(), "UPDATE users SET theme = ? WHERE id = ?", theme, CurrentUser(r).ID); err != nil {
			a.Fail(w, err)
			return
		}
	}
	back := "/"
	if u, err := url.Parse(r.Referer()); err == nil && u.Path != "" {
		back = "/" + strings.TrimLeft(u.Path, "/") // "//host" would redirect off-site
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}
