package onec

import (
	"testing"

	"docflow/internal/domain"

	"github.com/google/uuid"
)

func TestTypeInboxPayloadKeepsQualityFlags(t *testing.T) {
	doc := domain.Document{
		ID: uuid.New(),
		Recognition: &domain.Recognition{
			DocType:           "upd",
			DocTypeConfidence: 0.42,
			Fields: map[string]domain.Field{
				"number": {
					Value:      "UPD-7",
					Confidence: 0.35,
					Source:     "model",
				},
			},
			Missing: []string{"date"},
			Faults:  []string{"invalid_date"},
			Rejected: map[string]string{
				"date": "31.02.2026",
			},
			ReviewNotes: map[string]string{
				"date": "invalid calendar date",
			},
		},
	}

	p, err := BuildTypeInboxPayload(
		doc, domain.ExportKindCreate,
	)
	if err != nil {
		t.Fatal(err)
	}
	if p.DocType != "upd" || !p.Incomplete {
		t.Fatalf("incorrect type or completeness: %+v", p)
	}
	if len(p.Faults) != 1 ||
		p.Rejected["date"] != "31.02.2026" {
		t.Fatal("recognition quality flags lost")
	}
	if p.FieldEvidence["number"].Confidence != 0.35 {
		t.Fatal("field confidence lost")
	}
	if p.Number != "UPD-7" || p.Date != "" {
		t.Fatal("field was fabricated or lost")
	}
	if !containsMissing(
		p.IntegrationIssues, "company_not_assigned",
	) {
		t.Fatal("missing company warning")
	}
}

// A transfer acceptance act can legitimately have no total.
// Its source-specific Missing list must survive grouping to act.
func TestTypeInboxPayloadDoesNotInventMissingAfterGrouping(t *testing.T) {
	rec := &domain.Recognition{
		DocType: "act",
		Fields: map[string]domain.Field{
			"number": {Value: "15", Confidence: 0.9},
			"date":   {Value: "12.03.2026", Confidence: 0.9},
		},
		Missing: nil,
	}

	p, err := BuildTypeInboxPayload(
		domain.Document{ID: uuid.New(), Recognition: rec},
		domain.ExportKindCreate,
	)
	if err != nil {
		t.Fatal(err)
	}

	if len(p.Missing) != 0 || p.Incomplete {
		t.Fatalf("unexpected missing fields: %v", p.Missing)
	}
	if p.Total != "" {
		t.Fatalf("total was fabricated: %q", p.Total)
	}
}

func TestTypeInboxPayloadFaultWithoutMissing(t *testing.T) {
	rec := &domain.Recognition{
		DocType: "act",
		Fields:  completeActFields(),
		Faults:  []string{"arithmetic_mismatch"},
	}

	p, err := BuildTypeInboxPayload(
		domain.Document{ID: uuid.New(), Recognition: rec},
		domain.ExportKindCreate,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Incomplete {
		t.Fatal("fault-only document must be incomplete")
	}

	legacy := BuildPayload(
		domain.Document{ID: uuid.New(), Recognition: rec},
		domain.ExportKindCreate,
	)
	if legacy.Incomplete || len(legacy.Faults) != 0 {
		t.Fatal("legacy payload behavior changed")
	}
}

func TestTypeInboxPayloadCleanRecognition(t *testing.T) {
	companyID := uuid.New()
	p, err := BuildTypeInboxPayload(
		domain.Document{
			ID:        uuid.New(),
			CompanyID: &companyID,
			Recognition: &domain.Recognition{
				DocType: "act",
				Fields:  completeActFields(),
			},
		},
		domain.ExportKindCreate,
	)
	if err != nil {
		t.Fatal(err)
	}
	if p.Incomplete {
		t.Fatalf("clean recognition marked incomplete: %v",
			p.Missing)
	}
}

func TestTypeInboxPayloadSuppressesInvalidLines(t *testing.T) {
	rec := &domain.Recognition{
		DocType: "waybill",
		Fields: map[string]domain.Field{
			"total": {Value: "100.00", Confidence: 0.9},
		},
		Missing: []string{"lines"},
		Lines: []domain.LineItem{
			{Name: "Item A", Amount: "40.00"},
			{Name: "Item B", Amount: "70.00"},
		},
		Table: &domain.Table{
			Rows: []domain.TableRow{{Index: 1}},
		},
	}

	p, err := BuildTypeInboxPayload(
		domain.Document{ID: uuid.New(), Recognition: rec},
		domain.ExportKindCreate,
	)
	if err != nil {
		t.Fatal(err)
	}
	if p.Lines != nil || p.Table != nil || !p.Incomplete {
		t.Fatal("invalid lines were not excluded")
	}
	if len(rec.Lines) != 2 || rec.Table == nil {
		t.Fatal("original recognition data was modified")
	}
}

func TestTypeInboxPayloadRejectsUnsupported(t *testing.T) {
	cases := []*domain.Recognition{
		nil,
		{DocType: "unknown"},
		{DocType: "unsupported_document"},
	}
	for _, rec := range cases {
		_, err := BuildTypeInboxPayload(
			domain.Document{ID: uuid.New(), Recognition: rec},
			domain.ExportKindCreate,
		)
		if err == nil {
			t.Fatalf("expected rejection for %+v", rec)
		}
	}
}

func completeActFields() map[string]domain.Field {
	return map[string]domain.Field{
		"number": {Value: "1", Confidence: 0.9},
		"date":   {Value: "12.03.2026", Confidence: 0.9},
		"total":  {Value: "100.00", Confidence: 0.9},
	}
}
