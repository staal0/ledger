package main

import (
	"io/fs"
	"net/http"
)

// Plugin is an optional module (pension, investments, ...) compiled into the
// binary. A plugin lives in its own file and calls register from init().
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
