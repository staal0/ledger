package app

import (
	"io/fs"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"
)

// notesPlugin is a plugin for the tests, with a migration, a page and texts of its own.
type notesPlugin struct{}

func init() { Register(notesPlugin{}) }

func (notesPlugin) ID() string { return "notes" }

func (notesPlugin) Migrations() fs.FS {
	return fstest.MapFS{"001.sql": {Data: []byte("CREATE TABLE IF NOT EXISTS notes_items (id INT AUTO_INCREMENT PRIMARY KEY);\n")}}
}

func (notesPlugin) Templates() fs.FS {
	return fstest.MapFS{"notes.html": {Data: []byte(`{{define "content"}}<p>{{.L.T "notes.hello"}}</p>{{end}}`)}}
}

func (notesPlugin) Locales() fs.FS {
	return fstest.MapFS{
		"en.json": {Data: []byte(`{"nav.notes": "Notes", "notes.hello": "Hello from the plugin", "plugin.notes": "Notes plugin"}`)},
		"da.json": {Data: []byte(`{"nav.notes": "Noter", "notes.hello": "Hej fra plugin'et", "plugin.notes": "Noter-plugin"}`)},
	}
}

func (notesPlugin) Routes(mux *http.ServeMux, a *App) {
	mux.HandleFunc("GET /notes", func(w http.ResponseWriter, r *http.Request) {
		a.Render(w, r, "notes", map[string]any{"Title": "Notes"})
	})
	mux.HandleFunc("POST /notes", a.Only(RoleEditor, func(w http.ResponseWriter, r *http.Request) {
		if _, err := a.DB().Exec("INSERT INTO notes_items () VALUES ()"); err != nil {
			a.Fail(w, err)
			return
		}
		http.Redirect(w, r, "/notes", http.StatusSeeOther)
	}))
}

func (notesPlugin) Nav() []NavItem { return []NavItem{{Path: "/notes", Label: "nav.notes"}} }

func TestPluginSwitch(t *testing.T) {
	a := testApp(t, false)
	menu := func() bool {
		return strings.Contains(request(a, "GET", "/stats", nil, "").Body.String(), `href="/notes"`)
	}

	// Off until turned on: no menu entry and no pages.
	mustStatus(t, request(a, "GET", "/notes", nil, ""), 404)
	mustStatus(t, request(a, "POST", "/notes", url.Values{}, ""), 404)
	if menu() {
		t.Error("the menu shows a plugin that is off")
	}
	if page := request(a, "GET", "/manage", nil, "").Body.String(); !strings.Contains(page, `name="on" value="notes"`) || !strings.Contains(page, "Notes plugin") {
		t.Error("the manage page has no switch for the plugin, by its own name")
	}

	// On: its page is parsed with the layout and its texts are there.
	mustStatus(t, request(a, "POST", "/manage/plugins", url.Values{"on": {"notes"}}, ""), 303)
	page := request(a, "GET", "/notes", nil, "").Body.String()
	if !strings.Contains(page, "<p>Hello from the plugin</p>") || !strings.Contains(page, "<title>Notes · Ledger</title>") {
		t.Errorf("the plugin's page: %s", page)
	}
	if !menu() {
		t.Error("the menu lacks a plugin that is on")
	}
	mustStatus(t, request(a, "POST", "/notes", url.Values{}, ""), 303)

	// Off again: hidden, and its data, in its own table, is kept.
	mustStatus(t, request(a, "POST", "/manage/plugins", url.Values{}, ""), 303)
	mustStatus(t, request(a, "GET", "/notes", nil, ""), 404)
	if n := queryInt(t, "SELECT COUNT(*) FROM notes_items"); n != 1 {
		t.Errorf("%d items after turning the plugin off, want 1 kept", n)
	}
}
