package app

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"
)

// healthz is healthy only when the database answers, since the app is useless without it.
func (a *App) healthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := a.db.PingContext(ctx); err != nil {
		log.Printf("healthz: %v", err)
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	fmt.Fprint(w, "ok")
}

// healthcheck is the "ledger healthcheck" command. The image has no shell or
// curl, so its HEALTHCHECK runs the binary itself. It returns the exit code.
func healthcheck(addr string) int {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		log.Print(err)
		return 1
	}
	if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	c := http.Client{Timeout: 5 * time.Second}
	res, err := c.Get("http://" + net.JoinHostPort(host, port) + "/healthz")
	if err != nil {
		log.Print(err)
		return 1
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		log.Printf("healthz: %s", res.Status)
		return 1
	}
	return 0
}
