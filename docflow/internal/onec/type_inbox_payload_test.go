package onec

import (
	"bytes"
	"encoding/xml"
	"strings"
	"testing"

	"docflow/internal/domain"

	"github.com/google/uuid"
)

func TestTypeInboxPreservesLegacyXML(t *testing.T) {
	for _, typ := range []string{
		"act",
		"waybill",
		"contract",
		"power_of_attorney",
		"upd",
	} {
		t.Run(typ, func(t *testing.T) {
			doc := domain.Document{
				ID:           uuid.New(),
				OriginalName: "test.pdf",
				Recognition: &domain.Recognition{
					DocType: typ,
					Fields: map[string]domain.Field{
						"number": {Value: "125"},
						"date":   {Value: "12.10.2026"},
					},
					Missing: []string{"total"},
				},
			}

			oldPayload := BuildPayload(
				doc, domain.ExportKindCreate,
			)

			newPayload, err := BuildTypeInboxPayload(
				doc, domain.ExportKindCreate,
			)
			if err != nil {
				t.Fatal(err)
			}

			oldXML, err := xml.Marshal(
				toEnterpriseData(oldPayload),
			)
			if err != nil {
				t.Fatal(err)
			}

			newXML, err := xml.Marshal(
				toEnterpriseData(newPayload),
			)
			if err != nil {
				t.Fatal(err)
			}

			if !bytes.Equal(oldXML, newXML) {
				t.Fatal("original XML structure changed")
			}

			if !newPayload.TypeInbox {
				t.Fatal("typed routing marker missing")
			}

			if strings.Contains(
				string(newXML), "RecognitionQuality",
			) {
				t.Fatal("unexpected XML diagnostics")
			}
		})
	}
}

func TestTypeInboxFaultWithoutNewXMLElements(t *testing.T) {
	doc := domain.Document{
		ID: uuid.New(),
		Recognition: &domain.Recognition{
			DocType: "act",
			Fields: map[string]domain.Field{
				"number": {Value: "125"},
			},
			Faults: []string{"invalid_date"},
		},
	}

	p, err := BuildTypeInboxPayload(
		doc, domain.ExportKindCreate,
	)
	if err != nil {
		t.Fatal(err)
	}

	if !p.Incomplete {
		t.Fatal("incomplete attribute not set")
	}

	body, err := xml.Marshal(toEnterpriseData(p))
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(
		string(body), "RecognitionQuality",
	) {
		t.Fatal("unexpected XML section")
	}
}

func TestTypeInboxDoesNotInventMissing(t *testing.T) {
	doc := domain.Document{
		ID: uuid.New(),
		Recognition: &domain.Recognition{
			DocType: "act",
			Fields: map[string]domain.Field{
				"number": {Value: "125"},
				"date":   {Value: "12.10.2026"},
			},
		},
	}

	p, err := BuildTypeInboxPayload(
		doc, domain.ExportKindCreate,
	)
	if err != nil {
		t.Fatal(err)
	}

	if p.Incomplete || len(p.Missing) != 0 {
		t.Fatal("unexpected missing fields")
	}
}

func TestTypeInboxSuppressesInvalidLines(t *testing.T) {
	rec := &domain.Recognition{
		DocType: "waybill",
		Fields: map[string]domain.Field{
			"total": {Value: "100.00"},
		},
		Missing: []string{"lines"},
		Lines: []domain.LineItem{
			{Name: "A", Amount: "40.00"},
			{Name: "B", Amount: "70.00"},
		},
	}

	p, err := BuildTypeInboxPayload(
		domain.Document{
			ID:          uuid.New(),
			Recognition: rec,
		},
		domain.ExportKindCreate,
	)
	if err != nil {
		t.Fatal(err)
	}

	if !p.Incomplete || p.Lines != nil {
		t.Fatal("invalid lines were exported")
	}

	if len(rec.Lines) != 2 {
		t.Fatal("recognition source was modified")
	}
}

func TestTypeInboxRejectsUnsupported(t *testing.T) {
	cases := []*domain.Recognition{
		nil,
		{DocType: "unknown"},
		{DocType: "unsupported_document"},
	}

	for _, rec := range cases {
		_, err := BuildTypeInboxPayload(
			domain.Document{
				ID:          uuid.New(),
				Recognition: rec,
			},
			domain.ExportKindCreate,
		)

		if err == nil {
			t.Fatalf("unexpected type accepted: %+v", rec)
		}
	}
}
