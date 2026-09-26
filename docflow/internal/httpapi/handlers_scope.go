package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"docflow/internal/domain"
	"docflow/internal/recognize"
	"docflow/internal/storage"

	"github.com/google/uuid"
)

// Область видимости документов и согласование главбухом.
//
// Правило простое: документ принадлежит юрлицу, и видит его только тот, кто к
// этому юрлицу допущен. Клиент — свои загрузки, оператор — компанию, за которой
// закреплён (или все, если не закреплён ни за одной: он сидит в офисе и снимает
// за любого приехавшего), главбух — компании, которые ему назначил админ,
// админ — всё.

// applyScope дописывает в фильтр списка документов ограничения текущего
// пользователя.
func (s *Server) applyScope(r *http.Request, userID uuid.UUID, f *storage.DocumentFilter) error {
	user, err := s.db.UserByID(r.Context(), userID)
	if err != nil {
		return err
	}
	switch user.Role {
	case domain.RoleAdmin:
		return nil
	case domain.RoleClient:
		// Клиент видит именно свои загрузки, а не всё юрлицо: сотрудники одной
		// компании не обязаны видеть документы друг друга.
		f.OwnerID = &user.ID
		return nil
	case domain.RoleAccountant:
		ids, err := s.db.VisibleCompanyIDs(r.Context(), user)
		if err != nil {
			return err
		}
		f.Scoped = true
		f.CompanyIDs = ids
		return nil
	default: // оператор
		if user.CompanyID != nil {
			f.Scoped = true
			f.CompanyIDs = []uuid.UUID{*user.CompanyID}
		}
		return nil
	}
}

// canAccess отвечает, вправе ли пользователь открыть конкретный документ.
func (s *Server) canAccess(r *http.Request, userID uuid.UUID, doc domain.Document) (bool, error) {
	user, err := s.db.UserByID(r.Context(), userID)
	if err != nil {
		return false, err
	}
	switch user.Role {
	case domain.RoleAdmin:
		return true, nil
	case domain.RoleClient:
		return doc.OwnerID == user.ID, nil
	case domain.RoleAccountant:
		if doc.CompanyID == nil {
			return false, nil
		}
		ids, err := s.db.VisibleCompanyIDs(r.Context(), user)
		if err != nil {
			return false, err
		}
		for _, id := range ids {
			if id == *doc.CompanyID {
				return true, nil
			}
		}
		return false, nil
	default: // оператор
		if user.CompanyID == nil {
			return true, nil
		}
		return doc.CompanyID != nil && *doc.CompanyID == *user.CompanyID, nil
	}
}

// resolveUploadCompany определяет юрлицо загружаемого документа.
func (s *Server) resolveUploadCompany(r *http.Request, userID uuid.UUID, requested string) (*uuid.UUID, error) {
	user, err := s.db.UserByID(r.Context(), userID)
	if err != nil {
		return nil, errors.New("не удалось определить учётную запись")
	}
	requested = strings.TrimSpace(requested)

	if requested == "" {
		return user.CompanyID, nil
	}
	cid, err := uuid.Parse(requested)
	if err != nil {
		return nil, errors.New("некорректный идентификатор компании")
	}
	company, err := s.db.Company(r.Context(), cid)
	if err != nil {
		return nil, errors.New("компания не найдена")
	}
	if !company.IsActive {
		return nil, errors.New("компания отключена")
	}

	switch user.Role {
	case domain.RoleAdmin:
		return &cid, nil
	case domain.RoleOperator:
		// Оператор без привязки обслуживает всех, привязанный — только своих.
		if user.CompanyID == nil || *user.CompanyID == cid {
			return &cid, nil
		}
		return nil, errors.New("нет прав на загрузку в эту компанию")
	default:
		if user.CompanyID != nil && *user.CompanyID == cid {
			return &cid, nil
		}
		return nil, errors.New("нет прав на загрузку в эту компанию")
	}
}

type approvalRequest struct {
	Note string `json:"note"`
}

// handleApproveDocument — главбух согласовал документ. Если документ ждал
// согласования, он тут же уходит в 1С; если уже ушёл (компания без обязательного
// согласования), остаётся отметка «проверено» с именем главбуха.
func (s *Server) handleApproveDocument(w http.ResponseWriter, r *http.Request) {
	doc, ok := s.loadOwned(w, r)
	if !ok {
		return
	}
	var req approvalRequest
	_ = decodeJSON(r, &req)

	identity, _ := identityFrom(r.Context())
	if err := s.db.ApproveDocument(r.Context(), doc.ID, identity.UserID, strings.TrimSpace(req.Note)); err != nil {
		respondNotFound(w, err, s)
		return
	}

	if doc.Status == domain.StatusNeedsApproval || doc.Status == domain.StatusRejected {
		if doc.Recognition == nil {
			writeError(w, http.StatusConflict, "документ ещё не распознан")
			return
		}
		// Согласовать документ с ошибками распознавания нельзя: по нему
		// нельзя формировать XML, пока поля не исправлены (13.09.2026).
		if recognize.RecognitionFaulty(*doc.Recognition) && !recognize.PartialExportEnabled() {
			writeError(w, http.StatusConflict,
				"в документе есть ошибки распознавания — исправьте поля, затем согласуйте")
			return
		}
		if err := s.db.SetStatus(r.Context(), doc.ID, domain.StatusConfirmed); err != nil {
			respondNotFound(w, err, s)
			return
		}
		if _, err := s.db.EnqueueExport(r.Context(), doc.ID, domain.ExportKindCreate); err != nil {
			s.log.Error("enqueue export after approval", "error", err, "document_id", doc.ID)
			writeError(w, http.StatusInternalServerError, "не удалось поставить в очередь на выгрузку")
			return
		}
	}

	s.db.Audit(r.Context(), &identity.UserID, "approve", "document", doc.ID.String(),
		map[string]any{"note": req.Note})

	updated, _ := s.db.Document(r.Context(), doc.ID)
	writeJSON(w, http.StatusOK, updated)
}

// handleRejectDocument — главбух вернул документ. В 1С он не уходит, причина
// видна клиенту в карточке.
func (s *Server) handleRejectDocument(w http.ResponseWriter, r *http.Request) {
	doc, ok := s.loadOwned(w, r)
	if !ok {
		return
	}
	var req approvalRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	note := strings.TrimSpace(req.Note)
	if note == "" {
		writeError(w, http.StatusBadRequest, "укажите причину возврата — её увидит клиент")
		return
	}

	identity, _ := identityFrom(r.Context())
	if err := s.db.RejectDocument(r.Context(), doc.ID, identity.UserID, note); err != nil {
		respondNotFound(w, err, s)
		return
	}
	s.db.Audit(r.Context(), &identity.UserID, "reject", "document", doc.ID.String(),
		map[string]any{"note": note})

	updated, _ := s.db.Document(r.Context(), doc.ID)
	writeJSON(w, http.StatusOK, updated)
}

// narrowToCompany сужает выборку до одной компании — фильтр «показать только
// документы этого юрлица». Сузить можно лишь в пределах того, что пользователю
// и так доступно: запрос чужой компании выборку не расширяет, а обнуляет.
func (s *Server) narrowToCompany(f *storage.DocumentFilter, requested string) error {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return nil
	}
	cid, err := uuid.Parse(requested)
	if err != nil {
		return errors.New("некорректный идентификатор компании")
	}

	if f.Scoped {
		allowed := false
		for _, id := range f.CompanyIDs {
			if id == cid {
				allowed = true
				break
			}
		}
		if !allowed {
			f.CompanyIDs = nil // ничего не покажем: компания вне области видимости
			return nil
		}
	}
	f.Scoped = true
	f.CompanyIDs = []uuid.UUID{cid}
	return nil
}

type setDocumentCompanyRequest struct {
	CompanyID *string `json:"company_id"`
}

// handleSetDocumentCompany — администратор проставляет документу юрлицо.
// Нужно для документов, загруженных под учёткой без привязки к компании: в
// списке у них пустая колонка «Компания», и в папку компании они не уезжают.
func (s *Server) handleSetDocumentCompany(w http.ResponseWriter, r *http.Request) {
	doc, ok := s.loadOwned(w, r)
	if !ok {
		return
	}
	var req setDocumentCompanyRequest
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

	if err := s.db.SetDocumentCompany(r.Context(), doc.ID, companyID); err != nil {
		respondNotFound(w, err, s)
		return
	}

	identity, _ := identityFrom(r.Context())
	s.db.Audit(r.Context(), &identity.UserID, "set_document_company", "document", doc.ID.String(),
		map[string]any{"company_id": req.CompanyID})

	updated, _ := s.db.Document(r.Context(), doc.ID)
	writeJSON(w, http.StatusOK, updated)
}
