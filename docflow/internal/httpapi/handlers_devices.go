package httpapi

import (
	"net/http"
	"strings"
)

type deviceRequest struct {
	Platform string `json:"platform"` // android | ios | web
	Token    string `json:"token"`
}

var allowedPlatforms = map[string]bool{"android": true, "ios": true, "web": true}

// handleRegisterDevice привязывает push-токен устройства к текущему пользователю.
func (s *Server) handleRegisterDevice(w http.ResponseWriter, r *http.Request) {
	identity, _ := identityFrom(r.Context())

	var req deviceRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Platform = strings.ToLower(strings.TrimSpace(req.Platform))
	req.Token = strings.TrimSpace(req.Token)
	if req.Token == "" || !allowedPlatforms[req.Platform] {
		writeError(w, http.StatusBadRequest, "platform (android|ios|web) and token are required")
		return
	}

	if err := s.db.UpsertDeviceToken(r.Context(), identity.UserID, req.Platform, req.Token); err != nil {
		s.log.Error("register device", "error", err)
		writeError(w, http.StatusInternalServerError, "could not register device")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleUnregisterDevice удаляет токен (например, при выходе из аккаунта).
func (s *Server) handleUnregisterDevice(w http.ResponseWriter, r *http.Request) {
	var req deviceRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Token = strings.TrimSpace(req.Token); req.Token == "" {
		writeError(w, http.StatusBadRequest, "token is required")
		return
	}
	if err := s.db.DeleteDeviceToken(r.Context(), req.Token); err != nil {
		s.log.Error("unregister device", "error", err)
		writeError(w, http.StatusInternalServerError, "could not unregister device")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
