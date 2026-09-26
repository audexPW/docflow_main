package httpapi

import (
	"errors"
	"net/http"
	"time"

	"docflow/internal/auth"
	"docflow/internal/storage"
)

type loginRequest struct {
	Login    string `json:"login"`
	Password string `json:"password"`
}

type loginResponse struct {
	Token              string    `json:"token"`
	ExpiresAt          time.Time `json:"expires_at"`
	Role               string    `json:"role"`
	MustChangePassword bool      `json:"must_change_password,omitempty"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Login == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "login and password are required")
		return
	}

	attemptKey := clientIP(r) + "|" + req.Login

	user, err := s.db.UserByLogin(r.Context(), req.Login)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			s.logins.fail(attemptKey)
			writeError(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		s.log.Error("lookup user", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	ok, err := auth.VerifyPassword(req.Password, user.PasswordHash)
	if err != nil || !ok {
		s.logins.fail(attemptKey)
		s.db.Audit(r.Context(), nil, "login_failed", "user", user.ID.String(), nil)
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	// Заблокированная учётка не должна получать токен вообще.
	if !user.IsActive {
		writeError(w, http.StatusForbidden, "account is disabled")
		return
	}
	s.logins.success(attemptKey)

	token, expiresAt, err := s.tokens.Issue(user)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not issue token")
		return
	}

	s.db.Audit(r.Context(), &user.ID, "login", "user", user.ID.String(), nil)
	writeJSON(w, http.StatusOK, loginResponse{
		Token:              token,
		ExpiresAt:          expiresAt,
		Role:               string(user.Role),
		MustChangePassword: user.MustChangePassword,
	})
}
