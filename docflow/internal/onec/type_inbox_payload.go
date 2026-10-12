package onec

import (
	"fmt"
	"strings"

	"docflow/internal/domain"
	"docflow/internal/recognize"
)

// FieldEvidence describes the actual recorded recognition confidence
// and source. It does not imply that a value has been verified.
type FieldEvidence struct {
	Confidence float64 `json:"confidence"`
	Source     string  `json:"source,omitempty"`
}

// BuildTypeInboxPayload prepares a create-only, quality-aware payload.
// Unlike the legacy builder, it includes recognition and integration
// issues without making them a reason to block XML creation.
func BuildTypeInboxPayload(
	doc domain.Document,
	kind domain.ExportKind,
) (Payload, error) {
	rec := doc.Recognition
	if rec == nil {
		return Payload{}, fmt.Errorf(
			"type_inbox: recognition is missing",
		)
	}

	docType, ok := recognize.TypeInboxDocType(rec.DocType)
	if !ok {
		return Payload{}, fmt.Errorf(
			"type_inbox: unsupported document type %q",
			rec.DocType,
		)
	}

	p := BuildPayload(doc, kind)
	p.DocType = docType
	p.DocTypeTitle = recognize.DocTypeTitle(docType)
	if docType == "act" {
		p.DocTypeTitle = "Акт"
	}

	target := recognize.OneCTargetFor(docType)
	p.OneCObject = target.Object
	p.OneCKind = target.Kind

	p.DocTypeConfidence = rec.DocTypeConfidence
	p.Missing = append([]string(nil), rec.Missing...)
	p.Faults = append([]string(nil), rec.Faults...)
	p.Review = append([]string(nil), rec.Review...)

	if len(rec.Rejected) > 0 {
		p.Rejected = make(map[string]string, len(rec.Rejected))
		for key, value := range rec.Rejected {
			p.Rejected[key] = value
		}
	}

	if len(rec.ReviewNotes) > 0 {
		p.ReviewNotes = make(map[string]string, len(rec.ReviewNotes))
		for key, value := range rec.ReviewNotes {
			p.ReviewNotes[key] = value
		}
	}

	// The recognition worker calculated Missing before grouping
	// source document types into their canonical business types.
	// Do not recalculate it here: for example, act_priemki does
	// not require total, while the canonical act type does.

	if len(rec.Fields) > 0 {
		p.FieldEvidence = make(map[string]FieldEvidence)
		for key, field := range rec.Fields {
			if field.Value == "" {
				continue
			}
			p.FieldEvidence[key] = FieldEvidence{
				Confidence: field.Confidence,
				Source:     field.Source,
			}
		}
	}

	// An inconsistent line total must not be passed to accounting
	// as a verified table. Preserve the original in Recognition.
	linesInvalid := containsMissing(p.Missing, "lines")
	if recognize.LinesTotalMismatch(
		*rec, recognize.LinesTolerance,
	) {
		linesInvalid = true
		if !containsMissing(p.Missing, "lines") {
			p.Missing = append(p.Missing, "lines")
		}
	}
	if linesInvalid {
		p.Lines = nil
		p.Table = nil
	}

	p.Incomplete = len(p.Missing) > 0 ||
		len(p.Faults) > 0 ||
		len(p.Rejected) > 0

	// Integration problems are reported separately from
	// recognition incompleteness.
	if strings.TrimSpace(p.OneCObject) == "" {
		p.IntegrationIssues = append(
			p.IntegrationIssues, "onec_mapping_missing",
		)
	}
	if doc.CompanyID == nil {
		p.IntegrationIssues = append(
			p.IntegrationIssues, "company_not_assigned",
		)
	}

	return p, nil
}

func containsMissing(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}
