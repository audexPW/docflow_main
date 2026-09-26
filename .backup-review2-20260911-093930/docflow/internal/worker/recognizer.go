package worker

import (
	"context"
	"log/slog"
	"time"

	"docflow/internal/domain"
	"docflow/internal/push"
	"docflow/internal/recognize"
	"docflow/internal/storage"
)

type Recognizer struct {
	db         *storage.DB
	pipeline   *recognize.Pipeline
	notifier   push.Notifier
	autoExport bool
	// requireMapping — не отправлять в 1С документ, для типа которого не
	// согласован объект-приёмник (см. recognize.ExportRoutable).
	requireMapping bool
	log            *slog.Logger
	idle           time.Duration
}

func NewRecognizer(db *storage.DB, pipeline *recognize.Pipeline, notifier push.Notifier, autoExport, requireMapping bool, log *slog.Logger) *Recognizer {
	return &Recognizer{
		db:             db,
		pipeline:       pipeline,
		notifier:       notifier,
		autoExport:     autoExport,
		requireMapping: requireMapping,
		log:            log,
		idle:           2 * time.Second,
	}
}

// Run запускает n параллельных обработчиков и блокируется до отмены контекста.
func (r *Recognizer) Run(ctx context.Context, n int) {
	if n < 1 {
		n = 1
	}
	done := make(chan struct{}, n)
	for i := 0; i < n; i++ {
		go func(id int) {
			r.loop(ctx, id)
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < n; i++ {
		<-done
	}
}

func (r *Recognizer) loop(ctx context.Context, id int) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		processed, err := r.step(ctx)
		if err != nil {
			r.log.Error("recognizer step failed", "worker", id, "error", err)
		}
		if !processed {
			select {
			case <-ctx.Done():
				return
			case <-time.After(r.idle):
			}
		}
	}
}

func (r *Recognizer) step(ctx context.Context) (bool, error) {
	doc, ok, err := r.db.ClaimNextForRecognition(ctx)
	if err != nil || !ok {
		return false, err
	}

	// Один файл может содержать несколько разных документов (типовой случай —
	// счёт и акт от одного поставщика в одном скане). Прежде чем разбирать,
	// проверяем это и при необходимости заменяем запись набором отдельных.
	if parts, err := r.pipeline.Split(ctx, doc); err != nil {
		r.log.Warn("split check failed", "id", doc.ID, "error", err)
	} else if len(parts) > 1 {
		for _, part := range parts {
			if _, err := r.db.CreateDocument(ctx, storage.NewDocument{
				OwnerID:      doc.OwnerID,
				CompanyID:    doc.CompanyID,
				OriginalName: part.OriginalName,
				ContentType:  "application/pdf",
				SizeBytes:    part.SizeBytes,
				StorageKey:   part.StorageKey,
				SHA256:       part.SHA256,
			}); err != nil {
				return true, err
			}
		}
		if err := r.db.DeleteDocument(ctx, doc.ID); err != nil {
			return true, err
		}
		r.log.Info("document split into separate records",
			"id", doc.ID, "name", doc.OriginalName, "parts", len(parts))
		return true, nil
	}

	r.log.Info("recognizing document", "id", doc.ID, "name", doc.OriginalName)
	res, err := r.pipeline.Process(ctx, doc)
	if err != nil {
		r.log.Warn("recognition failed", "id", doc.ID, "error", err)
		if markErr := r.db.MarkFailed(ctx, doc.ID, err.Error()); markErr != nil {
			return true, markErr
		}
		return true, nil
	}

	rec := res.Recognition
	// Маршрут документа решают ТОЛЬКО обязательные реквизиты шапки.
	missing := recognize.MissingRequired(rec)
	rec.Missing = missing

	// Табличная часть разобрана, но сумма строк не сходится с итогом — значит
	// колонки разъехались. Такие позиции в 1С не отправляем (лучше без
	// номенклатуры, чем с неверными суммами), но документ при этом НЕ
	// задерживаем: шапка уходит в 1С как обычно, а позиции сотрудник вносит
	// руками. В выгрузке это видно по <MissingFields><Field>lines</Field>.
	if recognize.LinesTotalMismatch(rec, recognize.LinesTolerance) {
		r.log.Warn("line items do not reconcile with total, sending header only",
			"id", doc.ID, "lines", len(rec.Lines), "total", rec.Fields["total"].Value)
		rec.Lines = nil
		rec.Missing = append(rec.Missing, "lines")
	}

	// Выбор маршрута документа:
	//   - авто-экспорт выключен  → на проверку оператору (прежнее поведение);
	//   - тип не опознан         → на проверку оператору (в 1С мапить не по чему);
	//   - нет маппинга на 1С     → на проверку оператору;
	//   - всё распознано         → сразу подтверждаем и выгружаем в 1С;
	//   - не хватает полей       → распознанное уходит в 1С частично, а клиента
	//                              просят ввести недостающее вручную (досыл позже).
	status := r.route(rec, missing)

	// Компания может требовать согласования главбухом: тогда документ не
	// уходит в 1С сам, а ждёт решения закреплённого за ней главбуха.
	if r.needsApproval(ctx, doc) && (status == domain.StatusConfirmed || status == domain.StatusNeedsInput) {
		status = domain.StatusNeedsApproval
	}

	if err := r.db.SaveRecognition(ctx, doc.ID, rec, res.OCRText, status); err != nil {
		return true, err
	}

	switch status {
	case domain.StatusConfirmed:
		if _, err := r.db.EnqueueExport(ctx, doc.ID, domain.ExportKindCreate); err != nil {
			return true, err
		}
	case domain.StatusNeedsInput:
		// Частичная выгрузка распознанного (если авто-экспорт включён).
		if _, err := r.db.EnqueueExport(ctx, doc.ID, domain.ExportKindPartial); err != nil {
			return true, err
		}
	}

	// Уведомляем владельца о новом статусе.
	doc.Status = status
	doc.Recognition = &rec
	r.notifier.NotifyStatus(ctx, doc.OwnerID, doc)

	r.log.Info("document recognized", "id", doc.ID, "type", rec.DocType, "status", status, "missing", missing)
	return true, nil
}

// needsApproval — включено ли для компании документа обязательное
// согласование главбухом. Ошибку чтения компании трактуем как «не требуется»:
// документооборот важнее, чем строгость флага, а документ в любом случае
// останется виден главбуху.
func (r *Recognizer) needsApproval(ctx context.Context, doc domain.Document) bool {
	if doc.CompanyID == nil {
		return false
	}
	company, err := r.db.Company(ctx, *doc.CompanyID)
	if err != nil {
		r.log.Warn("не удалось прочитать компанию документа", "id", doc.ID, "error", err)
		return false
	}
	return company.ApprovalRequired
}

func (r *Recognizer) route(rec domain.Recognition, missing []string) domain.Status {
	if !r.autoExport {
		return domain.StatusNeedsReview
	}
	if rec.DocType == "" || rec.DocType == recognize.DocTypeUnknown {
		return domain.StatusNeedsReview
	}
	// Тип опознан, но приёмник 1С не знает, что из него создавать: маппинг на
	// объект базы не согласован. Отправлять такое автоматически — значит либо
	// потерять документ, либо положить его не в тот справочник.
	if r.requireMapping && !recognize.ExportRoutable(rec.DocType) {
		return domain.StatusNeedsReview
	}
	if len(missing) == 0 {
		return domain.StatusConfirmed
	}
	return domain.StatusNeedsInput
}
