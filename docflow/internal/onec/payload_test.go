package onec

import (
	"encoding/xml"
	"strings"
	"testing"

	"docflow/internal/domain"

	"github.com/google/uuid"
)

// Табличная часть должна доезжать от распознавания до XML для 1С:
// накладная без номенклатуры для бухгалтерии бесполезна.
func TestPayloadCarriesLinesToXML(t *testing.T) {
	doc := domain.Document{
		ID:           uuid.New(),
		OriginalName: "nakladnaya.pdf",
		Recognition: &domain.Recognition{
			DocType: "waybill",
			Fields: map[string]domain.Field{
				"number":       {Value: "145"},
				"date":         {Value: "12.03.2026"},
				"total":        {Value: "375.00"},
				"unp":          {Value: "191234567"},
				"counterparty": {Value: `ООО "Ромашка"`},
			},
			Lines: []domain.LineItem{
				{Name: "Бумага офисная А4", Qty: "10", Unit: "шт", Price: "12.50", Amount: "125.00"},
				{Name: "Картридж HP CF217A", Qty: "2", Unit: "шт", Price: "95.00", Amount: "190.00", VAT: "20"},
			},
		},
	}

	p := BuildPayload(doc, domain.ExportKindCreate)
	if len(p.Lines) != 2 {
		t.Fatalf("строки не попали в payload: %+v", p.Lines)
	}
	if p.UNP != "191234567" {
		t.Errorf("УНП не проброшен: %q", p.UNP)
	}

	body, err := xml.MarshalIndent(toEnterpriseData(p), "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := string(body)

	for _, want := range []string{
		"<Lines>", "Бумага офисная А4", "<Qty>10</Qty>", "<Amount>125.00</Amount>",
		"<UNP>191234567</UNP>", "Картридж HP CF217A", "<VAT>20</VAT>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("в XML нет %q\n%s", want, out)
		}
	}
}

func TestPayloadMarksIncomplete(t *testing.T) {
	doc := domain.Document{
		ID: uuid.New(),
		Recognition: &domain.Recognition{
			DocType: "invoice",
			Fields:  map[string]domain.Field{"number": {Value: "1"}},
			Missing: []string{"date", "total", "unp"},
		},
	}
	p := BuildPayload(doc, domain.ExportKindPartial)
	if !p.Incomplete || len(p.Missing) != 3 {
		t.Fatalf("частичная выгрузка не помечена: %+v", p)
	}
}
