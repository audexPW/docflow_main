package recognize

import "testing"

func TestEffectiveDocType(t *testing.T) {
	tests := []struct {
		source string
		want   string
	}{
		{"act", "act"},
		{"schet_faktura", "act"},
		{"act_priemki", "act"},
		{"invoice", "act"},
		{"receipt", "act"},
		{"payment_order", "act"},
		{"act_sverki", "act"},
		{"waybill", "waybill"},
		{"upd", "upd"},
		{"contract", "contract"},
		{"power_of_attorney", "power_of_attorney"},
		{"unknown", "unknown"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.source, func(t *testing.T) {
			got := EffectiveDocType(tt.source)
			if got != tt.want {
				t.Errorf("EffectiveDocType(%q) = %q, want %q",
					tt.source, got, tt.want)
			}
		})
	}
}
