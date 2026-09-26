package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"docflow/internal/auth"
	"docflow/internal/domain"
	"docflow/internal/storage"

	"github.com/google/uuid"
)

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// handleChangeOwnPassword — смена собственного пароля. Доступна главбуху и
// администратору. Клиенту и оператору пароль назначает администратор:
// POST /api/users/{id}/password.
func (s *Server) handleChangeOwnPassword(w http.ResponseWriter, r *http.Request) {
	identity, _ := identityFrom(r.Context())

	// Проверка дублирует список ролей в маршруте намеренно: если маршрут
	// когда-нибудь откроют шире, запрет останется здесь.
	if identity.Role == domain.RoleClient || identity.Role == domain.RoleOperator {
		writeError(w, http.StatusForbidden,
			"смену пароля выполняет администратор — обратитесь к нему")
		return
	}

	var req changePasswordRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := validatePassword(req.NewPassword); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	user, err := s.db.UserByID(r.Context(), identity.UserID)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token")
		return
	}
	ok, err := auth.VerifyPassword(req.CurrentPassword, user.PasswordHash)
	if err != nil || !ok {
		writeError(w, http.StatusUnauthorized, "current password is incorrect")
		return
	}
	if req.CurrentPassword == req.NewPassword {
		writeError(w, http.StatusBadRequest, "new password must differ from the current one")
		return
	}

	hash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not hash password")
		return
	}
	if err := s.db.SetPassword(r.Context(), user.ID, hash); err != nil {
		s.log.Error("set password", "error", err)
		writeError(w, http.StatusInternalServerError, "could not change password")
		return
	}

	s.db.Audit(r.Context(), &user.ID, "change_password", "user", user.ID.String(), nil)
	// Все прежние токены только что аннулированы — выдаём новый, чтобы
	// пользователя не выбрасывало из интерфейса сразу после смены пароля.
	user.MustChangePassword = false
	token, expiresAt, err := s.tokens.Issue(user)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "reauth_required": true})
		return
	}
	writeJSON(w, http.StatusOK, loginResponse{Token: token, ExpiresAt: expiresAt, Role: string(user.Role)})
}

type adminSetPasswordRequest struct {
	NewPassword string `json:"new_password"`
}

// handleAdminSetPassword — сброс пароля администратором (сотрудник забыл свой).
func (s *Server) handleAdminSetPassword(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r)
	if !ok {
		return
	}
	var req adminSetPasswordRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := validatePassword(req.NewPassword); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	hash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not hash password")
		return
	}
	if err := s.db.SetPassword(r.Context(), id, hash); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		s.log.Error("admin set password", "error", err)
		writeError(w, http.StatusInternalServerError, "could not set password")
		return
	}

	actor, _ := identityFrom(r.Context())
	s.db.Audit(r.Context(), &actor.UserID, "reset_password", "user", id.String(), nil)
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

type setActiveRequest struct {
	IsActive *bool `json:"is_active"`
}

// handleSetUserActive блокирует и разблокирует учётную запись. Удаления нет
// намеренно: за пользователем числятся документы и записи аудита, их нельзя
// осиротить. Блокировка выгоняет пользователя немедленно.
func (s *Server) handleSetUserActive(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r)
	if !ok {
		return
	}
	var req setActiveRequest
	if err := decodeJSON(r, &req); err != nil || req.IsActive == nil {
		writeError(w, http.StatusBadRequest, "is_active is required")
		return
	}

	actor, _ := identityFrom(r.Context())
	if !*req.IsActive {
		if actor.UserID == id {
			writeError(w, http.StatusBadRequest, "you cannot disable your own account")
			return
		}
		if err := s.ensureAnotherAdminRemains(r, id); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	if err := s.db.SetUserActive(r.Context(), id, *req.IsActive); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		s.log.Error("set user active", "error", err)
		writeError(w, http.StatusInternalServerError, "could not update user")
		return
	}

	action := "disable_user"
	if *req.IsActive {
		action = "enable_user"
	}
	s.db.Audit(r.Context(), &actor.UserID, action, "user", id.String(), nil)
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

type setRoleRequest struct {
	Role string `json:"role"`
}

func (s *Server) handleSetUserRole(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r)
	if !ok {
		return
	}
	var req setRoleRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	role := domain.Role(strings.TrimSpace(req.Role))
	if !role.Valid() {
		writeError(w, http.StatusBadRequest, "роль: client, operator, accountant или admin")
		return
	}

	actor, _ := identityFrom(r.Context())
	if role != domain.RoleAdmin {
		if actor.UserID == id {
			writeError(w, http.StatusBadRequest, "you cannot demote yourself")
			return
		}
		if err := s.ensureAnotherAdminRemains(r, id); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	if err := s.db.SetUserRole(r.Context(), id, string(role)); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		s.log.Error("set user role", "error", err)
		writeError(w, http.StatusInternalServerError, "could not update user")
		return
	}

	s.db.Audit(r.Context(), &actor.UserID, "set_role", "user", id.String(),
		map[string]any{"role": role})
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// ensureAnotherAdminRemains не даёт заблокировать или разжаловать последнего
// администратора: иначе в систему больше никто не войдёт, и чинить это придётся
// руками в базе.
func (s *Server) ensureAnotherAdminRemains(r *http.Request, excluding uuid.UUID) error {
	target, err := s.db.UserByID(r.Context(), excluding)
	if err != nil || target.Role != domain.RoleAdmin {
		return nil
	}
	n, err := s.db.CountActiveAdmins(r.Context(), excluding)
	if err != nil {
		return errors.New("could not verify remaining administrators")
	}
	if n == 0 {
		return errors.New("this is the last active administrator")
	}
	return nil
}

func validatePassword(p string) error {
	if len([]rune(p)) < 10 {
		return errors.New("password must be at least 10 characters")
	}
	var hasLetter, hasDigit bool
	for _, r := range p {
		switch {
		case r >= '0' && r <= '9':
			hasDigit = true
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r > 127:
			hasLetter = true
		}
	}
	if !hasLetter || !hasDigit {
		return errors.New("password must contain both letters and digits")
	}
	return nil
}

func pathUUID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return uuid.Nil, false
	}
	return id, true
}
