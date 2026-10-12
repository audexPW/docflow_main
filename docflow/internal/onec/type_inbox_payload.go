package onec

import (
	"fmt"

	"docflow/internal/domain"
	"docflow/internal/recognize"
)

// BuildTypeInboxPayload reuses the original XML structure.
// Only document classification and export safety differ.
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
			"type_inbox: unsupported type %q",
			rec.DocType,
		)
	}

	p := BuildPayload(doc, kind)
	p.TypeInbox = true

	p.DocType = docType
	p.DocTypeTitle = recognize.DocTypeTitle(docType)

	if docType == "act" {
		p.DocTypeTitle = "Акт"
	}

	target := recognize.OneCTargetFor(docType)
	p.OneCObject = target.Object
	p.OneCKind = target.Kind

	// Keep the missing fields determined during recognition.
	// No extra diagnostic XML sections are generated.
	p.Missing = append([]string(nil), rec.Missing...)

	invalidLines := containsMissing(p.Missing, "lines")

	if recognize.LinesTotalMismatch(
		*rec, recognize.LinesTolerance,
	) {
		invalidLines = true

		if !containsMissing(p.Missing, "lines") {
			p.Missing = append(p.Missing, "lines")
		}
	}

	if invalidLines {
		p.Lines = nil
		p.Table = nil
	}

	// This is an existing XML attribute.
	p.Incomplete = len(p.Missing) > 0 ||
		len(rec.Faults) > 0 ||
		len(rec.Rejected) > 0

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
