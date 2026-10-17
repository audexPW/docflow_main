package recognize

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// This test is intentionally dependent on the existing Golden91 snapshot
// helpers committed in the previous, guarded financial reconciliation patch.
func TestFinancialConfidenceGolden91(t *testing.T) {
	root := os.Getenv("DOCFLOW_GOLDEN_DIR")
	if root == "" {
		t.Skip("DOCFLOW_GOLDEN_DIR not set")
	}

	keys := []string{"amount_no_vat", "vat_amount", "total"}
	correctBefore, correctAfter := 0, 0
	highWrongBefore, highWrongAfter, highCorrectLost := 0, 0, 0
	flagged, falseAlerts := 0, 0
	flaggedDocs := make(map[string]bool)

	for i := 1; i <= 91; i++ {
		id := fmt.Sprintf("%03d", i)
		var actual financialGoldenActual
		var expected financialGoldenExpected
		financialGoldenRead(t, filepath.Join(root, "reports", "secondary", id+"_actual.json"), &actual)
		financialGoldenRead(t, filepath.Join(root, id, "expected.json"), &expected)

		// The new guard runs after the already committed, opt-in table repair.
		ReconcileGroundedSingleLineAmounts(&actual.Recognition, actual.OCRText)
		correctFields, wrongFields := 0, 0
		initialConf := make(map[string]float64)
		for _, k := range keys {
			field, ok := expected.Fields[k]
			if !ok || field.Value == nil {
				continue
			}
			f := actual.Recognition.Fields[k]
			initialConf[k] = f.Confidence
			isCorrect := financialGoldenEqual(f.Value, *field.Value)
			if isCorrect {
				correctBefore++
				correctFields++
			} else {
				wrongFields++
			}
			if !isCorrect && f.Confidence >= 0.9 {
				highWrongBefore++
			}
		}

		guarded := ApplyFinancialConfidenceGuard(&actual.Recognition, actual.OCRText)
		if guarded {
			flagged++
			flaggedDocs[id] = true
			t.Logf("%s guarded; wrong fields=%d", id, wrongFields)
			if wrongFields == 0 {
				falseAlerts++
				t.Errorf("%s: false alert on all-grounded correct financial fields", id)
			}
			if actual.Recognition.AutoPass || len(actual.Recognition.Faults) == 0 {
				t.Errorf("%s: failed to block unsafe auto pass", id)
			}
		}
		for _, k := range keys {
			field, ok := expected.Fields[k]
			if !ok || field.Value == nil {
				continue
			}
			f := actual.Recognition.Fields[k]
			isCorrect := financialGoldenEqual(f.Value, *field.Value)
			if isCorrect {
				correctAfter++
			}
			if !isCorrect && f.Confidence >= 0.9 {
				highWrongAfter++
			}
			if isCorrect && initialConf[k] >= 0.9 && f.Confidence < 0.9 {
				highCorrectLost++
			}
		}
	}

	t.Logf("CORRECT_BEFORE=%d CORRECT_AFTER=%d FLAGGED=%d FALSE_ALERT_DOCS=%d HIGH_WRONG_BEFORE=%d HIGH_WRONG_AFTER=%d HIGH_CORRECT_DOWNGRADED=%d", correctBefore, correctAfter, flagged, falseAlerts, highWrongBefore, highWrongAfter, highCorrectLost)
	if correctBefore != 183 || correctAfter != 183 {
		t.Fatalf("unexpected numeric baseline/recalculation: %d -> %d", correctBefore, correctAfter)
	}
	if highWrongAfter >= highWrongBefore {
		t.Errorf("no reduction in high-confidence mistakes: %d -> %d", highWrongBefore, highWrongAfter)
	}
	for _, id := range []string{"033", "039", "087"} {
		if !flaggedDocs[id] {
			t.Errorf("known wrong zero VAT was not guarded: %s", id)
		}
	}
	for _, id := range []string{"003", "004", "007", "014", "030", "053", "061", "086", "012", "051"} {
		if flaggedDocs[id] {
			t.Errorf("known correct/existing table repair was unnecessarily guarded: %s", id)
		}
	}
}
