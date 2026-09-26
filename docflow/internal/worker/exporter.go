package worker

import (
	"context"
	"log/slog"
	"time"

	"docflow/internal/domain"
	"docflow/internal/onec"
	"docflow/internal/push"
	"docflow/internal/storage"
)

type Exporter struct {
	db          *storage.DB
	exporter    onec.Exporter
	notifier    push.Notifier
	log         *slog.Logger
	maxAttempts int
	idle        time.Duration
}

func NewExporter(db *storage.DB, exp onec.Exporter, notifier push.Notifier, maxAttempts int, log *slog.Logger) *Exporter {
	if maxAttempts < 1 {
		maxAttempts = 5
	}
	return &Exporter{db: db, exporter: exp, notifier: notifier, log: log, maxAttempts: maxAttempts, idle: 3 * time.Second}
}

func (e *Exporter) Run(ctx context.Context, n int) {
	if n < 1 {
		n = 1
	}
	done := make(chan struct{}, n)
	for i := 0; i < n; i++ {
		go func(id int) {
			e.loop(ctx, id)
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < n; i++ {
		<-done
	}
}

func (e *Exporter) loop(ctx context.Context, id int) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		processed, err := e.step(ctx)
		if err != nil {
			e.log.Error("exporter step failed", "worker", id, "error", err)
		}
		if !processed {
			select {
			case <-ctx.Done():
				return
			case <-time.After(e.idle):
			}
		}
	}
}

func (e *Exporter) step(ctx context.Context) (bool, error) {
	job, ok, err := e.db.ClaimNextExport(ctx)
	if err != nil || !ok {
		return false, err
	}

	doc, err := e.db.Document(ctx, job.DocumentID)
	if err != nil {
		_, rErr := e.db.RescheduleExport(ctx, job.ID, "load document: "+err.Error(), backoff(job.Attempts), e.maxAttempts)
		return true, rErr
	}

	payload := onec.BuildPayload(doc, job.Kind)
	// Реквизиты юрлица: по ним файловая выгрузка выбирает папку обмена, а 1С
	// понимает, на какую организацию оформлять документ.
	if doc.CompanyID != nil {
		company, cErr := e.db.Company(ctx, *doc.CompanyID)
		if cErr != nil {
			e.log.Warn("не удалось прочитать компанию документа", "document_id", doc.ID, "error", cErr)
		} else {
			payload = payload.WithCompany(company)
		}
	}
	if err := e.exporter.Export(ctx, payload); err != nil {
		e.log.Warn("1c export failed", "document_id", doc.ID, "kind", job.Kind, "attempt", job.Attempts+1, "error", err)
		retry, rErr := e.db.RescheduleExport(ctx, job.ID, err.Error(), backoff(job.Attempts), e.maxAttempts)
		if rErr != nil {
			return true, rErr
		}
		if !retry {
			e.log.Error("1c export gave up after max attempts", "document_id", doc.ID)
		}
		return true, nil
	}

	if err := e.db.MarkExportDone(ctx, job.ID); err != nil {
		return true, err
	}

	// Частичная выгрузка не завершает жизненный цикл: документ остаётся в
	// needs_input и ждёт ручного ввода недостающих полей. Полная выгрузка и
	// досыл переводят его в exported.
	if job.Kind == domain.ExportKindPartial {
		e.log.Info("1c partial export sent", "document_id", doc.ID)
		return true, nil
	}

	if err := e.db.SetStatus(ctx, doc.ID, domain.StatusExported); err != nil {
		return true, err
	}
	doc.Status = domain.StatusExported
	e.notifier.NotifyStatus(ctx, doc.OwnerID, doc)
	e.log.Info("document exported to 1c", "document_id", doc.ID, "kind", job.Kind)
	return true, nil
}

// backoff растёт экспоненциально от числа уже сделанных попыток с потолком.
func backoff(attempts int) time.Duration {
	const (
		base = 30 * time.Second
		max  = 30 * time.Minute
	)
	d := base
	for i := 0; i < attempts; i++ {
		d *= 2
		if d >= max {
			return max
		}
	}
	return d
}
