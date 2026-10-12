package recognize

import "testing"

func TestTypeInboxDocType(t *testing.T) {
	cases := []struct {
		source string
		want   string
		ok     bool
	}{
		{"act", "act", true},
		{"waybill", "waybill", true},
		{"contract", "contract", true},
		{"power_of_attorney", "power_of_attorney", true},
		{"upd", "upd", true},

		{"invoice", "act", true},
		{"receipt", "act", true},
		{"schet_faktura", "act", true},
		{"act_priemki", "act", true},
		{"act_sverki", "act", true},
		{"payment_order", "act", true},

		{"unknown", "", false},
		{"", "", false},
		{"unsupported_document", "", false},
		{"waybill/../contract", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.source, func(t *testing.T) {
			got, ok := TypeInboxDocType(tc.source)
			if got != tc.want || ok != tc.ok {
				t.Fatalf(
					"TypeInboxDocType(%q) = (%q, %v), want (%q, %v)",
					tc.source, got, ok, tc.want, tc.ok,
				)
			}
		})
	}
}
