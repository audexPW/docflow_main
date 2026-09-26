package httpapi

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"docflow/internal/domain"
	"docflow/internal/storage"

	"github.com/google/uuid"
)

// Компании (юрлица) и права по ним.
//
// Каждая компания — отдельный контур: своя папка обмена, свои пользователи,
// свой главбух. Заходящий под компанией А видит только документы А; всех видит
// только администратор, он же раздаёт права.

var reFolderName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

type createCompanyRequest struct {
	Name             string `json:"name"`
	UNP              string `json:"unp"`
	Folder           string `json:"folder"`
	ApprovalRequired bool   `json:"approval_required"`
}

func (s *Server) handleCreateCompany(w http.ResponseWriter, r *http.Request) {
	var req createCompanyRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.UNP = strings.TrimSpace(req.UNP)
	req.Folder = strings.TrimSpace(req.Folder)
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "название компании обязательно")
		return
	}
	if req.Folder == "" {
		req.Folder = folderFromUNP(req.UNP, req.Name)
	}
	if !reFolderName.MatchString(req.Folder) {
		writeError(w, http.StatusBadRequest,
			"имя папки: латиница, цифры, дефис, точка или подчёркивание, до 64 символов")
		return
	}

	company, err := s.db.CreateCompany(r.Context(), storage.NewCompany{
		Name:             req.Name,
		UNP:              req.UNP,
		Folder:           req.Folder,
		ApprovalRequired: req.ApprovalRequired,
	})
	if err != nil {
		if strings.Contains(err.Error(), "duplicate") {
			writeError(w, http.StatusConflict, "папка с таким именем уже занята")
			return
		}
		s.log.Error("create company", "error", err)
		writeError(w, http.StatusInternalServerError, "не удалось создать компанию")
		return
	}

	// Папку обмена создаём сразу: 1С забирает документы именно из неё, и она
	// должна существовать до первой выгрузки, а не появляться внезапно.
	if dir := s.companyDir(company.Folder); dir != "" {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			s.log.Error("create company folder", "error", err, "dir", dir)
		}
	}

	actor, _ := identityFrom(r.Context())
	s.db.Audit(r.Context(), &actor.UserID, "create_company", "company", company.ID.String(),
		map[string]any{"name": company.Name, "folder": company.Folder})
	writeJSON(w, http.StatusCreated, company)
}

func (s *Server) handleListCompanies(w http.ResponseWriter, r *http.Request) {
	identity, _ := identityFrom(r.Context())
	user, err := s.db.UserByID(r.Context(), identity.UserID)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token")
		return
	}

	ids, err := s.db.VisibleCompanyIDs(r.Context(), user)
	if err != nil {
		s.log.Error("visible companies", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	var companies []domain.Company
	if ids == nil {
		companies, err = s.db.ListCompanies(r.Context())
	} else {
		companies, err = s.db.CompaniesByIDs(r.Context(), ids)
	}
	if err != nil {
		s.log.Error("list companies", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": companies})
}

type updateCompanyRequest struct {
	Name             *string `json:"name"`
	UNP              *string `json:"unp"`
	ApprovalRequired *bool   `json:"approval_required"`
	IsActive         *bool   `json:"is_active"`
}

func (s *Server) handleUpdateCompany(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r)
	if !ok {
		return
	}
	var req updateCompanyRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	company, err := s.db.UpdateCompany(r.Context(), id, storage.CompanyUpdate{
		Name:             req.Name,
		UNP:              req.UNP,
		ApprovalRequired: req.ApprovalRequired,
		IsActive:         req.IsActive,
	})
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "компания не найдена")
			return
		}
		s.log.Error("update company", "error", err)
		writeError(w, http.StatusInternalServerError, "не удалось изменить компанию")
		return
	}

	actor, _ := identityFrom(r.Context())
	s.db.Audit(r.Context(), &actor.UserID, "update_company", "company", id.String(), nil)
	writeJSON(w, http.StatusOK, company)
}

func (s *Server) handleListCompanyAccountants(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r)
	if !ok {
		return
	}
	users, err := s.db.CompanyAccountants(r.Context(), id)
	if err != nil {
		s.log.Error("list company accountants", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": users})
}

type setAccountantsRequest struct {
	UserIDs []string `json:"user_ids"`
}

// handleSetCompanyAccountants заменяет состав главбухов компании. Переназначение
// главбуха с компании А на компанию Б — это два таких вызова, и оба видны в
// журнале: кто, когда и кого поставил.
func (s *Server) handleSetCompanyAccountants(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r)
	if !ok {
		return
	}
	var req setAccountantsRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	ids := make([]uuid.UUID, 0, len(req.UserIDs))
	for _, raw := range req.UserIDs {
		uid, err := uuid.Parse(strings.TrimSpace(raw))
		if err != nil {
			writeError(w, http.StatusBadRequest, "некорректный идентификатор пользователя")
			return
		}
		user, err := s.db.UserByID(r.Context(), uid)
		if err != nil {
			writeError(w, http.StatusBadRequest, "пользователь не найден")
			return
		}
		if user.Role != domain.RoleAccountant {
			writeError(w, http.StatusBadRequest,
				"закреплять за компанией можно только пользователя с ролью «Главбух»: "+user.Login)
			return
		}
		ids = append(ids, uid)
	}

	if err := s.db.SetCompanyAccountants(r.Context(), id, ids); err != nil {
		s.log.Error("set company accountants", "error", err)
		writeError(w, http.StatusInternalServerError, "не удалось сохранить закрепление")
		return
	}

	actor, _ := identityFrom(r.Context())
	s.db.Audit(r.Context(), &actor.UserID, "set_company_accountants", "company", id.String(),
		map[string]any{"user_ids": req.UserIDs})
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

type setUserCompanyRequest struct {
	CompanyID *string `json:"company_id"`
}

// handleSetUserCompany привязывает учётку к юрлицу. Пустое значение снимает
// привязку — так делают для сотрудников аудиторской фирмы, работающих сразу за
// нескольких клиентов.
func (s *Server) handleSetUserCompany(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r)
	if !ok {
		return
	}
	var req setUserCompanyRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	var companyID *uuid.UUID
	if req.CompanyID != nil && strings.TrimSpace(*req.CompanyID) != "" {
		cid, err := uuid.Parse(strings.TrimSpace(*req.CompanyID))
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

	if err := s.db.SetUserCompany(r.Context(), id, companyID); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "пользователь не найден")
			return
		}
		s.log.Error("set user company", "error", err)
		writeError(w, http.StatusInternalServerError, "не удалось изменить привязку")
		return
	}

	actor, _ := identityFrom(r.Context())
	s.db.Audit(r.Context(), &actor.UserID, "set_user_company", "user", id.String(),
		map[string]any{"company_id": req.CompanyID})
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// companyDir — путь папки обмена компании внутри каталога выгрузки.
func (s *Server) companyDir(folder string) string {
	if s.cfg.OneC.FileDir == "" || folder == "" {
		return ""
	}
	return filepath.Join(s.cfg.OneC.FileDir, folder)
}

// folderFromUNP подбирает имя папки, если администратор его не задал: по УНП
// (он уникален и короток), иначе по латинской транслитерации названия.
func folderFromUNP(unp, name string) string {
	if unp != "" && reFolderName.MatchString(unp) {
		return unp
	}
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ', r == '-', r == '_':
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "company-" + uuid.NewString()[:8]
	}
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}

type setUserCompaniesRequest struct {
	CompanyIDs []string `json:"company_ids"`
}

// handleListUserCompanies — компании, закреплённые за главбухом. Нужен странице
// «Пользователи»: без него она не знает, какие галочки уже стоят.
func (s *Server) handleListUserCompanies(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r)
	if !ok {
		return
	}
	ids, err := s.db.AccountantCompanyIDs(r.Context(), id)
	if err != nil {
		s.log.Error("list user companies", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]string, 0, len(ids))
	for _, cid := range ids {
		out = append(out, cid.String())
	}
	writeJSON(w, http.StatusOK, map[string]any{"company_ids": out})
}

// handleSetUserCompanies закрепляет главбуха сразу за набором компаний. То же
// закрепление, что и на странице «Компании», но со стороны сотрудника: посадить
// одного человека на пять юрлиц — один запрос, а не пять карточек.
func (s *Server) handleSetUserCompanies(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r)
	if !ok {
		return
	}
	var req setUserCompaniesRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	user, err := s.db.UserByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "пользователь не найден")
		return
	}
	// Та же проверка, что и в handleSetCompanyAccountants: закрепление имеет
	// смысл только для главбуха, у остальных ролей область видимости считается
	// по users.company_id и эти строки просто повисли бы мусором.
	if user.Role != domain.RoleAccountant {
		writeError(w, http.StatusBadRequest,
			"закреплять компании можно только пользователю с ролью «Главбух»")
		return
	}

	ids := make([]uuid.UUID, 0, len(req.CompanyIDs))
	for _, raw := range req.CompanyIDs {
		cid, err := uuid.Parse(strings.TrimSpace(raw))
		if err != nil {
			writeError(w, http.StatusBadRequest, "некорректный идентификатор компании")
			return
		}
		if _, err := s.db.Company(r.Context(), cid); err != nil {
			writeError(w, http.StatusBadRequest, "компания не найдена")
			return
		}
		ids = append(ids, cid)
	}

	if err := s.db.SetAccountantCompanies(r.Context(), id, ids); err != nil {
		s.log.Error("set user companies", "error", err)
		writeError(w, http.StatusInternalServerError, "не удалось сохранить закрепление")
		return
	}

	actor, _ := identityFrom(r.Context())
	s.db.Audit(r.Context(), &actor.UserID, "set_user_companies", "user", id.String(),
		map[string]any{"company_ids": req.CompanyIDs})
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}
