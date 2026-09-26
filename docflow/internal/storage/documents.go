package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"docflow/internal/domain"

	"github.com/google/uuid"
)

// documentColumns — единый список колонок для всех запросов документа.
// Разъезд этого списка с scanDocument уже ронял каждый /api/documents
// ("expected 13 destination arguments in Scan, not 16"), поэтому список
// объявлен один раз и подставляется во все запросы.
const documentColumns = `d.id, d.owner_id, d.company_id, d.original_name, d.content_type, d.size_bytes,
	       d.storage_key, d.sha256, d.status, d.recognition, d.ocr_text, d.error,
	       coalesce(d.onec_status,''), coalesce(d.onec_ref,''), coalesce(d.onec_message,''),
	       d.approved_by, d.approved_at, coalesce(d.approval_note,''),
	       d.archived_at, coalesce(c.name,''), d.created_at, d.updated_at`

// documentsFrom — источник строк: документ вместе с названием своей компании,
// чтобы интерфейс и выгрузка не ходили за ним отдельным запросом.
const documentsFrom = ` FROM documents d LEFT JOIN companies c ON c.id = d.company_id`

type NewDocument struct {
	OwnerID      uuid.UUID
	CompanyID    *uuid.UUID
	OriginalName string
	ContentType  string
	SizeBytes    int64
	StorageKey   string
	SHA256       string
}

func (db *DB) CreateDocument(ctx context.Context, in NewDocument) (domain.Document, error) {
	var id uuid.UUID
	err := db.QueryRowContext(ctx, `
		INSERT INTO documents (owner_id, company_id, original_name, content_type, size_bytes, storage_key, sha256)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id`,
		in.OwnerID, in.CompanyID, in.OriginalName, in.ContentType, in.SizeBytes, in.StorageKey, in.SHA256,
	).Scan(&id)
	if err != nil {
		return domain.Document{}, fmt.Errorf("create document: %w", err)
	}
	return db.Document(ctx, id)
}

func (db *DB) Document(ctx context.Context, id uuid.UUID) (domain.Document, error) {
	var d domain.Document
	row := db.QueryRowContext(ctx,
		`SELECT `+documentColumns+documentsFrom+` WHERE d.id = $1`, id)
	if err := scanDocument(row, &d); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return d, ErrNotFound
		}
		return d, err
	}
	return d, nil
}

type DocumentFilter struct {
	OwnerID *uuid.UUID
	// CompanyIDs ограничивает выборку юрлицами, доступными пользователю.
	// nil — без ограничения (администратор); пустой непустой-указатель
	// (len == 0 при Scoped == true) — не видно ничего.
	CompanyIDs []uuid.UUID
	Scoped     bool
	Status     domain.Status
	// Archive выбирает, что показывать: "" — только рабочие документы
	// (проведённые в 1С скрыты), "only" — только архив, "all" — всё.
	// По умолчанию архив из общего списка убран: ради этого он и заведён.
	Archive string
	Limit   int
	Offset  int
}

// Значения DocumentFilter.Archive.
const (
	ArchiveHide = ""     // архивные скрыть
	ArchiveOnly = "only" // только архивные
	ArchiveAll  = "all"  // и те и другие
)

// documentWhere собирает условия выборки. Один код на список и на подсчёт:
// разъедься они — счётчик «показано N из M» начал бы врать, а именно по нему
// пользователь понимает, что список ещё не кончился.
func documentWhere(f DocumentFilter) (where []string, args []any, empty bool) {
	if f.OwnerID != nil {
		args = append(args, *f.OwnerID)
		where = append(where, fmt.Sprintf("d.owner_id = $%d", len(args)))
	}
	if f.Scoped {
		if len(f.CompanyIDs) == 0 {
			// Пользователь не привязан ни к одной компании — своих документов
			// он ещё может не иметь, но чужие видеть не должен.
			return nil, nil, true
		}
		args = append(args, uuidArray(f.CompanyIDs))
		where = append(where, fmt.Sprintf("d.company_id = ANY($%d)", len(args)))
	}
	if f.Status != "" {
		args = append(args, f.Status)
		where = append(where, fmt.Sprintf("d.status = $%d", len(args)))
	}
	switch f.Archive {
	case ArchiveOnly:
		where = append(where, "d.archived_at IS NOT NULL")
	case ArchiveAll:
		// без условия
	default:
		where = append(where, "d.archived_at IS NULL")
	}
	return where, args, false
}

// CountDocuments — сколько всего документов подходит под фильтр. Нужен списку:
// без общего числа страница не знает, есть ли что подгружать дальше.
func (db *DB) CountDocuments(ctx context.Context, f DocumentFilter) (int, error) {
	where, args, empty := documentWhere(f)
	if empty {
		return 0, nil
	}
	q := `SELECT count(*)` + documentsFrom
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	var n int
	err := db.QueryRowContext(ctx, q, args...).Scan(&n)
	return n, err
}

func (db *DB) ListDocuments(ctx context.Context, f DocumentFilter) ([]domain.Document, error) {
	where, args, empty := documentWhere(f)
	if empty {
		return []domain.Document{}, nil
	}

	q := `SELECT ` + documentColumns + documentsFrom
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY d.created_at DESC"
	// Потолок поднят до 500: страница листается порциями, и упор в прежние 200
	// оставлял часть документов недостижимой вовсе.
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 50
	}
	args = append(args, f.Limit)
	q += fmt.Sprintf(" LIMIT $%d", len(args))
	if f.Offset < 0 {
		f.Offset = 0
	}
	args = append(args, f.Offset)
	q += fmt.Sprintf(" OFFSET $%d", len(args))

	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []domain.Document{}
	for rows.Next() {
		var d domain.Document
		if err := scanDocument(rows, &d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ClaimNextForRecognition атомарно забирает один непринятый документ и переводит
// его в статус processing. SKIP LOCKED позволяет запускать несколько воркеров.
func (db *DB) ClaimNextForRecognition(ctx context.Context) (domain.Document, bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Document{}, false, err
	}
	defer tx.Rollback()

	var id uuid.UUID
	err = tx.QueryRowContext(ctx, `
		SELECT id FROM documents
		WHERE status = 'received'
		ORDER BY created_at
		FOR UPDATE SKIP LOCKED
		LIMIT 1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Document{}, false, nil
	}
	if err != nil {
		return domain.Document{}, false, err
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE documents SET status = 'processing', updated_at = now() WHERE id = $1`, id); err != nil {
		return domain.Document{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Document{}, false, err
	}

	d, err := db.Document(ctx, id)
	if err != nil {
		return domain.Document{}, false, err
	}
	return d, true, nil
}

func (db *DB) SaveRecognition(ctx context.Context, id uuid.UUID, r domain.Recognition, ocrText string, status domain.Status) error {
	payload, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
		UPDATE documents
		SET recognition = $2, ocr_text = $3, status = $4, error = '', updated_at = now()
		WHERE id = $1`, id, payload, ocrText, status)
	return err
}

// DeleteDocument удаляет запись документа. Нужен для случая, когда в одном
// файле оказалось несколько документов: исходная запись заменяется набором
// отдельных, каждая со своим файлом.
func (db *DB) DeleteDocument(ctx context.Context, id uuid.UUID) error {
	_, err := db.ExecContext(ctx, `DELETE FROM documents WHERE id = $1`, id)
	return err
}

func (db *DB) MarkFailed(ctx context.Context, id uuid.UUID, reason string) error {
	_, err := db.ExecContext(ctx, `
		UPDATE documents SET status = 'failed', error = $2, updated_at = now()
		WHERE id = $1`, id, reason)
	return err
}

func (db *DB) UpdateRecognition(ctx context.Context, id uuid.UUID, r domain.Recognition) error {
	payload, err := json.Marshal(r)
	if err != nil {
		return err
	}
	res, err := db.ExecContext(ctx, `
		UPDATE documents SET recognition = $2, updated_at = now() WHERE id = $1`, id, payload)
	if err != nil {
		return err
	}
	return affected(res)
}

func (db *DB) SetStatus(ctx context.Context, id uuid.UUID, status domain.Status) error {
	res, err := db.ExecContext(ctx, `
		UPDATE documents SET status = $2, updated_at = now() WHERE id = $1`, id, status)
	if err != nil {
		return err
	}
	return affected(res)
}

// SetDocumentCompany переставляет документ на другое юрлицо (админ разбирает
// загрузку, сделанную не под той учёткой).
func (db *DB) SetDocumentCompany(ctx context.Context, id uuid.UUID, companyID *uuid.UUID) error {
	res, err := db.ExecContext(ctx, `
		UPDATE documents SET company_id = $2, updated_at = now() WHERE id = $1`, id, companyID)
	if err != nil {
		return err
	}
	return affected(res)
}

// RequeueForRecognition возвращает документ на повторное распознавание.
func (db *DB) RequeueForRecognition(ctx context.Context, id uuid.UUID) error {
	res, err := db.ExecContext(ctx, `
		UPDATE documents SET status = 'received', error = '', updated_at = now()
		WHERE id = $1`, id)
	if err != nil {
		return err
	}
	return affected(res)
}

// ArchiveDocument переводит документ в архив: 1С подтвердила проведение,
// работать с ним больше не нужно. Повторные подтверждения отметку не сдвигают —
// иначе срок хранения начинался бы заново с каждого сообщения из 1С.
func (db *DB) ArchiveDocument(ctx context.Context, id uuid.UUID) error {
	_, err := db.ExecContext(ctx, `
		UPDATE documents SET archived_at = now(), updated_at = now()
		WHERE id = $1 AND archived_at IS NULL`, id)
	return err
}

// UnarchiveDocument снимает архивную отметку (1С переоткрыла документ или
// отметка проставлена ошибочно).
func (db *DB) UnarchiveDocument(ctx context.Context, id uuid.UUID) error {
	res, err := db.ExecContext(ctx, `
		UPDATE documents SET archived_at = NULL, updated_at = now() WHERE id = $1`, id)
	if err != nil {
		return err
	}
	return affected(res)
}

// ExpiredArchived — архивные документы, у которых вышел срок хранения.
// Возвращаем и ключ файла, и папку компании: удалять надо всё разом — запись,
// зашифрованный файл и выгрузку в папке обмена этого юрлица.
type ExpiredDocument struct {
	ID            uuid.UUID
	StorageKey    string
	OriginalName  string
	CompanyFolder string
	CompanyID     *uuid.UUID
	ArchivedAt    time.Time
}

func (db *DB) ExpiredArchived(ctx context.Context, keepDays, limit int) ([]ExpiredDocument, error) {
	if keepDays <= 0 {
		return nil, nil
	}
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	rows, err := db.QueryContext(ctx, `
		SELECT d.id, d.storage_key, d.original_name, coalesce(c.folder,''), d.company_id, d.archived_at
		  FROM documents d
		  LEFT JOIN companies c ON c.id = d.company_id
		 WHERE d.archived_at IS NOT NULL
		   AND d.archived_at < now() - ($1 || ' days')::interval
		 ORDER BY d.archived_at
		 LIMIT $2`, keepDays, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ExpiredDocument
	for rows.Next() {
		var e ExpiredDocument
		var companyID uuid.NullUUID
		if err := rows.Scan(&e.ID, &e.StorageKey, &e.OriginalName, &e.CompanyFolder, &companyID, &e.ArchivedAt); err != nil {
			return nil, err
		}
		if companyID.Valid {
			id := companyID.UUID
			e.CompanyID = &id
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanDocument(sc scanner, d *domain.Document) error {
	var (
		raw        []byte
		companyID  uuid.NullUUID
		approvedBy uuid.NullUUID
		approvedAt sql.NullTime
		archivedAt sql.NullTime
	)
	err := sc.Scan(
		&d.ID, &d.OwnerID, &companyID, &d.OriginalName, &d.ContentType, &d.SizeBytes,
		&d.StorageKey, &d.SHA256, &d.Status, &raw, &d.OCRText, &d.Error,
		&d.OneCStatus, &d.OneCRef, &d.OneCMessage,
		&approvedBy, &approvedAt, &d.ApprovalNote,
		&archivedAt, &d.CompanyName, &d.CreatedAt, &d.UpdatedAt,
	)
	if err != nil {
		return err
	}
	if companyID.Valid {
		id := companyID.UUID
		d.CompanyID = &id
	}
	if approvedBy.Valid {
		id := approvedBy.UUID
		d.ApprovedBy = &id
	}
	if approvedAt.Valid {
		t := approvedAt.Time
		d.ApprovedAt = &t
	}
	if archivedAt.Valid {
		t := archivedAt.Time
		d.ArchivedAt = &t
	}
	if len(raw) > 0 {
		var r domain.Recognition
		if err := json.Unmarshal(raw, &r); err != nil {
			return fmt.Errorf("decode recognition: %w", err)
		}
		d.Recognition = &r
	}
	return nil
}

func affected(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
