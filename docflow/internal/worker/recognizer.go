package worker

import (
	"context"
	"log/slog"
	"time"

	"docflow/internal/domain"
	"docflow/internal/push"
	"docflow/internal/recognize"
	"docflow/internal/storage"

	"github.com/google/uuid"
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
	// Пустой справочник контрагентов наполняем из уже выгруженных
	// документов, иначе первые недели ни один документ не пройдёт проверку
	// «контрагент найден по УНП».
	if added, err := r.db.SeedCounterparties(ctx); err != nil {
		r.log.Warn("справочник контрагентов: первичное наполнение не удалось", "error", err)
	} else if added > 0 {
		r.log.Info("справочник контрагентов наполнен из выгруженных документов", "записей", added)
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
	company := r.company(ctx, doc)
	hint := recognize.ProcessHint{Directory: directory{ctx: ctx, db: r.db, companyID: doc.CompanyID, log: r.log}}
	if company != nil && company.UNP != "" {
		hint.Company = &recognize.CompanyHint{Name: company.Name, UNP: company.UNP}
	}
	res, err := r.pipeline.ProcessFor(ctx, doc, hint)
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
	//
	// Разбор от 10.09.2026, пункт 2. Раньше сюда попадал КАЖДЫЙ документ такой
	// компании, как бы хорошо он ни был распознан: на согласовании было 78 из
	// 100, после правок качества — 76. Документ попадал к человеку не потому,
	// что распознан плохо, а потому, что не было правила, по которому он мог
	// пройти мимо. Теперь правило есть (recognize.DecideAutoPass): уверенность
	// обязательных полей выше порога, арифметика сошлась, контрагент найден
	// по УНП. Прошёл — уходит в 1С сам; не прошёл — к главбуху, и в карточке
	// подсвечены ровно непрошедшие поля. AUTO_PASS_ENABLED=false — как раньше.
	// Требование заказчика от 13.09.2026: при ошибках распознавания статус не
	// должен позволять сформировать XML. Главбух отправляет данные пакетом, и
	// documento с недостающими или отклонёнными полями ломает ему пакет.
	// Поэтому такой документ не идёт ни в 1С, ни на согласование: он получает
	// «На проверке», и выгрузка по нему не ставится в очередь.
	faulty := recognize.RecognitionFaulty(rec)
	if faulty && !recognize.PartialExportEnabled() {
		if status == domain.StatusConfirmed || status == domain.StatusNeedsInput {
			r.log.Info("ошибки распознавания — документ на проверку, XML не формируется",
				"id", doc.ID, "поля", rec.Faults, "не_найдено", rec.Missing)
		}
		status = domain.StatusNeedsReview
	}

	approval := company != nil && company.ApprovalRequired
	if approval && (status == domain.StatusConfirmed || status == domain.StatusNeedsInput) {
		if recognize.AutoPassEnabled() && rec.AutoPass && status == domain.StatusConfirmed {
			r.log.Info("документ прошёл автоматическую проверку, согласование не требуется", "id", doc.ID)
		} else {
			status = domain.StatusNeedsApproval
		}
	}

	// Golden Dataset использует выделенную учётку golden_test, но общую очередь
	// документов с Production. Какой именно backend-worker заберёт документ,
	// заранее неизвестно. Поэтому тестовый документ распознаётся полностью,
	// но никогда не ставится в очередь экспорта в 1С.
	owner, ownerErr := r.db.UserByID(ctx, doc.OwnerID)
	if ownerErr != nil {
		r.log.Warn("не удалось определить владельца документа", "id", doc.ID, "error", ownerErr)
	} else if owner.Login == "golden_test" {
		status = domain.StatusNeedsReview
		r.log.Info("golden test document: export disabled", "id", doc.ID)
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
		// Частичная выгрузка распознанного. По умолчанию выключена: заказчик
		// требует, чтобы при ошибках распознавания XML не формировался
		// (EXPORT_PARTIAL_ENABLED=true возвращает прежнее поведение).
		if recognize.PartialExportEnabled() {
			if _, err := r.db.EnqueueExport(ctx, doc.ID, domain.ExportKindPartial); err != nil {
				return true, err
			}
		}
	}

	// Уведомляем владельца о новом статусе.
	doc.Status = status
	doc.Recognition = &rec
	r.notifier.NotifyStatus(ctx, doc.OwnerID, doc)

	r.log.Info("document recognized", "id", doc.ID, "type", rec.DocType, "status", status, "missing", missing)
	return true, nil
}

// company — компания документа. Ошибку чтения трактуем как «компании нет»:
// согласование тогда не требуется (документооборот важнее строгости флага),
// а стороны определяются без УНП компании.
func (r *Recognizer) company(ctx context.Context, doc domain.Document) *domain.Company {
	if doc.CompanyID == nil {
		return nil
	}
	company, err := r.db.Company(ctx, *doc.CompanyID)
	if err != nil {
		r.log.Warn("не удалось прочитать компанию документа", "id", doc.ID, "error", err)
		return nil
	}
	return &company
}

// directory — справочник контрагентов компании для распознавания. Ошибка базы
// равна «не найдено»: документ тогда просто пойдёт человеку.
type directory struct {
	ctx       context.Context
	db        *storage.DB
	companyID *uuid.UUID
	log       *slog.Logger
}

func (d directory) ByUNP(unp string) (string, bool) {
	name, ok, err := d.db.CounterpartyByUNP(d.ctx, d.companyID, unp)
	if err != nil {
		d.log.Warn("справочник контрагентов: поиск по УНП", "error", err)
		return "", false
	}
	return name, ok
}

func (d directory) ByName(name string) (string, string, bool) {
	unp, canonical, ok, err := d.db.CounterpartyByName(d.ctx, d.companyID, name)
	if err != nil {
		d.log.Warn("справочник контрагентов: поиск по наименованию", "error", err)
		return "", "", false
	}
	return unp, canonical, ok
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
