package worker

import (
	"testing"

	"docflow/internal/domain"

	"github.com/google/uuid"
)

func TestTypeInboxWorkerPreflight(t *testing.T) {
	createDoc := func() domain.Document {
		return domain.Document{
			ID:      uuid.New(),
			OwnerID: uuid.New(),
			Status:  domain.StatusConfirmed,
			Recognition: &domain.Recognition{
				DocType: "act",
			},
		}
	}

	cases := []struct {
		name   string
		kind   domain.ExportKind
		modify func(*domain.Document, *domain.User)
		reject bool
	}{
		{
			name: "act allowed",
			kind: domain.ExportKindCreate,
		},
		{
			name: "waybill allowed",
			kind: domain.ExportKindCreate,
			modify: func(d *domain.Document, _ *domain.User) {
				d.Recognition.DocType = "waybill"
			},
		},
		{
			name: "contract allowed",
			kind: domain.ExportKindCreate,
			modify: func(d *domain.Document, _ *domain.User) {
				d.Recognition.DocType = "contract"
			},
		},
		{
			name: "power of attorney allowed",
			kind: domain.ExportKindCreate,
			modify: func(d *domain.Document, _ *domain.User) {
				d.Recognition.DocType = "power_of_attorney"
			},
		},
		{
			name: "upd allowed",
			kind: domain.ExportKindCreate,
			modify: func(d *domain.Document, _ *domain.User) {
				d.Recognition.DocType = "upd"
			},
		},
		{
			name: "update allowed",
			kind: domain.ExportKindUpdate,
		},
		{
			name: "partial allowed for needs input",
			kind: domain.ExportKindPartial,
			modify: func(d *domain.Document, _ *domain.User) {
				d.Status = domain.StatusNeedsInput
			},
		},
		{
			name: "golden test blocked",
			kind: domain.ExportKindCreate,
			modify: func(_ *domain.Document, u *domain.User) {
				u.Login = "golden_test"
			},
			reject: true,
		},
		{
			name: "golden test case insensitive",
			kind: domain.ExportKindCreate,
			modify: func(_ *domain.Document, u *domain.User) {
				u.Login = " GOLDEN_TEST "
			},
			reject: true,
		},
		{
			name: "unknown type blocked",
			kind: domain.ExportKindCreate,
			modify: func(d *domain.Document, _ *domain.User) {
				d.Recognition.DocType = "unknown"
			},
			reject: true,
		},
		{
			name: "unsupported type blocked",
			kind: domain.ExportKindCreate,
			modify: func(d *domain.Document, _ *domain.User) {
				d.Recognition.DocType = "unsupported"
			},
			reject: true,
		},
		{
			name: "missing recognition blocked",
			kind: domain.ExportKindCreate,
			modify: func(d *domain.Document, _ *domain.User) {
				d.Recognition = nil
			},
			reject: true,
		},
		{
			name: "wrong owner blocked",
			kind: domain.ExportKindCreate,
			modify: func(_ *domain.Document, u *domain.User) {
				u.ID = uuid.New()
			},
			reject: true,
		},
		{
			name: "unconfirmed document blocked",
			kind: domain.ExportKindCreate,
			modify: func(d *domain.Document, _ *domain.User) {
				d.Status = domain.StatusNeedsReview
			},
			reject: true,
		},
		{
			name:   "invalid kind blocked",
			kind:   domain.ExportKind("invalid"),
			reject: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := createDoc()
			owner := domain.User{
				ID:    doc.OwnerID,
				Login: "regular_user",
			}

			if tc.modify != nil {
				tc.modify(&doc, &owner)
			}

			payload, err := prepareTypeInboxPayload(
				doc, tc.kind, owner,
			)

			if tc.reject {
				if err == nil {
					t.Fatal("prohibited export accepted")
				}
				return
			}

			if err != nil {
				t.Fatal(err)
			}

			if !payload.TypeInbox ||
				payload.DocumentID != doc.ID.String() {
				t.Fatal("incorrect typed export payload")
			}
		})
	}
}

func TestLegacyWorkerUnchangedByDefault(t *testing.T) {
	exp := NewExporter(
		nil, nil, nil, 3, nil,
	)

	if exp.typeInbox {
		t.Fatal("legacy changed to typed profile")
	}

	exp.ConfigureTypeInbox()

	if !exp.typeInbox {
		t.Fatal("typed profile was not activated")
	}
}
