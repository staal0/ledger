package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
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
}

// parsePages pairs every template with layout.html, keyed by file name without extension.
func parsePages() (map[string]*template.Template, error) {
	funcs := template.FuncMap{"hasPrefix": strings.HasPrefix, "asset": asset}
	files, err := fs.Glob(embedded, "templates/*.html")
	if err != nil {
		return nil, err
	}
	pages := map[string]*template.Template{}
	for _, f := range files {
		name := path.Base(f)
		if name == "layout.html" {
			continue
		}
		t, err := template.New(name).Funcs(funcs).ParseFS(embedded, "templates/layout.html", f)
		if err != nil {
			return nil, err
		}
		pages[strings.TrimSuffix(name, ".html")] = t
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

func (a *App) render(w http.ResponseWriter, r *http.Request, page string, data map[string]any) {
	data["L"] = a.locale(r)
	data["User"] = currentUser(r)
	data["Path"] = r.URL.Path
	data["Nav"] = a.nav()
	data["LoginEnabled"] = a.loginEnabled()
	var buf bytes.Buffer
	if err := a.pages[page].ExecuteTemplate(&buf, "layout", data); err != nil {
		a.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

func (a *App) nav() []NavItem {
	nav := append([]NavItem{}, coreNav...)
	for _, p := range plugins {
		nav = append(nav, p.Nav()...)
	}
	return nav
}

func (a *App) fail(w http.ResponseWriter, err error) {
	log.Print(err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}
