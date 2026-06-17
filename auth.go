package main

import (
	"crypto/subtle"
	"net/http"
)

// withBasicAuth gates every request behind a single shared credential.
// Constant-time comparison avoids leaking the password via timing.
func (a *App) withBasicAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Health probe is unauthenticated so kubelet can reach it.
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		user, pass, ok := r.BasicAuth()
		if !ok || !constantEq(user, a.cfg.BasicAuthUser) || !constantEq(pass, a.cfg.BasicAuthPass) {
			w.Header().Set("WWW-Authenticate", `Basic realm="dev-env-config"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func constantEq(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// nsAllowed enforces the namespace allow-list. This is defence-in-depth on
// top of RBAC: even if RBAC were mis-scoped, the app refuses unknown namespaces.
func (a *App) nsAllowed(ns string) bool {
	for _, allowed := range a.cfg.AllowedNamespaces {
		if allowed == ns {
			return true
		}
	}
	return false
}

// kindAllowed validates the resource kind and honours the ALLOW_SECRETS switch.
func (a *App) kindAllowed(kind string) bool {
	switch kind {
	case "configmaps":
		return true
	case "secrets":
		return a.cfg.AllowSecrets
	default:
		return false
	}
}
