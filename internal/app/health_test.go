package app

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthzWithoutDatabase(t *testing.T) {
	db, err := sql.Open("mysql", "ledger:ledger@tcp(127.0.0.1:9)/ledger") // nothing listens on port 9
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	w := httptest.NewRecorder()
	(&App{db: db}).healthz(w, httptest.NewRequest("GET", "/healthz", nil))
	mustStatus(t, w, http.StatusServiceUnavailable)
}

func TestHealthcheckCommand(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	_, port, _ := strings.Cut(addr, ":")

	for _, a := range []string{addr, ":" + port, "0.0.0.0:" + port} {
		if code := healthcheck(a); code != 0 {
			t.Errorf("healthcheck(%q) = %d, want 0", a, code)
		}
	}
	status = http.StatusServiceUnavailable
	if healthcheck(addr) != 1 {
		t.Error("an unhealthy app should fail the healthcheck")
	}
	srv.Close()
	if healthcheck(addr) != 1 {
		t.Error("a stopped app should fail the healthcheck")
	}
}

func TestHealthz(t *testing.T) {
	a := testApp(t, true)
	mustStatus(t, request(a, "GET", "/healthz", nil, ""), http.StatusOK) // no login needed
}
