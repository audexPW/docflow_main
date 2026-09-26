package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"docflow/internal/domain"

	"github.com/google/uuid"
)

var ErrNotFound = errors.New("not found")

// userColumns перечислены один раз: рассинхрон списка колонок и Scan уже
// однажды укладывал систему целиком (Scan ждал больше значений, чем выбирал
// запрос), поэтому и запрос, и разбор строки берут список отсюда.
const userColumns = `id, login, password_hash, role, is_active, must_change_password, tokens_valid_from, company_id, created_at`

func scanUser(sc scanner, u *domain.User) error {
	var companyID uuid.NullUUID
	err := sc.Scan(&u.ID, &u.Login, &u.PasswordHash, &u.Role, &u.IsActive,
		&u.MustChangePassword, &u.TokensValidFrom, &companyID, &u.CreatedAt)
	if err != nil {
		return err
	}
	if companyID.Valid {
		id := companyID.UUID
		u.CompanyID = &id
	} else {
		u.CompanyID = nil
	}
	return nil
}

func (db *DB) CreateUser(ctx context.Context, login, passwordHash string, role domain.Role) (domain.User, error) {
	return db.CreateUserInCompany(ctx, login, passwordHash, role, nil)
}

// CreateUserInCompany заводит учётку сразу с привязкой к юрлицу: клиент и
// оператор без компании не видят ни одного документа, поэтому привязку удобнее
// задать при создании, а не отдельным шагом.
func (db *DB) CreateUserInCompany(ctx context.Context, login, passwordHash string, role domain.Role, companyID *uuid.UUID) (domain.User, error) {
	var u domain.User
	row := db.QueryRowContext(ctx, `
		INSERT INTO users (login, password_hash, role, company_id)
		VALUES ($1, $2, $3, $4)
		RETURNING `+userColumns,
		login, passwordHash, role, companyID)
	if err := scanUser(row, &u); err != nil {
		return u, fmt.Errorf("create user: %w", err)
	}
	return u, nil
}

func (db *DB) UserByLogin(ctx context.Context, login string) (domain.User, error) {
	var u domain.User
	row := db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE login = $1`, login)
	if err := scanUser(row, &u); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return u, ErrNotFound
		}
		return u, err
	}
	return u, nil
}

func (db *DB) UserByID(ctx context.Context, id uuid.UUID) (domain.User, error) {
	var u domain.User
	row := db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = $1`, id)
	if err := scanUser(row, &u); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return u, ErrNotFound
		}
		return u, err
	}
	return u, nil
}

func (db *DB) ListUsers(ctx context.Context) ([]domain.User, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+userColumns+` FROM users ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []domain.User{}
	for rows.Next() {
		var u domain.User
		if err := scanUser(rows, &u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (db *DB) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}
