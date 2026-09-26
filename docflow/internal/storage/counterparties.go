package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"docflow/internal/domain"
	"docflow/internal/parties"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

// Справочник контрагентов по УНП. См. migrations/0006_counterparties.sql.

var zeroCompany = uuid.UUID{}

func companyArg(companyID *uuid.UUID) uuid.UUID {
	if companyID == nil {
		return zeroCompany
	}
	return *companyID
}

// CounterpartyByUNP — каноническое наименование контрагента по УНП.
func (db *DB) CounterpartyByUNP(ctx context.Context, companyID *uuid.UUID, unp string) (string, bool, error) {
	var name string
	err := db.QueryRowContext(ctx, `
		SELECT name FROM counterparties
		WHERE COALESCE(company_id, '00000000-0000-0000-0000-000000000000'::uuid) = $1 AND unp = $2`,
		companyArg(companyID), unp).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return name, true, nil
}

// CounterpartyByName ищет контрагента по наименованию или синониму. Находка
// засчитывается, только если подходит ровно одна запись: две похожие
// «Минские» больницы — повод отдать документ человеку, а не угадывать.
func (db *DB) CounterpartyByName(ctx context.Context, companyID *uuid.UUID, name string) (unp, canonical string, ok bool, err error) {
	rows, err := db.QueryContext(ctx, `
		SELECT unp, name, synonyms FROM counterparties
		WHERE COALESCE(company_id, '00000000-0000-0000-0000-000000000000'::uuid) = $1`,
		companyArg(companyID))
	if err != nil {
		return "", "", false, err
	}
	defer rows.Close()
	hits := 0
	for rows.Next() {
		var u, n string
		var syn pq.StringArray
		if err := rows.Scan(&u, &n, &syn); err != nil {
			return "", "", false, err
		}
		match := parties.Same(n, name)
		for _, s := range syn {
			if match {
				break
			}
			match = parties.Same(s, name)
		}
		if match {
			hits++
			unp, canonical = u, n
		}
	}
	if err := rows.Err(); err != nil {
		return "", "", false, err
	}
	if hits != 1 {
		return "", "", false, nil
	}
	return unp, canonical, true, nil
}

// UpsertCounterparty добавляет контрагента или новое написание к известному.
func (db *DB) UpsertCounterparty(ctx context.Context, companyID *uuid.UUID, unp, name, source string) error {
	unp, name = strings.TrimSpace(unp), strings.TrimSpace(name)
	if !parties.ValidUNP(unp) || name == "" {
		return nil
	}
	var cid any
	if companyID != nil {
		cid = *companyID
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO counterparties (company_id, unp, name, source)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (COALESCE(company_id, '00000000-0000-0000-0000-000000000000'::uuid), unp)
		DO UPDATE SET
		    synonyms = CASE
		        WHEN lower(counterparties.name) = lower(EXCLUDED.name)
		          OR lower(EXCLUDED.name) = ANY (SELECT lower(x) FROM unnest(counterparties.synonyms) x)
		        THEN counterparties.synonyms
		        ELSE array_append(counterparties.synonyms, EXCLUDED.name)
		    END,
		    updated_at = now()`,
		cid, unp, name, source)
	return err
}

// LearnCounterparty пополняет справочник по документу, уходящему в 1С.
// Номер компании и её собственное наименование контрагентом не становятся:
// именно так в справочник попали бы перепутанные стороны.
func (db *DB) LearnCounterparty(ctx context.Context, documentID uuid.UUID) error {
	var raw []byte
	var companyID *uuid.UUID
	var companyName, companyUNP sql.NullString
	err := db.QueryRowContext(ctx, `
		SELECT d.recognition, d.company_id, c.name, c.unp
		FROM documents d LEFT JOIN companies c ON c.id = d.company_id
		WHERE d.id = $1`, documentID).Scan(&raw, &companyID, &companyName, &companyUNP)
	if err != nil || len(raw) == 0 {
		return err
	}
	var rec domain.Recognition
	if err := json.Unmarshal(raw, &rec); err != nil {
		return err
	}
	unp := strings.TrimSpace(rec.Fields["unp"].Value)
	name := strings.TrimSpace(rec.Fields["counterparty"].Value)
	if !parties.ValidUNP(unp) || name == "" {
		return nil
	}
	if companyUNP.Valid && companyUNP.String == unp {
		return nil
	}
	if companyName.Valid && parties.Same(companyName.String, name) {
		return nil
	}
	return db.UpsertCounterparty(ctx, companyID, unp, name, "export")
}

// SeedCounterparties наполняет пустой справочник из уже выгруженных
// документов. Для каждого УНП берётся самое частое написание; документы, где
// контрагентом стоит сама компания или её номер, пропускаются. Справочник не
// пуст — ничего не делает. Возвращает число добавленных записей.
func (db *DB) SeedCounterparties(ctx context.Context) (int, error) {
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM counterparties`).Scan(&n); err != nil || n > 0 {
		return 0, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT d.company_id, COALESCE(c.name, ''), COALESCE(c.unp, ''),
		       d.recognition->'fields'->'unp'->>'value',
		       d.recognition->'fields'->'counterparty'->>'value'
		FROM documents d LEFT JOIN companies c ON c.id = d.company_id
		WHERE d.status = 'exported'
		  AND d.recognition->'fields'->'unp'->>'value' ~ '^[0-9]{9}$'
		  AND COALESCE(d.recognition->'fields'->'counterparty'->>'value', '') <> ''`)
	if err != nil {
		return 0, err
	}
	type key struct {
		company uuid.UUID
		unp     string
	}
	type agg struct {
		companyID *uuid.UUID
		counts    map[string]int
	}
	seen := map[key]*agg{}
	var order []key
	for rows.Next() {
		var cid *uuid.UUID
		var cname, cunp, unp, name string
		if err := rows.Scan(&cid, &cname, &cunp, &unp, &name); err != nil {
			rows.Close()
			return 0, err
		}
		name = strings.TrimSpace(name)
		if unp == cunp || (cname != "" && parties.Same(cname, name)) {
			continue
		}
		k := key{companyArg(cid), unp}
		a, ok := seen[k]
		if !ok {
			a = &agg{companyID: cid, counts: map[string]int{}}
			seen[k] = a
			order = append(order, k)
		}
		a.counts[name]++
	}
	rows.Close()
	added := 0
	for _, k := range order {
		a := seen[k]
		best, bestN := "", 0
		for name, c := range a.counts {
			if c > bestN || (c == bestN && name < best) {
				best, bestN = name, c
			}
		}
		if err := db.UpsertCounterparty(ctx, a.companyID, k.unp, best, "seed"); err != nil {
			return added, err
		}
		for name := range a.counts {
			if name != best {
				_ = db.UpsertCounterparty(ctx, a.companyID, k.unp, name, "seed")
			}
		}
		added++
	}
	return added, nil
}
