package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"

	"docflow/internal/domain"

	"github.com/google/uuid"
)

const companyColumns = `id, name, unp, folder, approval_required, is_active, created_at, updated_at`

func scanCompany(sc scanner, c *domain.Company) error {
	return sc.Scan(&c.ID, &c.Name, &c.UNP, &c.Folder, &c.ApprovalRequired, &c.IsActive, &c.CreatedAt, &c.UpdatedAt)
}

type NewCompany struct {
	Name             string
	UNP              string
	Folder           string
	ApprovalRequired bool
}

func (db *DB) CreateCompany(ctx context.Context, in NewCompany) (domain.Company, error) {
	var c domain.Company
	row := db.QueryRowContext(ctx, `
		INSERT INTO companies (name, unp, folder, approval_required)
		VALUES ($1, $2, $3, $4)
		RETURNING `+companyColumns,
		in.Name, in.UNP, in.Folder, in.ApprovalRequired)
	if err := scanCompany(row, &c); err != nil {
		return c, fmt.Errorf("create company: %w", err)
	}
	return c, nil
}

func (db *DB) Company(ctx context.Context, id uuid.UUID) (domain.Company, error) {
	var c domain.Company
	row := db.QueryRowContext(ctx, `SELECT `+companyColumns+` FROM companies WHERE id = $1`, id)
	if err := scanCompany(row, &c); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return c, ErrNotFound
		}
		return c, err
	}
	return c, nil
}

func (db *DB) ListCompanies(ctx context.Context) ([]domain.Company, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+companyColumns+` FROM companies ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []domain.Company{}
	for rows.Next() {
		var c domain.Company
		if err := scanCompany(rows, &c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CompaniesByIDs — компании из явного списка (то, что доступно конкретному
// пользователю). Пустой список — пустой ответ, а не «все».
func (db *DB) CompaniesByIDs(ctx context.Context, ids []uuid.UUID) ([]domain.Company, error) {
	if len(ids) == 0 {
		return []domain.Company{}, nil
	}
	rows, err := db.QueryContext(ctx,
		`SELECT `+companyColumns+` FROM companies WHERE id = ANY($1) ORDER BY name`, uuidArray(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []domain.Company{}
	for rows.Next() {
		var c domain.Company
		if err := scanCompany(rows, &c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

type CompanyUpdate struct {
	Name             *string
	UNP              *string
	ApprovalRequired *bool
	IsActive         *bool
}

func (db *DB) UpdateCompany(ctx context.Context, id uuid.UUID, in CompanyUpdate) (domain.Company, error) {
	var c domain.Company
	row := db.QueryRowContext(ctx, `
		UPDATE companies
		   SET name              = coalesce($2, name),
		       unp               = coalesce($3, unp),
		       approval_required = coalesce($4, approval_required),
		       is_active         = coalesce($5, is_active),
		       updated_at        = now()
		 WHERE id = $1
		RETURNING `+companyColumns,
		id, in.Name, in.UNP, in.ApprovalRequired, in.IsActive)
	if err := scanCompany(row, &c); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return c, ErrNotFound
		}
		return c, err
	}
	return c, nil
}

// SetUserCompany привязывает пользователя к юрлицу (nil — снять привязку).
// Токены аннулируются: область видимости пользователя только что изменилась.
func (db *DB) SetUserCompany(ctx context.Context, userID uuid.UUID, companyID *uuid.UUID) error {
	res, err := db.ExecContext(ctx, `
		UPDATE users SET company_id = $2, tokens_valid_from = now() WHERE id = $1`, userID, companyID)
	if err != nil {
		return fmt.Errorf("set user company: %w", err)
	}
	return affectedOne(res)
}

// AssignAccountant закрепляет главбуха за компанией. Повторное закрепление
// молча ничего не меняет.
func (db *DB) AssignAccountant(ctx context.Context, companyID, userID uuid.UUID) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO company_accountants (company_id, user_id)
		VALUES ($1, $2) ON CONFLICT DO NOTHING`, companyID, userID)
	return err
}

// UnassignAccountant снимает главбуха с компании — это и есть переназначение:
// админ снимает одного и ставит другого.
func (db *DB) UnassignAccountant(ctx context.Context, companyID, userID uuid.UUID) error {
	_, err := db.ExecContext(ctx, `
		DELETE FROM company_accountants WHERE company_id = $1 AND user_id = $2`, companyID, userID)
	return err
}

// SetCompanyAccountants заменяет весь состав главбухов компании одним списком.
func (db *DB) SetCompanyAccountants(ctx context.Context, companyID uuid.UUID, userIDs []uuid.UUID) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM company_accountants WHERE company_id = $1`, companyID); err != nil {
		return err
	}
	for _, uid := range userIDs {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO company_accountants (company_id, user_id)
			VALUES ($1, $2) ON CONFLICT DO NOTHING`, companyID, uid); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// CompanyAccountants — учётки главбухов, закреплённых за компанией.
func (db *DB) CompanyAccountants(ctx context.Context, companyID uuid.UUID) ([]domain.User, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT `+userColumns+`
		  FROM users u
		  JOIN company_accountants ca ON ca.user_id = u.id
		 WHERE ca.company_id = $1
		 ORDER BY u.login`, companyID)
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

// AccountantCompanyIDs — компании, закреплённые за главбухом.
func (db *DB) AccountantCompanyIDs(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT company_id FROM company_accountants WHERE user_id = $1`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// VisibleCompanyIDs — компании, документы которых пользователь вправе видеть.
// Возвращает nil для администратора: у него ограничений нет. Для остальных —
// список, возможно пустой (тогда не видно ничего, кроме своих загрузок).
func (db *DB) VisibleCompanyIDs(ctx context.Context, u domain.User) ([]uuid.UUID, error) {
	if u.Role == domain.RoleAdmin {
		return nil, nil
	}
	// Оператор без привязки обслуживает всех: он сидит в офисе и принимает
	// документы у любого приехавшего клиента, поэтому ему нужны все компании —
	// иначе выбирать при загрузке будет не из чего.
	if u.Role == domain.RoleOperator && u.CompanyID == nil {
		return nil, nil
	}
	ids := []uuid.UUID{}
	if u.CompanyID != nil {
		ids = append(ids, *u.CompanyID)
	}
	if u.Role == domain.RoleAccountant {
		assigned, err := db.AccountantCompanyIDs(ctx, u.ID)
		if err != nil {
			return nil, err
		}
		for _, id := range assigned {
			if !containsUUID(ids, id) {
				ids = append(ids, id)
			}
		}
	}
	return ids, nil
}

// ApproveDocument — отметка главбуха «согласовано».
func (db *DB) ApproveDocument(ctx context.Context, docID, userID uuid.UUID, note string) error {
	res, err := db.ExecContext(ctx, `
		UPDATE documents
		   SET approved_by = $2, approved_at = now(), approval_note = $3, updated_at = now()
		 WHERE id = $1`, docID, userID, note)
	if err != nil {
		return fmt.Errorf("approve document: %w", err)
	}
	return affectedOne(res)
}

// RejectDocument — отказ главбуха: документ в 1С не уходит, причина видна
// клиенту в карточке.
func (db *DB) RejectDocument(ctx context.Context, docID, userID uuid.UUID, note string) error {
	res, err := db.ExecContext(ctx, `
		UPDATE documents
		   SET status = 'rejected', approved_by = $2, approved_at = now(),
		       approval_note = $3, updated_at = now()
		 WHERE id = $1`, docID, userID, note)
	if err != nil {
		return fmt.Errorf("reject document: %w", err)
	}
	return affectedOne(res)
}

// CountDocumentsAwaitingApproval — сколько документов ждут главбуха (для
// значка в интерфейсе).
func (db *DB) CountDocumentsAwaitingApproval(ctx context.Context, companyIDs []uuid.UUID) (int, error) {
	var n int
	if companyIDs == nil {
		err := db.QueryRowContext(ctx,
			`SELECT count(*) FROM documents WHERE status = 'needs_approval'`).Scan(&n)
		return n, err
	}
	if len(companyIDs) == 0 {
		return 0, nil
	}
	err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM documents WHERE status = 'needs_approval' AND company_id = ANY($1)`,
		uuidArray(companyIDs)).Scan(&n)
	return n, err
}

func containsUUID(list []uuid.UUID, id uuid.UUID) bool {
	for _, v := range list {
		if v == id {
			return true
		}
	}
	return false
}

// uuidArray отдаёт список UUID в виде литерала массива Postgres — так его
// понимает ANY($1). Отдельный пакет ради pq.Array не тянем: UUID приходят
// только из uuid.Parse, спецсимволов в них не бывает.
func uuidArray(ids []uuid.UUID) driver.Valuer {
	out := make(pgUUIDArray, 0, len(ids))
	for _, id := range ids {
		out = append(out, id.String())
	}
	return out
}

type pgUUIDArray []string

func (a pgUUIDArray) Value() (driver.Value, error) {
	if len(a) == 0 {
		return "{}", nil
	}
	buf := make([]byte, 0, len(a)*40+2)
	buf = append(buf, '{')
	for i, s := range a {
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = append(buf, '"')
		buf = append(buf, s...)
		buf = append(buf, '"')
	}
	buf = append(buf, '}')
	return string(buf), nil
}

// SetAccountantCompanies заменяет весь набор компаний одного главбуха. Обратная
// сторона SetCompanyAccountants: там правится состав одной компании, здесь —
// список компаний одного человека. Одной транзакцией, чтобы админ не увидел
// половину сохранённого при обрыве.
func (db *DB) SetAccountantCompanies(ctx context.Context, userID uuid.UUID, companyIDs []uuid.UUID) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM company_accountants WHERE user_id = $1`, userID); err != nil {
		return err
	}
	for _, cid := range companyIDs {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO company_accountants (company_id, user_id)
			VALUES ($1, $2) ON CONFLICT DO NOTHING`, cid, userID); err != nil {
			return err
		}
	}
	return tx.Commit()
}
