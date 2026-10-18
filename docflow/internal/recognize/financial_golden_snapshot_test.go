package recognize

import (
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"docflow/internal/domain"
)

type financialGoldenActual struct {
	Recognition domain.Recognition `json:"recognition"`
	OCRText     string             `json:"ocr_text"`
}

type financialGoldenExpected struct {
	Fields map[string]struct {
		Value *string `json:"value"`
	} `json:"fields"`
}

var financialGoldenNumber = regexp.MustCompile(
	`^[+-]?[0-9]+([.,][0-9]+)?$`,
)

func financialGoldenMoney(s string) (*big.Rat, bool) {
	for _, v := range []string{
		" ", "\t", "\n", "\u00a0", "\u202f",
	} {
		s = strings.ReplaceAll(s, v, "")
	}
	s = strings.ReplaceAll(s, ",", ".")

	if !financialGoldenNumber.MatchString(s) {
		return nil, false
	}

	r, ok := new(big.Rat).SetString(s)
	return r, ok
}

func financialGoldenEqual(actual, expected string) bool {
	a, okA := financialGoldenMoney(actual)
	e, okE := financialGoldenMoney(expected)

	return okA && okE && a.Cmp(e) == 0
}

func financialGoldenRead(t *testing.T, path string, out any) {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := json.Unmarshal(data, out); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

func TestFinancialGolden91Snapshot(t *testing.T) {
	root := os.Getenv("DOCFLOW_GOLDEN_DIR")
	if root == "" {
		t.Skip("DOCFLOW_GOLDEN_DIR not set")
	}

	keys := []string{
		"amount_no_vat",
		"vat_amount",
		"total",
	}

	validated := 0
	before := 0
	after := 0
	improved := 0
	regressed := 0
	applied := 0

	for i := 1; i <= 91; i++ {
		id := fmt.Sprintf("%03d", i)

		var actual financialGoldenActual
		var expected financialGoldenExpected

		financialGoldenRead(
			t,
			filepath.Join(
				root, "reports", "secondary",
				id+"_actual.json",
			),
			&actual,
		)

		financialGoldenRead(
			t,
			filepath.Join(root, id, "expected.json"),
			&expected,
		)

		original := make(map[string]bool)

		for _, key := range keys {
			field, exists := expected.Fields[key]
			if !exists || field.Value == nil {
				continue
			}

			if _, ok := financialGoldenMoney(*field.Value); !ok {
				t.Fatalf(
					"%s %s: invalid expected money %q",
					id, key, *field.Value,
				)
			}

			validated++

			got := actual.Recognition.Fields[key].Value
			original[key] = financialGoldenEqual(
				got, *field.Value,
			)

			if original[key] {
				before++
			}
		}

		if ReconcileGroundedSingleLineAmounts(
			&actual.Recognition,
			actual.OCRText,
		) {
			applied++
			t.Logf("%s: reconciliation applied", id)
		}

		for _, key := range keys {
			field, exists := expected.Fields[key]
			if !exists || field.Value == nil {
				continue
			}

			got := actual.Recognition.Fields[key].Value
			now := financialGoldenEqual(
				got, *field.Value,
			)

			if now {
				after++
			}

			switch {
			case !original[key] && now:
				improved++
				t.Logf("%s %s: IMPROVED", id, key)

			case original[key] && !now:
				regressed++
				t.Errorf("%s %s: REGRESSION", id, key)
			}
		}
	}

	t.Logf(
		"VALIDATED=%d BEFORE=%d AFTER=%d IMPROVED=%d REGRESSED=%d APPLIED=%d",
		validated, before, after, improved, regressed, applied,
	)

	if validated != 261 || before != 178 {
		t.Fatalf(
			"baseline differs: validated=%d before=%d",
			validated, before,
		)
	}

	if after != 183 || improved != 5 || regressed != 0 {
		t.Fatalf(
			"unexpected result: after=%d improved=%d regressed=%d",
			after, improved, regressed,
		)
	}
}
