package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"docflow/internal/auth"
	"docflow/internal/domain"
)

type ctxKey int

const identityKey ctxKey = iota

func identityFrom(ctx context.Context) (auth.Identity, bool) {
	id, ok := ctx.Value(identityKey).(auth.Identity)
	return id, ok
}

func recoverer(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Error("panic recovered", "error", rec, "path", r.URL.Path)
				writeError(w, http.StatusInternalServerError, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func requestLogger(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"dur_ms", time.Since(start).Milliseconds(),
		)
	})
}

func cors(origins []string, next http.Handler) http.Handler {
	allowAll := len(origins) == 1 && origins[0] == "*"
	allowed := make(map[string]bool, len(origins))
	for _, o := range origins {
		allowed[o] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && (allowAll || allowed[origin]) {
			if allowAll {
				w.Header().Set("Access-Control-Allow-Origin", "*")
			} else {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// authenticate проверяет Bearer-токен и кладёт личность в контекст.
func (s *Server) authenticate(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		token, ok := strings.CutPrefix(header, "Bearer ")
		if !ok || token == "" {
			writeError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		identity, err := s.tokens.Parse(token)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid token")
			return
		}

		// JWT сам по себе не отзывается, поэтому на каждом запросе сверяемся с
		// учётной записью: заблокирован ли пользователь и не выпущен ли токен
		// раньше смены пароля, роли или блокировки. Иначе уволенный сотрудник
		// работал бы до истечения срока действия токена.
		user, err := s.db.UserByID(r.Context(), identity.UserID)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid token")
			return
		}
		if !user.IsActive {
			writeError(w, http.StatusForbidden, "account is disabled")
			return
		}
		if identity.IssuedAt.Before(user.TokensValidFrom.Add(-time.Second)) {
			writeError(w, http.StatusUnauthorized, "token revoked, please sign in again")
			return
		}
		identity.Role = user.Role

		ctx := context.WithValue(r.Context(), identityKey, identity)
		next(w, r.WithContext(ctx))
	}
}

// requireRole ограничивает доступ по ролям (аутентификация должна стоять раньше).
func requireRole(roles ...domain.Role) func(http.HandlerFunc) http.HandlerFunc {
	allowed := make(map[domain.Role]bool, len(roles))
	for _, role := range roles {
		allowed[role] = true
	}
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			identity, ok := identityFrom(r.Context())
			if !ok || !allowed[identity.Role] {
				writeError(w, http.StatusForbidden, "insufficient permissions")
				return
			}
			next(w, r)
		}
	}
}
