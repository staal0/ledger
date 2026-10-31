package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"path"
	"strings"
)

var coreNav = []NavItem{
	{Path: "/budget/", Label: "nav.budget"},
	{Path: "/stats", Label: "nav.stats"},
	{Path: "/manage", Label: "nav.manage", Role: RoleAdmin},
}

// parsePages pairs every template, the core's and the plugins', with
// layout.html, keyed by file name without extension.
func parsePages() (map[string]*template.Template, error) {
	funcs := template.FuncMap{"hasPrefix": strings.HasPrefix, "asset": asset}
	pages := map[string]*template.Template{}
	add := func(fsys fs.FS, pattern string) error {
		files, err := fs.Glob(fsys, pattern)
		if err != nil {
			return err
		}
		for _, f := range files {
			name := path.Base(f)
			key := strings.TrimSuffix(name, ".html")
			if name == "layout.html" {
				continue
			}
			if pages[key] != nil {
				return fmt.Errorf("two pages named %s", name)
			}
			t, err := template.New(name).Funcs(funcs).ParseFS(embedded, "templates/layout.html")
			if err == nil {
				t, err = t.ParseFS(fsys, f)
			}
			if err != nil {
				return err
			}
			pages[key] = t
		}
		return nil
	}
	if err := add(embedded, "templates/*.html"); err != nil {
		return nil, err
	}
	for _, p := range plugins {
		if fsys := p.Templates(); fsys != nil {
			if err := add(fsys, "*.html"); err != nil {
				return nil, fmt.Errorf("plugin %s: %w", p.ID(), err)
			}
		}
	}
	return pages, nil
}

// asset returns the URL of a static file with a hash of its content in the path, so a
// changed file gets a new URL and no browser or proxy cache serves the old one. The hash
// is in the path rather than the query string because some caches ignore query strings.
func asset(name string) string {
	if h, ok := assetHashes[name]; ok {
		return "/static/v/" + h + "/" + name
	}
	return "/static/" + name
}

// assetHashes holds the content hash of each static file. The files are embedded, so it is computed once.
var assetHashes = func() map[string]string {
	hashes := map[string]string{}
	files, _ := fs.Glob(embedded, "static/*")
	for _, f := range files {
		b, err := fs.ReadFile(embedded, f)
		if err != nil {
			continue
		}
		sum := sha256.Sum256(b)
		hashes[path.Base(f)] = hex.EncodeToString(sum[:4])
	}
	return hashes
}()

func (a *App) Render(w http.ResponseWriter, r *http.Request, page string, data map[string]any) {
	data["L"] = a.Locale(r)
	data["User"] = CurrentUser(r)
	data["Path"] = r.URL.Path
	data["Nav"] = a.nav(r.Context(), CurrentUser(r))
	data["LoginEnabled"] = a.loginEnabled()
	var buf bytes.Buffer
	if err := a.pages[page].ExecuteTemplate(&buf, "layout", data); err != nil {
		a.Fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

// nav is the menu for u: the core's entries and those of the plugins that are on.
func (a *App) nav(ctx context.Context, u *User) []NavItem {
	all := append([]NavItem{}, coreNav...)
	for _, p := range plugins {
		on, err := a.pluginOn(ctx, p.ID())
		if err != nil {
			log.Print(err) // the page itself still works without the plugin's entries
		}
		if on {
			all = append(all, p.Nav()...)
		}
	}
	var nav []NavItem
	for _, item := range all {
		if item.Role == "" || u.can(item.Role) {
			nav = append(nav, item)
		}
	}
	return nav
}

// DB is the app's database, for plugins.
func (a *App) DB() *sql.DB { return a.db }

func (a *App) Fail(w http.ResponseWriter, err error) {
	log.Print(err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}
