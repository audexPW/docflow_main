package storage

import (
	"context"

	"docflow/internal/domain"

	"github.com/google/uuid"
)

// UpsertDeviceToken привязывает токен к пользователю. Один токен принадлежит
// одному пользователю — при повторной регистрации перепривязываем.
func (db *DB) UpsertDeviceToken(ctx context.Context, userID uuid.UUID, platform, token string) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO device_tokens (user_id, platform, token)
		VALUES ($1, $2, $3)
		ON CONFLICT (token)
		DO UPDATE SET user_id = EXCLUDED.user_id, platform = EXCLUDED.platform, updated_at = now()`,
		userID, platform, token)
	return err
}

func (db *DB) DeviceTokensForUser(ctx context.Context, userID uuid.UUID) ([]domain.DeviceToken, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, user_id, platform, token, created_at, updated_at
		FROM device_tokens WHERE user_id = $1`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.DeviceToken
	for rows.Next() {
		var t domain.DeviceToken
		if err := rows.Scan(&t.ID, &t.UserID, &t.Platform, &t.Token, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (db *DB) DeleteDeviceToken(ctx context.Context, token string) error {
	_, err := db.ExecContext(ctx, `DELETE FROM device_tokens WHERE token = $1`, token)
	return err
}
