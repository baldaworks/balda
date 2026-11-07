package managementhttp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

const (
	authorizationHeader = "Authorization"
	requestIDHeader     = "X-Request-ID"
)

type requestIDContextKey struct{}

type operatorAuthenticator struct {
	digest [sha256.Size]byte
}

func newAuthenticator(token string) (operatorAuthenticator, error) {
	if token == "" {
		return operatorAuthenticator{}, fmt.Errorf("balda.management.operator_token is required when management is enabled")
	}
	if len(token) > maxOperatorTokenBytes {
		return operatorAuthenticator{}, fmt.Errorf("balda.management.operator_token must not exceed %d bytes", maxOperatorTokenBytes)
	}
	if !validBearerToken(token) {
		return operatorAuthenticator{}, fmt.Errorf("balda.management.operator_token must be a valid bearer token")
	}
	return operatorAuthenticator{digest: sha256.Sum256([]byte(token))}, nil
}

func validBearerToken(token string) bool {
	if token == "" {
		return false
	}
	content := false
	padding := false
	for _, char := range token {
		if char == '=' {
			if !content {
				return false
			}
			padding = true
			continue
		}
		if padding {
			return false
		}
		if (char >= 'a' && char <= 'z') ||
			(char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') ||
			strings.ContainsRune("-._~+/", char) {
			content = true
			continue
		}
		return false
	}
	return content
}

func (a operatorAuthenticator) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values := r.Header.Values(authorizationHeader)
		if len(values) != 1 {
			writeError(w, r, http.StatusUnauthorized, "unauthorized", "unauthorized")
			return
		}
		const prefix = "Bearer "
		value := values[0]
		if !strings.HasPrefix(value, prefix) {
			writeError(w, r, http.StatusUnauthorized, "unauthorized", "unauthorized")
			return
		}
		token := strings.TrimPrefix(value, prefix)
		if len(token) > maxOperatorTokenBytes || !validBearerToken(token) {
			writeError(w, r, http.StatusUnauthorized, "unauthorized", "unauthorized")
			return
		}
		digest := sha256.Sum256([]byte(token))
		if subtle.ConstantTimeCompare(digest[:], a.digest[:]) != 1 {
			writeError(w, r, http.StatusUnauthorized, "unauthorized", "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := rand.Text()
		w.Header().Set(requestIDHeader, requestID)
		ctx := context.WithValue(r.Context(), requestIDContextKey{}, requestID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func requestIDFromContext(ctx context.Context) string {
	requestID, _ := ctx.Value(requestIDContextKey{}).(string)
	return requestID
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	requestID := requestIDFromContext(r.Context())
	response := managementv1.ManagementError{
		Code:    code,
		Message: message,
	}
	if requestID != "" {
		response.RequestID = &requestID
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response)
}
