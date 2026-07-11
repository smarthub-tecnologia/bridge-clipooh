package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"go.uber.org/zap"
)

type contextKey string

const CorrelationIDKey contextKey = "correlation_id"

func AdminAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}

		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
			http.Error(w, `{"error":"invalid authorization format"}`, http.StatusUnauthorized)
			return
		}

		token := parts[1]
		adminAPIKey := os.Getenv("ADMIN_API_KEY")
		if adminAPIKey == "" {
			zap.L().Warn("ADMIN_API_KEY is not set in environment")
			http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
			return
		}

		if token != adminAPIKey {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}

		// Inject correlation_id for tracing
		corrID := r.Header.Get("X-Correlation-ID")
		if corrID == "" {
			corrID = r.Header.Get("X-Request-Id") // From chi middleware
		}
		ctx := context.WithValue(r.Context(), CorrelationIDKey, corrID)
		
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

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
