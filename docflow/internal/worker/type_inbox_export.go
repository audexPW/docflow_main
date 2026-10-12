package worker

import (
	"fmt"
	"strings"

	"docflow/internal/domain"
	"docflow/internal/onec"

	"github.com/google/uuid"
)

// prepareTypeInboxPayload verifies the document before
// publishing it through the existing XML exporter.
func prepareTypeInboxPayload(
	doc domain.Document,
	kind domain.ExportKind,
	owner domain.User,
) (onec.Payload, error) {
	if doc.OwnerID == uuid.Nil ||
		owner.ID != doc.OwnerID ||
		strings.TrimSpace(owner.Login) == "" {
		return onec.Payload{}, fmt.Errorf(
			"document owner is not verified",
		)
	}

	if strings.EqualFold(
		strings.TrimSpace(owner.Login),
		"golden_test",
	) {
		return onec.Payload{}, fmt.Errorf(
			"golden_test export is prohibited",
		)
	}

	switch kind {
	case domain.ExportKindCreate, domain.ExportKindUpdate:
		if doc.Status != domain.StatusConfirmed {
			return onec.Payload{}, fmt.Errorf(
				"document is not confirmed: %s",
				doc.Status,
			)
		}

	case domain.ExportKindPartial:
		if doc.Status != domain.StatusNeedsInput {
			return onec.Payload{}, fmt.Errorf(
				"partial export has invalid status: %s",
				doc.Status,
			)
		}

	default:
		return onec.Payload{}, fmt.Errorf(
			"invalid export kind %q",
			kind,
		)
	}

	return onec.BuildTypeInboxPayload(doc, kind)
}
