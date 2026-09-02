package middleware

import (
	"encoding/json"
	"net/http"
)

type contextKey string

const CorrelationIDKey contextKey = "correlation_id"

func BridgeAPIKeyAuth(auth *AuthService) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !auth.ValidateBridgeAPIKey(r) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized", "code": "INVALID_API_KEY"})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
