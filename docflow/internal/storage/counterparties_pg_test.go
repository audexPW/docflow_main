package storage

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"docflow/internal/domain"

	"github.com/google/uuid"
)

// Проверка на живой базе: DOCFLOW_TEST_DSN=postgres://… go test ./internal/storage/
func TestCounterpartiesOnPostgres(t *testing.T) {
	dsn := os.Getenv("DOCFLOW_TEST_DSN")
	if dsn == "" {
		t.Skip("DOCFLOW_TEST_DSN не задан")
	}
	ctx := context.Background()
	db, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var owner, company uuid.UUID
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(db.QueryRowContext(ctx, `INSERT INTO users (login, password_hash, role) VALUES ($1,'x','admin') RETURNING id`, uuid.NewString()).Scan(&owner))
	must(db.QueryRowContext(ctx, `INSERT INTO companies (name, unp, folder) VALUES ('ООО «КофеВенд»','192226948',$1) RETURNING id`, uuid.NewString()).Scan(&company))
	addDoc := func(status, unp, cp string) uuid.UUID {
		rec := domain.Recognition{DocType: "act", Fields: map[string]domain.Field{
			"unp": {Value: unp}, "counterparty": {Value: cp},
		}}
		raw, _ := json.Marshal(rec)
		var id uuid.UUID
		must(db.QueryRowContext(ctx, `INSERT INTO documents (owner_id, company_id, original_name, content_type, size_bytes, storage_key, sha256, status, recognition)
			VALUES ($1,$2,'a.jpg','image/jpeg',1,'k','s',$3,$4) RETURNING id`, owner, company, status, raw).Scan(&id))
		return id
	}
	addDoc("exported", "100000017", "КУП «Чижовский рынок»")
	addDoc("exported", "100000017", "КУП Чижовский рынок")
	addDoc("exported", "100000017", "КУП «Чижовский рынок»")
	addDoc("exported", "192226948", "ЗАО Суперпрод")      // номер компании — пропустить
	addDoc("exported", "300000011", "ОАО КофеВенд")       // сама компания — пропустить
	addDoc("needs_approval", "400000019", "ЧУП Черновик") // не выгружен

	n, err := db.SeedCounterparties(ctx)
	must(err)
	if n != 1 {
		t.Fatalf("наполнено %d записей, ожидалась 1", n)
	}
	name, ok, err := db.CounterpartyByUNP(ctx, &company, "100000017")
	must(err)
	if !ok || name != "КУП «Чижовский рынок»" {
		t.Fatalf("по УНП: %q %v", name, ok)
	}
	unp, canon, ok, err := db.CounterpartyByName(ctx, &company, "Коммунальное унитарное предприятие Чижовский рынок")
	must(err)
	if !ok || unp != "100000017" || canon != "КУП «Чижовский рынок»" {
		t.Fatalf("по наименованию: %q %q %v", unp, canon, ok)
	}
	if again, _ := db.SeedCounterparties(ctx); again != 0 {
		t.Fatal("повторное наполнение")
	}

	doc := addDoc("confirmed", "500000014", "ООО «Новый поставщик»")
	_, err = db.EnqueueExport(ctx, doc, domain.ExportKindCreate)
	must(err)
	if _, ok, _ := db.CounterpartyByUNP(ctx, &company, "500000014"); !ok {
		t.Fatal("контрагент выгруженного документа не попал в справочник")
	}
	must(db.UpsertCounterparty(ctx, &company, "500000014", "ООО Новый поставщик", "export"))
	must(db.UpsertCounterparty(ctx, &company, "500000014", "ООО Новый поставщик", "export"))
	var syn int
	must(db.QueryRowContext(ctx, `SELECT cardinality(synonyms) FROM counterparties WHERE unp='500000014'`).Scan(&syn))
	if syn != 1 {
		t.Fatalf("синонимов %d, ожидался 1", syn)
	}
	if _, ok, _ := db.CounterpartyByUNP(ctx, nil, "500000014"); ok {
		t.Fatal("справочник компании виден документам без компании")
	}
}
