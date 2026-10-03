package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"docflow/internal/domain"
	"docflow/internal/recognize"
	"docflow/internal/storage"

	"github.com/google/uuid"
)

var allowedContentTypes = map[string]bool{
	"image/jpeg":      true,
	"image/png":       true,
	"image/heic":      true,
	"image/heif":      true,
	"application/pdf": true,
}

func (s *Server) handleUploadDocument(w http.ResponseWriter, r *http.Request) {
	identity, _ := identityFrom(r.Context())

	if err := r.ParseMultipartForm(s.cfg.MaxUploadBytes); err != nil {
		writeError(w, http.StatusBadRequest, "could not parse upload")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "file field is required")
		return
	}
	defer file.Close()

	if header.Size > s.cfg.MaxUploadBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "file is too large")
		return
	}

	contentType := detectContentType(header.Header.Get("Content-Type"), header.Filename)
	if !allowedContentTypes[contentType] {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported file type")
		return
	}

	// Юрлицо документа: клиент и главбух — только своё, оператор может снимать
	// за клиента, который приехал в офис, и выбрать компанию в форме, админ —
	// любую. Без компании документ ложится в общую папку, как раньше.
	companyID, err := s.resolveUploadCompany(r, identity.UserID, r.FormValue("company_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	saved, err := s.files.Save(file)
	if err != nil {
		s.log.Error("save file", "error", err)
		writeError(w, http.StatusInternalServerError, "could not store file")
		return
	}

	doc, err := s.db.CreateDocument(r.Context(), storage.NewDocument{
		OwnerID:      identity.UserID,
		CompanyID:    companyID,
		OriginalName: sanitizeName(header.Filename),
		ContentType:  contentType,
		SizeBytes:    saved.Size,
		StorageKey:   saved.Key,
		SHA256:       saved.SHA256,
	})
	if err != nil {
		s.log.Error("create document", "error", err)
		writeError(w, http.StatusInternalServerError, "could not create document")
		return
	}

	s.db.Audit(r.Context(), &identity.UserID, "upload", "document", doc.ID.String(),
		map[string]any{"name": doc.OriginalName, "size": doc.SizeBytes})
	writeJSON(w, http.StatusCreated, doc)
}

func (s *Server) handleListDocuments(w http.ResponseWriter, r *http.Request) {
	identity, _ := identityFrom(r.Context())

	q := r.URL.Query()
	f := storage.DocumentFilter{
		Status: domain.Status(q.Get("status")),
		Limit:  atoiDefault(q.Get("limit"), 50),
		Offset: atoiDefault(q.Get("offset"), 0),
	}
	// archive=only — страница «Архив», archive=all — общий список без деления.
	// По умолчанию проведённые в 1С документы в рабочем списке не показываем.
	switch strings.ToLower(strings.TrimSpace(q.Get("archive"))) {
	case "only", "1", "true":
		f.Archive = storage.ArchiveOnly
	case "all":
		f.Archive = storage.ArchiveAll
	default:
		f.Archive = storage.ArchiveHide
	}
	if err := s.applyScope(r, identity.UserID, &f); err != nil {
		s.log.Error("document scope", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	// Фильтр «одна компания»: главбух ведёт несколько юрлиц и работает с ними
	// по очереди — выгружает в 1С сначала по одной компании, потом по другой.
	if err := s.narrowToCompany(&f, q.Get("company_id")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	docs, err := s.db.ListDocuments(r.Context(), f)
	if err != nil {
		s.log.Error("list documents", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	// Общее число нужно списку, чтобы показать «загружено N из M» и понять,
	// осталось ли что подгружать: раньше страница обрывалась на первых 50
	// документах, и добраться до остальных было нечем.
	total, err := s.db.CountDocuments(r.Context(), f)
	if err != nil {
		s.log.Error("count documents", "error", err)
		total = len(docs) + f.Offset
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":  docs,
		"total":  total,
		"limit":  f.Limit,
		"offset": f.Offset,
		// Срок хранения архива — чтобы страница «Архив» показывала, сколько
		// документу осталось до удаления, а не заставляла угадывать.
		"archive_keep_days": s.cfg.OneC.ArchiveKeepDays,
	})
}

func (s *Server) handleGetDocument(w http.ResponseWriter, r *http.Request) {
	doc, ok := s.loadOwned(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

func (s *Server) handleDownloadFile(w http.ResponseWriter, r *http.Request) {
	doc, ok := s.loadOwned(w, r)
	if !ok {
		return
	}
	data, err := s.files.ReadAll(doc.StorageKey)
	if err != nil {
		s.log.Error("read file", "error", err, "document_id", doc.ID)
		writeError(w, http.StatusInternalServerError, "could not read file")
		return
	}
	w.Header().Set("Content-Type", doc.ContentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", doc.OriginalName))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

type updateRecognitionRequest struct {
	DocType *string           `json:"doc_type"`
	Fields  map[string]string `json:"fields"`
}

func (s *Server) handleUpdateRecognition(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	var req updateRecognitionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	doc, err := s.db.Document(r.Context(), id)
	if err != nil {
		respondNotFound(w, err, s)
		return
	}

	rec := domain.Recognition{Fields: map[string]domain.Field{}}
	if doc.Recognition != nil {
		rec = *doc.Recognition
		if rec.Fields == nil {
			rec.Fields = map[string]domain.Field{}
		}
	}
	if req.DocType != nil {
		// Оператор может выбрать тип из списка или вписать свой — приводим к
		// коду реестра, чтобы выгрузка нашла обязательные поля и объект 1С.
		rec.DocType = recognize.NormalizeDocType(*req.DocType)
	}
	// Ручные правки оператора считаем достоверными.
	for k, v := range req.Fields {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		rec.Fields[k] = domain.Field{Value: strings.TrimSpace(v), Confidence: 1, Source: "manual"}
	}

	if err := s.db.UpdateRecognition(r.Context(), id, rec); err != nil {
		respondNotFound(w, err, s)
		return
	}

	identity, _ := identityFrom(r.Context())
	s.db.Audit(r.Context(), &identity.UserID, "edit", "document", id.String(), nil)

	updated, _ := s.db.Document(r.Context(), id)
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleConfirmDocument(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	doc, err := s.db.Document(r.Context(), id)
	if err != nil {
		respondNotFound(w, err, s)
		return
	}
	if doc.Recognition == nil {
		writeError(w, http.StatusConflict, "document is not recognized yet")
		return
	}
	if doc.Status == domain.StatusExported {
		writeError(w, http.StatusConflict, "document is already exported")
		return
	}

	if err := s.db.SetStatus(r.Context(), id, domain.StatusConfirmed); err != nil {
		respondNotFound(w, err, s)
		return
	}
	if _, err := s.db.EnqueueExport(r.Context(), id, domain.ExportKindCreate); err != nil {
		s.log.Error("enqueue export", "error", err, "document_id", id)
		writeError(w, http.StatusInternalServerError, "could not enqueue export")
		return
	}

	identity, _ := identityFrom(r.Context())
	s.db.Audit(r.Context(), &identity.UserID, "confirm", "document", id.String(), nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": string(domain.StatusConfirmed)})
}

type completeRequest struct {
	Fields map[string]string `json:"fields"`
}

// handleCompleteDocument принимает вручную введённые поля (обычно от клиента,
// которому система сообщила о недостающих реквизитах), дозаполняет документ и,
// если все обязательные поля собраны, отправляет досыл в 1С.
//
// Клиент может дозаполнять только свой документ и только в статусе needs_input;
// оператор/админ — любой ещё не выгруженный документ.
func (s *Server) handleCompleteDocument(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	var req completeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	doc, err := s.db.Document(r.Context(), id)
	if err != nil {
		respondNotFound(w, err, s)
		return
	}
	identity, _ := identityFrom(r.Context())
	if identity.Role == domain.RoleClient {
		if doc.OwnerID != identity.UserID {
			writeError(w, http.StatusNotFound, "document not found")
			return
		}
		if doc.Status != domain.StatusNeedsInput {
			writeError(w, http.StatusConflict, "document is not awaiting manual input")
			return
		}
	}
	if doc.Status == domain.StatusExported {
		writeError(w, http.StatusConflict, "document is already exported")
		return
	}
	if doc.Recognition == nil {
		writeError(w, http.StatusConflict, "document is not recognized yet")
		return
	}

	rec := *doc.Recognition
	if rec.Fields == nil {
		rec.Fields = map[string]domain.Field{}
	}
	for k, v := range req.Fields {
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k == "" || v == "" {
			continue
		}
		rec.Fields[k] = domain.Field{Value: v, Confidence: 1, Source: "manual"}
	}

	missing := recognize.MissingRequired(rec)
	rec.Missing = missing

	status := domain.StatusNeedsInput
	if len(missing) == 0 {
		status = domain.StatusConfirmed
	}
	if err := s.db.SaveRecognition(r.Context(), id, rec, doc.OCRText, status); err != nil {
		respondNotFound(w, err, s)
		return
	}

	// Все обязательные поля собраны — досылаем в 1С (update поверх ранее
	// отправленной частичной выгрузки; 1С сопоставляет по document_id).
	if len(missing) == 0 {
		if _, err := s.db.EnqueueExport(r.Context(), id, domain.ExportKindUpdate); err != nil {
			s.log.Error("enqueue update export", "error", err, "document_id", id)
			writeError(w, http.StatusInternalServerError, "could not enqueue export")
			return
		}
	}

	s.db.Audit(r.Context(), &identity.UserID, "complete", "document", id.String(),
		map[string]any{"missing_after": missing})

	updated, _ := s.db.Document(r.Context(), id)
	writeJSON(w, http.StatusOK, updated)
}

// handleUnarchiveDocument возвращает документ из архива в работу. Нужен, когда
// 1С отчиталась о проведении ошибочно или документ переоткрыли: без этого
// единственным способом достать его обратно была бы правка базы руками.
func (s *Server) handleUnarchiveDocument(w http.ResponseWriter, r *http.Request) {
	doc, ok := s.loadOwned(w, r)
	if !ok {
		return
	}
	if doc.ArchivedAt == nil {
		writeError(w, http.StatusConflict, "документ не в архиве")
		return
	}
	if err := s.db.UnarchiveDocument(r.Context(), doc.ID); err != nil {
		respondNotFound(w, err, s)
		return
	}
	identity, _ := identityFrom(r.Context())
	s.db.Audit(r.Context(), &identity.UserID, "unarchive", "document", doc.ID.String(), nil)

	updated, _ := s.db.Document(r.Context(), doc.ID)
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleReprocessDocument(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	if err := s.db.RequeueForRecognition(r.Context(), id); err != nil {
		respondNotFound(w, err, s)
		return
	}
	identity, _ := identityFrom(r.Context())
	s.db.Audit(r.Context(), &identity.UserID, "reprocess", "document", id.String(), nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": string(domain.StatusReceived)})
}

// loadOwned читает документ и проверяет, что клиент обращается к своему.
func (s *Server) loadOwned(w http.ResponseWriter, r *http.Request) (domain.Document, bool) {
	id, ok := parseID(w, r)
	if !ok {
		return domain.Document{}, false
	}
	doc, err := s.db.Document(r.Context(), id)
	if err != nil {
		respondNotFound(w, err, s)
		return domain.Document{}, false
	}
	identity, _ := identityFrom(r.Context())
	ok, err = s.canAccess(r, identity.UserID, doc)
	if err != nil {
		s.log.Error("access check", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return domain.Document{}, false
	}
	if !ok {
		writeError(w, http.StatusNotFound, "document not found")
		return domain.Document{}, false
	}
	return doc, true
}

func parseID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid document id")
		return uuid.UUID{}, false
	}
	return id, true
}

func respondNotFound(w http.ResponseWriter, err error, s *Server) {
	if errors.Is(err, storage.ErrNotFound) {
		writeError(w, http.StatusNotFound, "document not found")
		return
	}
	s.log.Error("document operation", "error", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

func detectContentType(headerType, filename string) string {
	headerType = strings.ToLower(strings.TrimSpace(headerType))
	if allowedContentTypes[headerType] {
		return headerType
	}
	switch strings.ToLower(ext(filename)) {
	case ".pdf":
		return "application/pdf"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".heic":
		return "image/heic"
	case ".heif":
		return "image/heif"
	}
	return headerType
}

func ext(name string) string {
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return name[i:]
	}
	return ""
}

func sanitizeName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "document"
	}
	return name
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

// handleListDocTypes отдаёт реестр типов документов: подписи для интерфейса,
// обязательные реквизиты и согласованный объект-приёмник в 1С. Список открыт и
// меняется файлом DOCTYPES_PATH, поэтому фронтенд берёт его с сервера, а не
// хранит у себя копию.
func (s *Server) handleListDocTypes(w http.ResponseWriter, r *http.Request) {
	specs := recognize.DocTypes()
	out := make([]docTypeView, 0, len(specs)+1)
	for _, spec := range specs {
		out = append(out, docTypeView{
			Slug:       spec.Slug,
			Title:      spec.Title,
			Required:   recognize.RequiredFields(spec.Slug),
			HasLines:   spec.HasLines,
			OneCObject: spec.OneC.Object,
			OneCKind:   spec.OneC.Kind,
			Routable:   recognize.ExportRoutable(spec.Slug),
		})
	}
	out = append(out, docTypeView{Slug: recognize.DocTypeUnknown, Title: "Не определён"})
	writeJSON(w, http.StatusOK, map[string]any{"types": out})
}

func (s *Server) handleDeleteTestDocument(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}

	doc, err := s.db.Document(r.Context(), id)
	if err != nil {
		respondNotFound(w, err, s)
		return
	}

	if err := s.files.Remove(doc.StorageKey); err != nil {
		s.log.Error(
			"remove test document file",
			"error",
			err,
			"document_id",
			id,
		)
		writeError(
			w,
			http.StatusInternalServerError,
			"could not remove file",
		)
		return
	}

	if err := s.db.DeleteDocument(
		r.Context(),
		id,
	); err != nil {
		s.log.Error(
			"delete test document",
			"error",
			err,
			"document_id",
			id,
		)
		writeError(
			w,
			http.StatusInternalServerError,
			"could not delete document",
		)
		return
	}

	writeJSON(
		w,
		http.StatusOK,
		map[string]string{
			"status": "deleted",
		},
	)
}

type docTypeView struct {
	Slug       string   `json:"slug"`
	Title      string   `json:"title"`
	Required   []string `json:"required,omitempty"`
	HasLines   bool     `json:"has_lines"`
	OneCObject string   `json:"onec_object,omitempty"`
	OneCKind   string   `json:"onec_object_kind,omitempty"`
	Routable   bool     `json:"routable"`
}
