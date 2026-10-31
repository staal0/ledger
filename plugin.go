package main

import (
	"context"
	"database/sql"
	"errors"
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
	Routes(mux *http.ServeMux, app *App) // handlers, served behind login
	Nav() []NavItem                      // menu entries
}

// NavItem is a menu entry. Label is a locale key.
type NavItem struct {
	Path  string
	Label string
	Role  string // lowest role that sees it; empty for everyone
}

var plugins []Plugin

func register(p Plugin) { plugins = append(plugins, p) }

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
				a.fail(w, err)
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
