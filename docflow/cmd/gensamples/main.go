// Утилита генерации эталонных XML для 1С-разработчика: примеры собираются тем
// же кодом, что и боевая выгрузка, поэтому не могут разойтись с форматом.
// Запуск из каталога docflow/: go run ./cmd/gensamples ../onec-samples
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"docflow/internal/config"
	"docflow/internal/domain"
	"docflow/internal/onec"
	"docflow/internal/recognize"

	"github.com/google/uuid"
)

func main() {
	outDir := "../onec-samples"
	if len(os.Args) > 1 {
		outDir = os.Args[1]
	}
	recognize.SetLocale("by")
	if _, err := recognize.LoadDocTypes("../onec-samples/doctypes.sample.json"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	id := uuid.MustParse("7a1c3e64-2b8d-4a1e-9c11-0f2a5b6d7e80")
	field := func(v string) domain.Field { return domain.Field{Value: v, Confidence: 0.9, Source: "rule"} }

	full := domain.Document{
		ID: id, OriginalName: "scan.jpg",
		Recognition: &domain.Recognition{
			DocType: "schet_faktura",
			Fields: map[string]domain.Field{
				"number": field("ЭСЧФ-000451"), "date": field("05.07.2026"),
				"total": field("3540.00"), "vat_amount": field("590.00"),
				"currency": field("BYN"), "unp": field("191234567"),
				"counterparty": field(`ООО "Ромашка"`), "organization": field(`ЧУП "ПартнёрБухгалтер"`),
				"organization_unp": field("190987654"),
			},
			Lines: []domain.LineItem{
				{Name: "Бумага А4 500 л.", Qty: "10", Unit: "пач", Price: "295.00", Amount: "2950.00", VAT: "20%"},
				{Name: "Тонер HP 26A", Qty: "1", Unit: "шт", Price: "590.00", Amount: "590.00", VAT: "20%"},
			},
		},
	}

	partial := full
	rec := *full.Recognition
	rec.Fields = map[string]domain.Field{
		"number": field("ЭСЧФ-000451"), "date": field("05.07.2026"),
		"counterparty": field(`ООО "Ромашка"`), "organization": field(`ЧУП "ПартнёрБухгалтер"`),
	}
	rec.Lines = nil
	rec.Missing = []string{"total", "unp"}
	partial.Recognition = &rec

	cases := []struct {
		name string
		doc  domain.Document
		kind domain.ExportKind
	}{
		{"create.xml", full, domain.ExportKindCreate},
		{"partial.xml", partial, domain.ExportKindPartial},
		{"update.xml", full, domain.ExportKindUpdate},
	}

	tmp, err := os.MkdirTemp("", "gensamples-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(tmp)

	exp, err := onec.NewExporter(config.OneCConfig{Mode: "file", FileDir: tmp}, nil)
	if err != nil {
		panic(err)
	}
	for _, c := range cases {
		if err := exp.Export(context.Background(), onec.BuildPayload(c.doc, c.kind)); err != nil {
			panic(err)
		}
		entries, err := os.ReadDir(tmp)
		if err != nil || len(entries) != 1 {
			panic(fmt.Sprintf("ожидался ровно один файл в %s", tmp))
		}
		src := filepath.Join(tmp, entries[0].Name())
		body, err := os.ReadFile(src)
		if err != nil {
			panic(err)
		}
		if err := os.WriteFile(filepath.Join(outDir, c.name), body, 0o644); err != nil {
			panic(err)
		}
		_ = os.Remove(src)
		fmt.Println("написан", c.name)
	}
}
