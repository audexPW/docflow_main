package config

import (
	"strings"
	"testing"
)

func TestOneCExportProfile(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-only-secret")
	t.Setenv("FILES_ENCRYPTION_KEY", strings.Repeat("11", 32))

	cases := []struct {
		name    string
		value   string
		want    string
		wantErr bool
	}{
		{"default", "", OneCExportProfileLegacy, false},
		{"legacy", "legacy", OneCExportProfileLegacy, false},
		{"new", "type_inbox", OneCExportProfileTypeInbox, false},
		{"normalized", " TYPE_INBOX ", OneCExportProfileTypeInbox, false},
		{"invalid", "typed_inbox", "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ONEC_EXPORT_PROFILE", tc.value)

			cfg, err := Load()
			if tc.wantErr {
				if err == nil ||
					!strings.Contains(err.Error(), "ONEC_EXPORT_PROFILE") {
					t.Fatalf("expected profile validation error, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.OneC.ExportProfile != tc.want {
				t.Fatalf("profile = %q, want %q",
					cfg.OneC.ExportProfile, tc.want)
			}
			if cfg.OneC.TypeInboxEnabled() !=
				(tc.want == OneCExportProfileTypeInbox) {
				t.Fatal("incorrect TypeInboxEnabled result")
			}
		})
	}
}
