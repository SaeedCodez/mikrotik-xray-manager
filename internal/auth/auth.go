// Package auth provides simple HMAC-signed cookie sessions for a single user.
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	cookieName = "xray_session"
	sessionTTL = 7 * 24 * time.Hour
)

// Manager issues and validates session cookies.
type Manager struct {
	password string
	secret   []byte
}

// New returns a session Manager. An empty password means any input unlocks.
func New(password string, secret []byte) *Manager {
	return &Manager{password: password, secret: secret}
}

// CheckPassword reports whether pw matches the configured password.
func (m *Manager) CheckPassword(pw string) bool {
	if m.password == "" {
		return true // demo / unconfigured mode
	}
	return subtle.ConstantTimeCompare([]byte(pw), []byte(m.password)) == 1
}

// token returns a "<expiryUnix>.<hmac>" string signed with the secret.
func (m *Manager) token() string {
	exp := time.Now().Add(sessionTTL).Unix()
	payload := strconv.FormatInt(exp, 10)
	return payload + "." + m.sign(payload)
}

func (m *Manager) sign(payload string) string {
	mac := hmac.New(sha256.New, m.secret)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// valid reports whether a token is well-formed, correctly signed, and unexpired.
func (m *Manager) valid(tok string) bool {
	parts := strings.SplitN(tok, ".", 2)
	if len(parts) != 2 {
		return false
	}
	payload, sig := parts[0], parts[1]
	if !hmac.Equal([]byte(sig), []byte(m.sign(payload))) {
		return false
	}
	exp, err := strconv.ParseInt(payload, 10, 64)
	if err != nil {
		return false
	}
	return time.Now().Unix() < exp
}

// SetCookie writes a fresh signed session cookie onto the response.
func (m *Manager) SetCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    m.token(),
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(sessionTTL),
		MaxAge:   int(sessionTTL.Seconds()),
	})
}

// ClearCookie expires the session cookie.
func (m *Manager) ClearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
}

// Authenticated reports whether the request carries a valid session cookie.
func (m *Manager) Authenticated(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	return m.valid(c.Value)
}

// Middleware guards a handler, returning 401 JSON when unauthenticated.
func (m *Manager) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !m.Authenticated(r) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":"unauthorized"}`)
			return
		}
		next.ServeHTTP(w, r)
	})
}
