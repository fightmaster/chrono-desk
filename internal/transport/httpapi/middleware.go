package httpapi

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func requireAPIToken(token string, next http.Handler) http.Handler {
	wantBearer := "Bearer " + token
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("Authorization")
		authorized := subtle.ConstantTimeCompare([]byte(got), []byte(wantBearer)) == 1

		// Browser image elements cannot attach Authorization. Permit the same
		// per-process token in the query only for the localhost photo proxy.
		if !authorized && r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/photos/img") {
			authorized = subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("api_token")), []byte(token)) == 1
		}
		if !authorized {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next.ServeHTTP(w, r)
	})
}
