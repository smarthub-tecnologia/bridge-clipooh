package middleware

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/go-chi/chi/v5/middleware"
	"go.uber.org/zap"
)

type AuthService struct {
}

func NewAuthService() *AuthService {
	return &AuthService{}
}

func (s *AuthService) ValidateAdminAPIKey(r *http.Request) bool {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return false
	}

	parts := strings.Split(authHeader, " ")
	if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
		return false
	}

	token := parts[1]
	adminAPIKey := os.Getenv("ADMIN_API_KEY")
	if adminAPIKey == "" {
		zap.L().Warn("ADMIN_API_KEY is not set in environment")
		return false
	}

	return token == adminAPIKey
}

func (s *AuthService) ValidateEvolutionHMAC(r *http.Request) bool {
	secret := os.Getenv("WEBHOOK_SECRET_EVOLUTION")
	if secret == "" {
		zap.L().Warn("WEBHOOK_SECRET_EVOLUTION is not set, skipping HMAC validation")
		return true
	}

	// 1. Valida via query param ?token=SECRET (Evolution GO não envia headers de auth no webhook)
	if tokenParam := r.URL.Query().Get("token"); tokenParam != "" {
		return tokenParam == secret
	}

	// 2. Fallback: valida via header apikey
	apiKey := r.Header.Get("apikey")
	if apiKey != "" && apiKey == secret {
		return true
	}

	// 3. Fallback: HMAC Signature (X-Evolution-Signature)
	signature := r.Header.Get("X-Evolution-Signature")
	if signature == "" {
		return false
	}

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		return false
	}
	r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(bodyBytes)
	expectedMAC := hex.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(signature), []byte(expectedMAC))
}

func (s *AuthService) ValidateChatwootToken(r *http.Request) bool {
	secret := os.Getenv("WEBHOOK_SECRET_CHATWOOT")
	if secret == "" {
		return true
	}
	// Chatwoot pode enviar o token em diferentes headers dependendo da versão
	candidates := []string{
		r.Header.Get("X-Chatwoot-Token"),
		r.Header.Get("Authorization"),
		strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "),
		r.URL.Query().Get("token"),
	}
	for _, c := range candidates {
		if c == secret {
			return true
		}
	}
	return false
}

func (s *AuthService) ValidateBridgeAPIKey(r *http.Request) bool {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return false
	}

	parts := strings.Split(authHeader, " ")
	if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
		return false
	}

	token := parts[1]
	bridgeAPIKey := os.Getenv("BRIDGE_API_KEY")
	if bridgeAPIKey == "" {
		zap.L().Warn("BRIDGE_API_KEY is not set in environment")
		return false
	}

	return token == bridgeAPIKey
}

func LoggerMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := middleware.GetReqID(r.Context())
		if reqID == "" {
			reqID = r.Header.Get("X-Request-Id")
		}

		ctx := context.WithValue(r.Context(), CorrelationIDKey, reqID)

		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

		// Log de chegada — registrado ANTES do handler para capturar qualquer tráfego
		zap.L().Info("incoming request",
			zap.String("req_id", reqID),
			zap.String("method", r.Method),
			zap.String("path", r.URL.Path),
			zap.String("query", r.URL.RawQuery),
			zap.String("remote_addr", r.RemoteAddr),
		)

		next.ServeHTTP(ww, r.WithContext(ctx))

		zap.L().Info("http request completed",
			zap.String("req_id", reqID),
			zap.String("method", r.Method),
			zap.String("path", r.URL.Path),
			zap.Int("status", ww.Status()),
			zap.Int("bytes", ww.BytesWritten()),
		)
	})
}
