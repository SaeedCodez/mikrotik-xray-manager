package handlers

import "net/http"

// Login validates the password and issues a session cookie.
func (a *App) Login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !a.auth.CheckPassword(body.Password) {
		writeError(w, http.StatusUnauthorized, "incorrect password")
		return
	}
	a.auth.SetCookie(w, a.secure(r))
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
}

// Logout clears the session cookie.
func (a *App) Logout(w http.ResponseWriter, r *http.Request) {
	a.auth.ClearCookie(w)
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": false})
}

// AuthStatus reports whether the request is authenticated.
func (a *App) AuthStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": a.auth.Authenticated(r)})
}
