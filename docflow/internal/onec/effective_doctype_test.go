package onec

import (
	"encoding/xml"
	"strings"
	"testing"

	"docflow/internal/domain"
	"docflow/internal/recognize"
)

func TestEffectiveActXML(t *testing.T) {
	sources := []string{
		"act",
		"schet_faktura",
		"act_priemki",
		"invoice",
		"receipt",
		"payment_order",
		"act_sverki",
	}

	for _, source := range sources {
		t.Run(source, func(t *testing.T) {
			rec := domain.Recognition{
				DocType: recognize.EffectiveDocType(source),
				Fields: map[string]domain.Field{
					"number": {Value: "123"},
					"total":  {Value: "100.00"},
				},
			}

			payload := BuildPayload(
				domain.Document{Recognition: &rec},
				domain.ExportKindCreate,
			)

			if payload.DocType != "act" {
				t.Fatalf("type = %q, want act", payload.DocType)
			}
			if payload.DocTypeTitle != "Акт" {
				t.Fatalf("title = %q, want Акт", payload.DocTypeTitle)
			}

			body, err := xml.Marshal(toEnterpriseData(payload))
			if err != nil {
				t.Fatal(err)
			}

			for _, fragment := range []string{
				"<Type>act</Type>",
				"<TypeTitle>Акт</TypeTitle>",
				"<Number>123</Number>",
				"<Total>100.00</Total>",
			} {
				if !strings.Contains(string(body), fragment) {
					t.Errorf("missing XML fragment: %s", fragment)
				}
			}
		})
	}
}
