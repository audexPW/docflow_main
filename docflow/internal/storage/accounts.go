package storage

import (
	"context"
	"database/sql"

	"fmt"
	"time"

	"github.com/google/uuid"
)

// SetPassword меняет пароль и одновременно инвалидирует все ранее выданные
// токены этого пользователя: tokens_valid_from сдвигается на текущий момент.
// Без этого смена пароля не выгоняет того, кто уже вошёл, — а именно ради
// этого пароль обычно и меняют.
func (db *DB) SetPassword(ctx context.Context, id uuid.UUID, hash string) error {
	res, err := db.ExecContext(ctx, `
		UPDATE users
		   SET password_hash = $2,
		       password_changed_at = now(),
		       must_change_password = FALSE,
		       tokens_valid_from = now()
		 WHERE id = $1`, id, hash)
	if err != nil {
		return fmt.Errorf("set password: %w", err)
	}
	return affectedOne(res)
}

// SetUserActive блокирует или разблокирует учётную запись. Блокировка тоже
// инвалидирует выданные токены — иначе уволенный сотрудник продолжит работать
// до истечения срока действия токена.
func (db *DB) SetUserActive(ctx context.Context, id uuid.UUID, active bool) error {
	res, err := db.ExecContext(ctx, `
		UPDATE users
		   SET is_active = $2,
		       tokens_valid_from = CASE WHEN $2 THEN tokens_valid_from ELSE now() END
		 WHERE id = $1`, id, active)
	if err != nil {
		return fmt.Errorf("set user active: %w", err)
	}
	return affectedOne(res)
}

// SetUserRole меняет роль. Токен несёт роль внутри себя, поэтому смену роли
// тоже сопровождаем инвалидацией — иначе новые права (или их отзыв) вступят в
// силу только после истечения токена.
func (db *DB) SetUserRole(ctx context.Context, id uuid.UUID, role string) error {
	res, err := db.ExecContext(ctx, `
		UPDATE users SET role = $2, tokens_valid_from = now() WHERE id = $1`, id, role)
	if err != nil {
		return fmt.Errorf("set user role: %w", err)
	}
	return affectedOne(res)
}

// CountActiveAdmins нужен, чтобы нельзя было заблокировать или разжаловать
// последнего администратора и остаться без доступа к системе.
func (db *DB) CountActiveAdmins(ctx context.Context, excluding uuid.UUID) (int, error) {
	var n int
	err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM users
		 WHERE role = 'admin' AND is_active AND id <> $1`, excluding).Scan(&n)
	return n, err
}

// SetOneCStatus принимает подтверждение из 1С (ТЗ §2, §10: обратная
// синхронизация статусов). Ищем документ по его же SourceId, который 1С
// получила в выгрузке.
func (db *DB) SetOneCStatus(ctx context.Context, id uuid.UUID, status, ref, message string) error {
	res, err := db.ExecContext(ctx, `
		UPDATE documents
		   SET onec_status = $2,
		       onec_ref = NULLIF($3, ''),
		       onec_message = NULLIF($4, ''),
		       onec_updated_at = now(),
		       updated_at = now()
		 WHERE id = $1`, id, status, ref, message)
	if err != nil {
		return fmt.Errorf("set 1c status: %w", err)
	}
	return affectedOne(res)
}

// StaleOutboxFiles — документы, выгруженные раньше указанного момента, по
// которым 1С так и не отчиталась. Используется для оповещения: молчание
// приёмника не должно выглядеть как успех.
func (db *DB) CountUnacknowledgedExports(ctx context.Context, olderThan time.Duration) (int, error) {
	var n int
	err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM documents
		 WHERE status = 'exported'
		   AND onec_status IS NULL
		   AND updated_at < now() - $1::interval`,
		fmt.Sprintf("%d seconds", int(olderThan.Seconds())),
	).Scan(&n)
	return n, err
}

func affectedOne(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
