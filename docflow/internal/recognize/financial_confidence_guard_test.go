package recognize

import (
	"strings"
	"testing"

	"docflow/internal/domain"
)

func guardFixture(no, vat, total string, sources map[string]string) *domain.Recognition {
	return &domain.Recognition{
		AutoPass: true,
		Fields: map[string]domain.Field{
			"amount_no_vat": {Value: no, Confidence: 0.93, Source: sources["amount_no_vat"]},
			"vat_amount":    {Value: vat, Confidence: 0.95, Source: sources["vat_amount"]},
			"total":         {Value: total, Confidence: 0.98, Source: sources["total"]},
		},
	}
}

func defaultSources() map[string]string {
	return map[string]string{"amount_no_vat": "derived", "vat_amount": "model", "total": "model"}
}

func TestFinancialConfidenceGuard(t *testing.T) {
	t.Run("033 nonzero VAT in table contradicts zero", func(t *testing.T) {
		rec := guardFixture("8.33", "0", "8.33", defaultSources())
		rec.Table = &domain.Table{
			Columns: []domain.TableColumn{{Index: 1, Title: "Name", Role: "name"}, {Index: 2, Title: "\u0421\u0443\u043c\u043c\u0430 \u041d\u0414\u0421", Role: "vat"}},
			Rows:    []domain.TableRow{{Index: 1, Cells: []domain.TableCell{{Column: 1, Role: "name", Value: "Rent"}, {Column: 2, Role: "vat", Value: "1.67"}}}},
		}
		text := "8.33 1.67 10.00 \u0421\u0443\u043c\u043c\u0430 \u041d\u0414\u0421: \u041e\u0434\u0438\u043d \u0440\u0443\u0431\u043b\u044c 67 \u043a\u043e\u043f\u0435\u0435\u043a"
		if !ApplyFinancialConfidenceGuard(rec, text) {
			t.Fatal("missing guard")
		}
		if rec.AutoPass || len(rec.Faults) == 0 {
			t.Fatal("must prevent unsafe auto-pass")
		}
		for _, k := range financialGuardKeys {
			if rec.Fields[k].Confidence > financialConfidenceGuardCap {
				t.Errorf("%s: still highly confident", k)
			}
		}
	})
	t.Run("039 OCR corrupted and zero not supported", func(t *testing.T) {
		rec := guardFixture("207.88", "0", "207.88", defaultSources())
		if !ApplyFinancialConfidenceGuard(rec, "\u0441 \u041d\u0414\u0421, Bceso Cymma 207.88") {
			t.Fatal("missing guard")
		}
	})
	t.Run("087 zero not supported despite numeric total", func(t *testing.T) {
		rec := guardFixture("24.34", "0", "24.34", defaultSources())
		if !ApplyFinancialConfidenceGuard(rec, "20.28 4.06 24.34 \u0441 \u041d\u0414\u0421") {
			t.Fatal("missing guard")
		}
	})
	t.Run("014 zero VAT written in words", func(t *testing.T) {
		rec := guardFixture("153.47", "0", "153.47", defaultSources())
		text := "153.47 \u0421\u0443\u043c\u043c\u0430 \u041d\u0414\u0421: \u041d\u043e\u043b\u044c \u0431\u0435\u043b\u043e\u0440\u0443\u0441\u0441\u043a\u0438\u0445 \u0440\u0443\u0431\u043b\u0435\u0439 00 \u043a\u043e\u043f\u0435\u0435\u043a"
		if ApplyFinancialConfidenceGuard(rec, text) {
			t.Fatal("false positive")
		}
	})
	t.Run("030 explicitly without VAT", func(t *testing.T) {
		rec := guardFixture("270.00", "0", "270.00", defaultSources())
		if ApplyFinancialConfidenceGuard(rec, "270.00 \u0411\u0435\u0437 \u041d\u0414\u0421") {
			t.Fatal("false positive")
		}
	})
	t.Run("086 explicitly without VAT", func(t *testing.T) {
		rec := guardFixture("1.67", "0", "1.67", defaultSources())
		if ApplyFinancialConfidenceGuard(rec, "1.67 \u0411\u0435\u0437 \u041d\u0414\u0421") {
			t.Fatal("false positive")
		}
	})
	t.Run("003 zero inferred from without VAT", func(t *testing.T) {
		rec := guardFixture("70.00", "0", "70.00", defaultSources())
		if ApplyFinancialConfidenceGuard(rec, "70.00 \u0431\u0435\u0437 \u041d\u0414\u0421") {
			t.Fatal("false positive")
		}
	})
	t.Run("053 VAT written in words", func(t *testing.T) {
		rec := guardFixture("86.70", "15.19", "101.89", defaultSources())
		text := "101.89 \u0432 \u0442.\u0447. \u041d\u0414\u0421: \u041f\u044f\u0442\u043d\u0430\u0434\u0446\u0430\u0442\u044c \u0440\u0443\u0431\u043b\u0435\u0439 19 \u043a\u043e\u043f\u0435\u0435\u043a"
		if ApplyFinancialConfidenceGuard(rec, text) {
			t.Fatal("false positive")
		}
	})
	t.Run("061 VAT written in words", func(t *testing.T) {
		rec := guardFixture("36.75", "7.27", "44.02", defaultSources())
		text := "44.02 \u0432 \u0442.\u0447. \u041d\u0414\u0421: \u0421\u0435\u043c\u044c \u0440\u0443\u0431\u043b\u0435\u0439 27 \u043a\u043e\u043f\u0435\u0435\u043a"
		if ApplyFinancialConfidenceGuard(rec, text) {
			t.Fatal("false positive")
		}
	})
	t.Run("004 derived VAT plus table no VAT", func(t *testing.T) {
		sources := map[string]string{"amount_no_vat": "table", "vat_amount": "derived", "total": "model"}
		rec := guardFixture("1.02", "0.21", "1.23", sources)
		if ApplyFinancialConfidenceGuard(rec, "1.23 0.21 \u0412\u0441\u0435\u0433\u043e \u0441 \u041d\u0414\u0421") {
			t.Fatal("false positive")
		}
	})
	t.Run("007 derived VAT plus table no VAT", func(t *testing.T) {
		sources := map[string]string{"amount_no_vat": "table", "vat_amount": "derived", "total": "model"}
		rec := guardFixture("48.06", "9.61", "57.67", sources)
		if ApplyFinancialConfidenceGuard(rec, "57.67 \u0412\u0441\u0435\u0433\u043e") {
			t.Fatal("false positive")
		}
	})
	t.Run("012 corrected by preceding table reconciler", func(t *testing.T) {
		sources := map[string]string{"amount_no_vat": "derived", "vat_amount": "table", "total": "table"}
		rec := guardFixture("587.50", "117.50", "705.00", sources)
		if ApplyFinancialConfidenceGuard(rec, "587.50 117.50 705.00") {
			t.Fatal("false positive")
		}
	})
	t.Run("positive table VAT beats row-level without VAT", func(t *testing.T) {
		rec := guardFixture("100.00", "0", "100.00", defaultSources())
		rec.Table = &domain.Table{
			Columns: []domain.TableColumn{{Index: 1, Title: "\u0421\u0443\u043c\u043c\u0430 \u041d\u0414\u0421", Role: "vat"}},
			Rows:    []domain.TableRow{{Index: 1, Cells: []domain.TableCell{{Column: 1, Role: "vat", Value: "20.00"}}}},
		}
		if !ApplyFinancialConfidenceGuard(rec, "100.00 \u0411\u0435\u0437 \u041d\u0414\u0421 20.00") {
			t.Fatal("missed mixed-VAT risk")
		}
	})
	t.Run("manual never forced to review", func(t *testing.T) {
		sources := defaultSources()
		sources["total"] = "manual"
		rec := guardFixture("100.00", "0", "100.00", sources)
		if ApplyFinancialConfidenceGuard(rec, "100.00") {
			t.Fatal("human correction not respected")
		}
		if !rec.AutoPass {
			t.Fatal("altered auto pass")
		}
	})
	t.Run("without a derived source no self-confirmation rule", func(t *testing.T) {
		sources := map[string]string{"amount_no_vat": "model", "vat_amount": "model", "total": "model"}
		rec := guardFixture("100.00", "0", "100.00", sources)
		if ApplyFinancialConfidenceGuard(rec, "100.00") {
			t.Fatal("unexpected guard")
		}
	})
	t.Run("guard is idempotent and keeps existing faults", func(t *testing.T) {
		rec := guardFixture("24.34", "0", "24.34", defaultSources())
		rec.Faults = []string{"date"}
		rec.Review = []string{"date"}
		if !ApplyFinancialConfidenceGuard(rec, "24.34") || !ApplyFinancialConfidenceGuard(rec, "24.34") {
			t.Fatal("unexpected skip")
		}
		if len(rec.Faults) != 2 || len(rec.Review) != 4 {
			t.Fatalf("faults=%v review=%v", rec.Faults, rec.Review)
		}
		if !strings.Contains(rec.ReviewNotes["total"], "unsupported") {
			t.Fatal("missing diagnosis")
		}
	})
}
