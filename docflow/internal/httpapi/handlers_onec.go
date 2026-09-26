package httpapi

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"

	"docflow/internal/storage"

	"github.com/google/uuid"
)

// Обратная синхронизация статусов из 1С (ТЗ §2, §10).
//
// 1С после обработки выгрузки сообщает сюда, что стало с документом. Статус
// сразу виден пользователю в приложении и уходит ему пуш-уведомлением.
//
// Аутентификация — отдельный статический токен ONEC_INBOUND_TOKEN, а не
// пользовательский логин: на стороне 1С регламентное задание работает без
// человека, и заводить ему учётку с ролью было бы хуже — она попала бы в
// общий список пользователей и могла бы войти в веб-интерфейс.

type onecStatusRequest struct {
	SourceID string `json:"source_id"`
	Status   string `json:"status"`
	Ref      string `json:"ref"`
	Message  string `json:"message"`
}

// Статусы, которые принимаем от 1С. Всё остальное отклоняем, чтобы в базе не
// появилось произвольных значений, которые интерфейс не умеет показать.
var onecStatuses = map[string]string{
	"accepted": "принят в 1С",
	"posted":   "проведён",
	"rejected": "отклонён",
	"error":    "ошибка обработки",
}

func (s *Server) handleOneCStatus(w http.ResponseWriter, r *http.Request) {
	if !s.onecInboundAuthorized(r) {
		writeError(w, http.StatusUnauthorized, "invalid integration token")
		return
	}

	var req onecStatusRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	id, err := uuid.Parse(strings.TrimSpace(req.SourceID))
	if err != nil {
		writeError(w, http.StatusBadRequest, "source_id must be the SourceId from the export")
		return
	}
	status := strings.ToLower(strings.TrimSpace(req.Status))
	if _, ok := onecStatuses[status]; !ok {
		writeError(w, http.StatusBadRequest, "status must be one of: accepted, posted, rejected, error")
		return
	}

	if err := s.db.SetOneCStatus(r.Context(), id, status, req.Ref, req.Message); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "document not found")
			return
		}
		s.log.Error("set 1c status", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Документ проведён в 1С — работа над ним закончена: он уходит на
	// страницу «Архив», и с этого момента идёт отсчёт срока хранения.
	// Какие именно статусы закрывают документ, задаётся ONEC_ARCHIVE_STATUSES.
	archived := false
	if s.cfg.OneC.ArchivesOn(status) {
		if err := s.db.ArchiveDocument(r.Context(), id); err != nil {
			s.log.Error("archive document", "error", err, "document_id", id)
		} else {
			archived = true
		}
	}

	s.db.Audit(r.Context(), nil, "onec_status", "document", id.String(),
		map[string]any{"status": status, "ref": req.Ref, "archived": archived})

	// Пуш владельцу: проведение и особенно отказ он должен увидеть сразу.
	if s.notifier != nil {
		if doc, err := s.db.Document(r.Context(), id); err == nil {
			s.notifier.NotifyStatus(r.Context(), doc.OwnerID, doc)
		}
	}

	s.log.Info("1c status received", "document", id, "status", status, "ref", req.Ref, "archived", archived)
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "archived": archived})
}

func (s *Server) onecInboundAuthorized(r *http.Request) bool {
	if s.onecInboundToken == "" {
		return false // канал не настроен — принимать нечего
	}
	got := strings.TrimSpace(r.Header.Get("X-DocFlow-Token"))
	if got == "" {
		got = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	return subtle.ConstantTimeCompare([]byte(strings.TrimSpace(got)), []byte(s.onecInboundToken)) == 1
}
