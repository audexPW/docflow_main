package httpapi

import (
	"net/http"
	"strings"

	"docflow/internal/auth"
	"docflow/internal/domain"

	"github.com/google/uuid"
)

type createUserRequest struct {
	Login    string `json:"login"`
	Password string `json:"password"`
	Role     string `json:"role"`
	// CompanyID — юрлицо, к которому привязывается учётка. Для клиента и
	// оператора компании это обязательный реквизит: без него человек не увидит
	// ни одного документа.
	CompanyID string `json:"company_id"`
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req createUserRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Login = strings.TrimSpace(req.Login)
	role := domain.Role(strings.TrimSpace(req.Role))
	if req.Login == "" || len(req.Password) < 8 {
		writeError(w, http.StatusBadRequest, "login required and password must be at least 8 characters")
		return
	}
	if !role.Valid() {
		writeError(w, http.StatusBadRequest, "роль: client, operator, accountant или admin")
		return
	}

	var companyID *uuid.UUID
	if id := strings.TrimSpace(req.CompanyID); id != "" {
		cid, err := uuid.Parse(id)
		if err != nil {
			writeError(w, http.StatusBadRequest, "некорректный идентификатор компании")
			return
		}
		if _, err := s.db.Company(r.Context(), cid); err != nil {
			writeError(w, http.StatusBadRequest, "компания не найдена")
			return
		}
		companyID = &cid
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not hash password")
		return
	}

	user, err := s.db.CreateUserInCompany(r.Context(), req.Login, hash, role, companyID)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate") {
			writeError(w, http.StatusConflict, "login already exists")
			return
		}
		s.log.Error("create user", "error", err)
		writeError(w, http.StatusInternalServerError, "could not create user")
		return
	}

	identity, _ := identityFrom(r.Context())
	s.db.Audit(r.Context(), &identity.UserID, "create_user", "user", user.ID.String(),
		map[string]any{"login": user.Login, "role": user.Role})
	writeJSON(w, http.StatusCreated, user)
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.db.ListUsers(r.Context())
	if err != nil {
		s.log.Error("list users", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": users})
}

func (s *Server) handleListAudit(w http.ResponseWriter, r *http.Request) {
	limit := atoiDefault(r.URL.Query().Get("limit"), 100)
	entries, err := s.db.ListAudit(r.Context(), limit)
	if err != nil {
		s.log.Error("list audit", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": entries})
}
