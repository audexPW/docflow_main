package onec

import (
	"encoding/xml"
	"strings"
	"testing"

	"docflow/internal/domain"

	"github.com/google/uuid"
)

func TestTypeInboxXMLQualityEndToEnd(t *testing.T) {
	doc := domain.Document{
		ID: uuid.New(),
		Recognition: &domain.Recognition{
			DocType:           "upd",
			DocTypeConfidence: 0.42,
			Fields: map[string]domain.Field{
				"number": {
					Value:      "UPD-7",
					Confidence: 0.36,
					Source:     "model",
				},
			},
			Missing: []string{"date"},
			Faults:  []string{"invalid_date"},
			Rejected: map[string]string{
				"date": "31.02.2026 & <invalid>",
			},
			Review: []string{"date"},
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

	body, err := xml.Marshal(toEnterpriseData(p))
	if err != nil {
		t.Fatal(err)
	}
	output := string(body)

	fragments := []string{
		`<RecognitionQuality typeConfidence="0.42">`,
		`<Faults><Fault>invalid_date</Fault></Faults>`,
		`<RejectedFields>`,
		`31.02.2026 &amp; &lt;invalid&gt;`,
		`<ReviewFields><Field>date</Field></ReviewFields>`,
		`<ReviewNotes>`,
		`<FieldEvidence>`,
		`name="number" confidence="0.36" source="model"`,
		`<IntegrationIssues>`,
		`<Issue>company_not_assigned</Issue>`,
		`<MissingFields><Field>date</Field>`,
		`incomplete="true"`,
	}

	for _, fragment := range fragments {
		if !strings.Contains(output, fragment) {
			t.Errorf("missing XML fragment %q", fragment)
		}
	}
}

func TestTypeInboxXMLDoesNotChangeLegacy(t *testing.T) {
	p := Payload{
		DocumentID: "legacy-document",
		DocType:    "act",
		Missing:    []string{"date"},
		Incomplete: true,
		Fields:     map[string]string{},
	}

	before, err := xml.Marshal(toEnterpriseData(p))
	if err != nil {
		t.Fatal(err)
	}

	// These new diagnostic fields must be inert in legacy.
	p.Faults = []string{"invalid_total"}
	p.Rejected = map[string]string{"date": "bad-date"}
	p.FieldEvidence = map[string]FieldEvidence{
		"number": {Confidence: 0.25, Source: "model"},
	}
	p.IntegrationIssues = []string{"onec_mapping_missing"}
	p.DocTypeConfidence = 0.17

	after, err := xml.Marshal(toEnterpriseData(p))
	if err != nil {
		t.Fatal(err)
	}

	if string(before) != string(after) {
		t.Fatal("legacy XML changed due to new diagnostic fields")
	}
	if strings.Contains(
		string(after), "<RecognitionQuality",
	) {
		t.Fatal("type_inbox diagnostics leaked into legacy")
	}
}

func TestTypeInboxXMLCleanDocumentHasQualityMarker(t *testing.T) {
	p := Payload{
		DocumentID: "clean-document",
		DocType:    "act",
		TypeInbox:  true,
		Fields:     map[string]string{},
	}

	body, err := xml.Marshal(toEnterpriseData(p))
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(
		string(body),
		`<RecognitionQuality typeConfidence="0">`,
	) {
		t.Fatal("new profile quality marker is missing")
	}
}

func TestTypeInboxXMLDeterministicFieldOrder(t *testing.T) {
	p := Payload{
		DocumentID: "sorted-document",
		TypeInbox:  true,
		Fields:     map[string]string{},
		Rejected: map[string]string{
			"rejected_z": "z",
			"rejected_a": "a",
		},
		FieldEvidence: map[string]FieldEvidence{
			"evidence_z": {Confidence: 0.5},
			"evidence_a": {Confidence: 0.4},
		},
	}

	for i := 0; i < 20; i++ {
		body, err := xml.Marshal(toEnterpriseData(p))
		if err != nil {
			t.Fatal(err)
		}
		output := string(body)

		if strings.Index(output, `name="rejected_a"`) >=
			strings.Index(output, `name="rejected_z"`) ||
			strings.Index(output, `name="evidence_a"`) >=
				strings.Index(output, `name="evidence_z"`) {
			t.Fatal("diagnostic XML order is not deterministic")
		}
	}
}
