package app

import (
	"context"
	"database/sql"
	"errors"
	"html/template"
	"io/fs"
	"net/http"
)

// Plugin is an optional module (pension, investments, ...) compiled into the
// binary. A plugin lives in its own file and calls register from init().
// Plugins are off until an admin turns them on on the manage page; their
// migrations run either way, so turning one on or off needs no restart and
// keeps its data.
type Plugin interface {
	ID() string                          // short unique name, also namespaces its migrations
	Migrations() fs.FS                   // *.sql files applied in name order, or nil
	Templates() fs.FS                    // *.html pages, each parsed with the core's layout, or nil
	Locales() fs.FS                      // <code>.json texts, added to the core's languages, or nil
	Routes(mux *http.ServeMux, app *App) // handlers, served behind login
	Nav() []NavItem                      // menu entries
}

// LinePanels is a plugin that adds sections to a line's stats page, for the
// lines named names in section and the years from to to. Empty adds nothing.
type LinePanels interface {
	LinePanel(a *App, r *http.Request, budget BudgetRef, section string, names []string, from, to int) (template.HTML, error)
}

// linePanels are the sections the plugins that are on add to a line's stats page.
func (a *App) linePanels(r *http.Request, budget BudgetRef, section string, names []string, from, to int) ([]template.HTML, error) {
	var out []template.HTML
	for _, p := range plugins {
		lp, ok := p.(LinePanels)
		if !ok {
			continue
		}
		on, err := a.pluginOn(r.Context(), p.ID())
		if err != nil {
			return nil, err
		}
		if !on {
			continue
		}
		h, err := lp.LinePanel(a, r, budget, section, names, from, to)
		if err != nil {
			return nil, err
		}
		if h != "" {
			out = append(out, h)
		}
	}
	return out, nil
}

// LineActuals is a plugin that knows what was actually spent or earned on
// lines: year → month → amount, signed like the budget, and a name for them
// when "Actual" says too little. A line's stats page shows it against the budget.
type LineActuals interface {
	LineActuals(a *App, r *http.Request, budget BudgetRef, section string, names []string, from, to int) (map[int]map[int]Money, string, error)
}

// lineActuals are the actual amounts of the plugins that are on, added up,
// nil when none has any, and their name when only one plugin has them.
func (a *App) lineActuals(r *http.Request, budget BudgetRef, section string, names []string, from, to int) (out map[int]map[int]Money, name string, err error) {
	sources := 0
	for _, p := range plugins {
		la, ok := p.(LineActuals)
		if !ok {
			continue
		}
		on, err := a.pluginOn(r.Context(), p.ID())
		if err != nil {
			return nil, "", err
		}
		if !on {
			continue
		}
		got, label, err := la.LineActuals(a, r, budget, section, names, from, to)
		if err != nil {
			return nil, "", err
		}
		if len(got) > 0 {
			sources++
			name = label
		}
		for y, months := range got {
			for m, v := range months {
				if out == nil {
					out = map[int]map[int]Money{}
				}
				if out[y] == nil {
					out[y] = map[int]Money{}
				}
				out[y][m] += v
			}
		}
	}
	if sources > 1 {
		name = ""
	}
	return out, name, nil
}

// NavItem is a menu entry. Label is a locale key.
type NavItem struct {
	Path  string
	Label string
	Role  string // lowest role that sees it; empty for everyone
}

var plugins []Plugin

func Register(p Plugin) { plugins = append(plugins, p) }

// pluginSetting is the setting that is "1" while the plugin is on.
func pluginSetting(id string) string { return "plugin." + id }

func (a *App) pluginOn(ctx context.Context, id string) (bool, error) {
	var v string
	err := a.db.QueryRowContext(ctx, "SELECT value FROM settings WHERE name = ?", pluginSetting(id)).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return v == "1", err
}

// withPlugins serves the routes of the plugins that are on, and core otherwise.
// A request for a plugin that is off is not found.
func (a *App) withPlugins(core http.Handler) http.Handler {
	type routed struct {
		id  string
		mux *http.ServeMux
	}
	var all []routed
	for _, p := range plugins {
		mux := http.NewServeMux()
		p.Routes(mux, a)
		all = append(all, routed{p.ID(), mux})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, p := range all {
			if _, pattern := p.mux.Handler(r); pattern == "" {
				continue
			}
			on, err := a.pluginOn(r.Context(), p.id)
			if err != nil {
				a.Fail(w, err)
				return
			}
			if !on {
				http.NotFound(w, r)
				return
			}
			p.mux.ServeHTTP(w, r)
			return
		}
		core.ServeHTTP(w, r)
	})
}
