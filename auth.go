package main

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const (
	sessionCookie = "ledger_session"
	authCookie    = "ledger_auth"
	sessionTTL    = 30 * 24 * time.Hour
)

type User struct {
	ID    int64
	Name  string
	Email string
	Lang  string
	Theme string // "", "light" or "dark"; empty follows the system
	// NumberFormat is a key of numberFormats; empty follows the language.
	NumberFormat string
	Role         string // set at login, see roles.go
}

type ctxKey struct{}

func currentUser(r *http.Request) *User {
	u, _ := r.Context().Value(ctxKey{}).(*User)
	return u
}

type oidcClient struct {
	mu       sync.Mutex
	provider *oidc.Provider
	oauth    *oauth2.Config
	verifier *oidc.IDTokenVerifier
}

// oidc discovers the provider on first use, so the app starts even when the
// identity provider is down.
func (a *App) oidc(ctx context.Context) (*oauth2.Config, *oidc.IDTokenVerifier, error) {
	c := &a.auth
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.oauth == nil {
		p, err := oidc.NewProvider(ctx, a.cfg.OIDCIssuer)
		if err != nil {
			return nil, nil, err
		}
		scopes := []string{oidc.ScopeOpenID, "profile", "email"}
		if a.cfg.rolesConfigured() && a.cfg.GroupsScope != "" {
			scopes = append(scopes, a.cfg.GroupsScope)
		}
		c.provider = p
		c.oauth = &oauth2.Config{
			ClientID:     a.cfg.OIDCClientID,
			ClientSecret: a.cfg.OIDCSecret,
			Endpoint:     p.Endpoint(),
			RedirectURL:  a.cfg.BaseURL + "/auth/callback",
			Scopes:       scopes,
		}
		c.verifier = p.Verifier(&oidc.Config{ClientID: a.cfg.OIDCClientID})
	}
	return c.oauth, c.verifier, nil
}

// loginEnabled is false in local mode, where everyone is the "local" user.
func (a *App) loginEnabled() bool { return a.cfg.OIDCIssuer != "" }

func (a *App) login(w http.ResponseWriter, r *http.Request) {
	if !a.loginEnabled() {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	oc, _, err := a.oidc(r.Context())
	if err != nil {
		log.Printf("oidc discovery: %v", err)
		http.Error(w, "login provider unavailable", http.StatusServiceUnavailable)
		return
	}
	state, nonce, verifier := rand.Text(), rand.Text(), oauth2.GenerateVerifier()
	a.setCookie(w, authCookie, state+"|"+nonce+"|"+verifier, "/auth", 10*time.Minute)
	http.Redirect(w, r, oc.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), http.StatusFound)
}

func (a *App) callback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, err := r.Cookie(authCookie)
	if err != nil {
		http.Error(w, "login expired, please try again", http.StatusBadRequest)
		return
	}
	a.setCookie(w, authCookie, "", "/auth", -time.Second)
	if e := r.FormValue("error"); e != "" {
		log.Printf("oidc: the provider refused the login: %s: %s", e, r.FormValue("error_description"))
		http.Error(w, "login failed: "+cmp.Or(r.FormValue("error_description"), e), http.StatusUnauthorized)
		return
	}
	parts := strings.SplitN(c.Value, "|", 3)
	if len(parts) != 3 || r.FormValue("state") != parts[0] {
		http.Error(w, "invalid login state", http.StatusBadRequest)
		return
	}
	oc, verifier, err := a.oidc(ctx)
	if err != nil {
		http.Error(w, "login provider unavailable", http.StatusServiceUnavailable)
		return
	}
	tok, err := oc.Exchange(ctx, r.FormValue("code"), oauth2.VerifierOption(parts[2]))
	if err != nil {
		log.Printf("oidc exchange: %v", err)
		http.Error(w, "login failed", http.StatusUnauthorized)
		return
	}
	raw, _ := tok.Extra("id_token").(string)
	idt, err := verifier.Verify(ctx, raw)
	if err == nil && idt.Nonce != parts[1] {
		err = errors.New("nonce does not match")
	}
	if err != nil {
		log.Printf("oidc verify: %v", err)
		http.Error(w, "login failed", http.StatusUnauthorized)
		return
	}
	var claims struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	if err := idt.Claims(&claims); err != nil {
		a.fail(w, err)
		return
	}
	role, err := a.loginRole(ctx, idt, tok)
	if err != nil {
		a.fail(w, err)
		return
	}
	if role == "" {
		log.Printf("login refused: %s (%s) is in none of the role groups", claims.Name, claims.Email)
		http.Error(w, a.locale(r).T("auth.no_access"), http.StatusForbidden)
		return
	}

	res, err := a.db.ExecContext(ctx, `INSERT INTO users (subject, name, email, role) VALUES (?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE id = LAST_INSERT_ID(id), name = ?, email = ?, role = ?`,
		idt.Subject, claims.Name, claims.Email, role, claims.Name, claims.Email, role)
	if err != nil {
		a.fail(w, err)
		return
	}
	userID, _ := res.LastInsertId()
	token := rand.Text()
	_, err = a.db.ExecContext(ctx, `INSERT INTO sessions (token_hash, user_id, expires_at)
		VALUES (?, ?, UTC_TIMESTAMP() + INTERVAL 30 DAY)`, hashToken(token), userID)
	if err != nil {
		a.fail(w, err)
		return
	}
	if _, err := a.db.ExecContext(ctx, "DELETE FROM sessions WHERE expires_at < UTC_TIMESTAMP()"); err != nil {
		log.Printf("removing expired sessions: %v", err) // housekeeping only; the login itself worked
	}
	a.setCookie(w, sessionCookie, token, "/", sessionTTL)
	http.Redirect(w, r, "/", http.StatusFound)
}

// loginRole is the role for the user's groups, taken from the ID token or, as
// some providers only send groups there, from the userinfo endpoint.
func (a *App) loginRole(ctx context.Context, idt *oidc.IDToken, tok *oauth2.Token) (string, error) {
	if !a.cfg.rolesConfigured() {
		return roleAdmin, nil
	}
	var claims map[string]any
	if err := idt.Claims(&claims); err != nil {
		return "", err
	}
	groups := claimStrings(claims, a.cfg.GroupsClaim)
	if groups == nil {
		info, err := a.auth.provider.UserInfo(ctx, oauth2.StaticTokenSource(tok))
		if err != nil {
			return "", err
		}
		claims = nil
		if err := info.Claims(&claims); err != nil {
			return "", err
		}
		groups = claimStrings(claims, a.cfg.GroupsClaim)
	}
	return a.cfg.roleFor(groups), nil
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		// If this fails the session stays valid, so say so instead of "logged out".
		if _, err := a.db.ExecContext(r.Context(), "DELETE FROM sessions WHERE token_hash = ?", hashToken(c.Value)); err != nil {
			a.fail(w, err)
			return
		}
	}
	a.setCookie(w, sessionCookie, "", "/", -time.Second)
	http.Redirect(w, r, "/logged-out", http.StatusSeeOther)
}

func (a *App) loggedOut(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, "logged_out", map[string]any{"Title": a.locale(r).T("logged_out.title")})
}

func (a *App) requireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var row *sql.Row
		if a.loginEnabled() {
			c, err := r.Cookie(sessionCookie)
			if err != nil {
				http.Redirect(w, r, "/login", http.StatusFound)
				return
			}
			row = a.db.QueryRowContext(r.Context(), `SELECT u.id, u.name, u.email, u.lang, u.theme, u.number_format, u.role
				FROM sessions s JOIN users u ON u.id = s.user_id
				WHERE s.token_hash = ? AND s.expires_at > UTC_TIMESTAMP()`, hashToken(c.Value))
		} else {
			row = a.db.QueryRowContext(r.Context(), "SELECT id, name, email, lang, theme, number_format, role FROM users WHERE subject = 'local'")
		}
		u := &User{}
		err := row.Scan(&u.ID, &u.Name, &u.Email, &u.Lang, &u.Theme, &u.NumberFormat, &u.Role)
		if errors.Is(err, sql.ErrNoRows) {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		if err != nil {
			a.fail(w, err)
			return
		}
		switch {
		case !a.loginEnabled() || !a.cfg.rolesConfigured():
			u.Role = roleAdmin
		case u.Role == "": // a session from before roles were set up
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, u)))
	})
}

func (a *App) setCookie(w http.ResponseWriter, name, value, path string, maxAge time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     path,
		MaxAge:   int(maxAge.Seconds()),
		HttpOnly: true,
		Secure:   strings.HasPrefix(a.cfg.BaseURL, "https://"),
		SameSite: http.SameSiteLaxMode,
	})
}

// hashToken means a leaked database does not leak usable session cookies.
func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}
