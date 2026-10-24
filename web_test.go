package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVersionedAssets(t *testing.T) {
	a := &App{}
	url := asset("style.css")
	if !strings.HasPrefix(url, "/static/v/") {
		t.Fatalf("asset(style.css) = %q", url)
	}
	for _, tt := range []struct {
		url    string
		status int
	}{
		{url, http.StatusOK},
		{"/static/v/deadbeef/style.css", http.StatusNotFound},
		{"/static/v/" + assetHashes["style.css"] + "/missing.css", http.StatusNotFound},
	} {
		w := httptest.NewRecorder()
		a.routes().ServeHTTP(w, httptest.NewRequest("GET", tt.url, nil))
		if w.Code != tt.status {
			t.Errorf("GET %s: %d, want %d", tt.url, w.Code, tt.status)
		}
		if cc := w.Header().Get("Cache-Control"); (tt.status == http.StatusOK) != strings.Contains(cc, "immutable") {
			t.Errorf("GET %s: Cache-Control %q", tt.url, cc)
		}
	}
}
