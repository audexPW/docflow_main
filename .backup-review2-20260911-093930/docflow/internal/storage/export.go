package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"docflow/internal/domain"

	"github.com/google/uuid"
)

func (db *DB) EnqueueExport(ctx context.Context, documentID uuid.UUID, kind domain.ExportKind) (domain.ExportJob, error) {
	if kind == "" {
		kind = domain.ExportKindCreate
	}
	var j domain.ExportJob
	err := db.QueryRowContext(ctx, `
		INSERT INTO export_jobs (document_id, kind)
		VALUES ($1, $2)
		RETURNING id, document_id, kind, status, attempts, last_error, next_attempt_at, created_at, updated_at`,
		documentID, kind,
	).Scan(&j.ID, &j.DocumentID, &j.Kind, &j.Status, &j.Attempts, &j.LastError, &j.NextAttemptAt, &j.CreatedAt, &j.UpdatedAt)
	return j, err
}

// ClaimNextExport забирает готовую к отправке задачу (с учётом отложенных повторов).
func (db *DB) ClaimNextExport(ctx context.Context) (domain.ExportJob, bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ExportJob{}, false, err
	}
	defer tx.Rollback()

	var id uuid.UUID
	err = tx.QueryRowContext(ctx, `
		SELECT id FROM export_jobs
		WHERE status = 'pending' AND next_attempt_at <= now()
		ORDER BY next_attempt_at
		FOR UPDATE SKIP LOCKED
		LIMIT 1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ExportJob{}, false, nil
	}
	if err != nil {
		return domain.ExportJob{}, false, err
	}

	var j domain.ExportJob
	err = tx.QueryRowContext(ctx, `
		UPDATE export_jobs SET status = 'sending', updated_at = now()
		WHERE id = $1
		RETURNING id, document_id, kind, status, attempts, last_error, next_attempt_at, created_at, updated_at`, id,
	).Scan(&j.ID, &j.DocumentID, &j.Kind, &j.Status, &j.Attempts, &j.LastError, &j.NextAttemptAt, &j.CreatedAt, &j.UpdatedAt)
	if err != nil {
		return domain.ExportJob{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ExportJob{}, false, err
	}
	return j, true, nil
}

func (db *DB) MarkExportDone(ctx context.Context, id uuid.UUID) error {
	_, err := db.ExecContext(ctx, `
		UPDATE export_jobs
		SET status = 'done', attempts = attempts + 1, last_error = '', updated_at = now()
		WHERE id = $1`, id)
	return err
}

// RescheduleExport откладывает повтор; при исчерпании попыток задача помечается failed.
func (db *DB) RescheduleExport(ctx context.Context, id uuid.UUID, reason string, retryIn time.Duration, maxAttempts int) (bool, error) {
	var attempts int
	if err := db.QueryRowContext(ctx,
		`UPDATE export_jobs SET attempts = attempts + 1, last_error = $2, updated_at = now()
		 WHERE id = $1 RETURNING attempts`, id, reason,
	).Scan(&attempts); err != nil {
		return false, err
	}

	if attempts >= maxAttempts {
		_, err := db.ExecContext(ctx,
			`UPDATE export_jobs SET status = 'failed', updated_at = now() WHERE id = $1`, id)
		return false, err
	}

	next := time.Now().Add(retryIn)
	_, err := db.ExecContext(ctx,
		`UPDATE export_jobs SET status = 'pending', next_attempt_at = $2, updated_at = now() WHERE id = $1`,
		id, next)
	return true, err
}

func (db *DB) Audit(ctx context.Context, actorID *uuid.UUID, action, entity, entityID string, meta any) {
	var raw []byte
	if meta != nil {
		raw, _ = json.Marshal(meta)
	}
	_, _ = db.ExecContext(ctx, `
		INSERT INTO audit_log (actor_id, action, entity, entity_id, meta)
		VALUES ($1, $2, $3, $4, $5)`, actorID, action, entity, entityID, raw)
}

type AuditEntry struct {
	ID        int64           `json:"id"`
	ActorID   *uuid.UUID      `json:"actor_id"`
	Action    string          `json:"action"`
	Entity    string          `json:"entity"`
	EntityID  string          `json:"entity_id"`
	Meta      json.RawMessage `json:"meta,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

func (db *DB) ListAudit(ctx context.Context, limit int) ([]AuditEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := db.QueryContext(ctx, `
		SELECT id, actor_id, action, entity, entity_id, meta, created_at
		FROM audit_log ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var raw []byte
		if err := rows.Scan(&e.ID, &e.ActorID, &e.Action, &e.Entity, &e.EntityID, &raw, &e.CreatedAt); err != nil {
			return nil, err
		}
		if len(raw) > 0 {
			e.Meta = json.RawMessage(raw)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
