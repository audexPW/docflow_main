package recognize

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"docflow/internal/domain"
)

// Snapshot comparison replays the FIRST financial patch followed by this
// experimental recovery over every numbered Golden document available on disk.
// New numbered cases are discovered automatically. No OCR/LLM or 1C.
func TestFinancialEvidenceGolden91(t *testing.T) {
	root := os.Getenv("DOCFLOW_GOLDEN_DIR")
	if root == "" {
		t.Skip("DOCFLOW_GOLDEN_DIR not configured")
	}
	names := []string{"amount_no_vat", "vat_amount", "total"}
	validated, before, after, improved, regressed, applied := 0, 0, 0, 0, 0, 0
	paths, err := filepath.Glob(filepath.Join(root, "reports", "secondary", "*_actual.json"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	docs := 0
	for _, ap := range paths {
		id := strings.TrimSuffix(filepath.Base(ap), "_actual.json")
		if _, err := strconv.Atoi(id); err != nil {
			continue // non-numbered auxiliary report
		}
		docs++
		var saved struct {
			Recognition domain.Recognition `json:"recognition"`
			OCRText     string             `json:"ocr_text"`
		}
		var expected struct {
			Fields map[string]struct {
				Value *string `json:"value"`
			} `json:"fields"`
		}
		ep := filepath.Join(root, id, "expected.json")
		ab, err := os.ReadFile(ap)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(ab, &saved); err != nil {
			t.Fatalf("%s actual: %v", id, err)
		}
		eb, err := os.ReadFile(ep)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(eb, &expected); err != nil {
			t.Fatalf("%s expected: %v", id, err)
		}
		ReconcileGroundedSingleLineAmounts(&saved.Recognition, saved.OCRText)
		prior := map[string]bool{}
		for _, key := range names {
			field, ok := expected.Fields[key]
			if !ok || field.Value == nil {
				continue
			}
			want, valid := groundedMoneyCents(*field.Value)
			if !valid {
				t.Fatalf("%s %s expected invalid: %q", id, key, *field.Value)
			}
			got, ok := groundedMoneyCents(saved.Recognition.Fields[key].Value)
			right := ok && got == want
			prior[key] = right
			validated++
			if right {
				before++
			}
		}
		if RecoverFinancialTotalsFromEvidence(&saved.Recognition, saved.OCRText) {
			applied++
			t.Logf("%s recovery applied", id)
		}
		for _, key := range names {
			field, ok := expected.Fields[key]
			if !ok || field.Value == nil {
				continue
			}
			want, _ := groundedMoneyCents(*field.Value)
			got, ok := groundedMoneyCents(saved.Recognition.Fields[key].Value)
			right := ok && got == want
			if right {
				after++
			}
			if !prior[key] && right {
				improved++
				t.Logf("%s %s IMPROVED %s", id, key, saved.Recognition.Fields[key].Value)
			}
			if prior[key] && !right {
				regressed++
				t.Errorf("%s %s REGRESSION: %q expected %q", id, key, saved.Recognition.Fields[key].Value, *field.Value)
			}
		}
	}
	t.Logf("FINANCIAL_RECOVERY DOCS=%d VALIDATED=%d BASE=%d AFTER=%d FIXED=%d REGRESSED=%d APPLIED_DOCS=%d", docs, validated, before, after, improved, regressed, applied)
	if docs < 91 {
		t.Fatalf("Golden dataset unexpectedly shrank: %d documents (expected at least 91)", docs)
	}
	if docs == 91 && (validated != 261 || before != 183) {
		t.Fatalf("unexpected original Golden91 baseline: validated=%d base=%d", validated, before)
	}
	if improved == 0 {
		t.Error("recovery failed to improve any financial field")
	}
	if regressed != 0 {
		t.Errorf("recovery regressed %d formerly correct fields", regressed)
	}
}
