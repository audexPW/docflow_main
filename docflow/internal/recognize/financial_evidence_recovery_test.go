package recognize

import (
	"docflow/internal/domain"
	"testing"
)

func recoveryTestTable(cols []domain.TableColumn, data [][]string) *domain.Table {
	table := &domain.Table{Columns: cols}
	for i, line := range data {
		row := domain.TableRow{Index: i + 1}
		for j, value := range line {
			row.Cells = append(row.Cells, domain.TableCell{Column: j + 1, Role: cols[j].Role, Value: value})
		}
		table.Rows = append(table.Rows, row)
	}
	return table
}

func recoveryTestFields(no, vat, total string) map[string]domain.Field {
	fields := map[string]domain.Field{}
	if no != "" {
		fields["amount_no_vat"] = domain.Field{Value: no, Source: "derived", Confidence: 0.90}
	}
	if vat != "" {
		fields["vat_amount"] = domain.Field{Value: vat, Source: "model", Confidence: 0.90}
	}
	if total != "" {
		fields["total"] = domain.Field{Value: total, Source: "model", Confidence: 0.90}
	}
	return fields
}

func recoveryCheck(t *testing.T, rec *domain.Recognition, ocr string, no, vat, total string) {
	t.Helper()
	if !RecoverFinancialTotalsFromEvidence(rec, ocr) {
		t.Fatal("expected recovery to change financial values")
	}
	for key, want := range map[string]string{"amount_no_vat": no, "vat_amount": vat, "total": total} {
		if got := rec.Fields[key].Value; got != want {
			t.Errorf("%s=%q want %q", key, got, want)
		}
	}
}

func TestFinancialEvidenceRecovery(t *testing.T) {
	t.Run("027 two verbal totals override misleading table total", func(t *testing.T) {
		rec := &domain.Recognition{Fields: recoveryTestFields("", "", "20.00")}
		rec.Fields["total"] = domain.Field{Value: "20.00", Source: "table", Confidence: 0.80}
		ocr := "40,72 8,15 48,87 " +
			"\u0421\u0443\u043c\u043c\u0430 \u041d\u0414\u0421: \u0412\u043e\u0441\u0435\u043c\u044c \u0431\u0435\u043b\u043e\u0440\u0443\u0441\u0441\u043a\u0438\u0445 \u0440\u0443\u0431\u043b\u0435\u0439 15 \u043a\u043e\u043f\u0435\u0435\u043a " +
			"\u0412\u0441\u0435\u0433\u043e: \u0421\u043e\u0440\u043e\u043a \u0432\u043e\u0441\u0435\u043c\u044c \u0431\u0435\u043b\u043e\u0440\u0443\u0441\u0441\u043a\u0438\u0445 \u0440\u0443\u0431\u043b\u0435\u0439 87 \u043a\u043e\u043f\u0435\u0435\u043a"
		recoveryCheck(t, rec, ocr, "40.72", "8.15", "48.87")
	})
	t.Run("032 single VAT table cell and OCR total", func(t *testing.T) {
		cols := []domain.TableColumn{{Index: 1, Title: "\u0410\u0440\u0435\u043d\u0434\u0430", Role: "name"}, {Index: 2, Title: "\u0426\u0435\u043d\u0430", Role: "price"}, {Index: 3, Title: recoveryVAT, Role: "vat"}}
		rec := &domain.Recognition{Fields: recoveryTestFields("", "18.252", "91.26"), Table: recoveryTestTable(cols, [][]string{{"\u0410\u0440\u0435\u043d\u0434\u043d\u0430\u044f \u043f\u043b\u0430\u0442\u0430", "76.05", "15.21"}})}
		recoveryCheck(t, rec, "76,05 15,21 \u0412\u0421\u0415\u0413\u041e: 91,26", "76.05", "15.21", "91.26")
	})
	t.Run("033 VAT and gross in labeled columns", func(t *testing.T) {
		cols := []domain.TableColumn{{Index: 1, Title: "\u041d\u0430\u0438\u043c\u0435\u043d\u043e\u0432\u0430\u043d\u0438\u0435", Role: "name"}, {Index: 2, Title: "\u0421\u0442\u043e\u0438\u043c\u043e\u0441\u0442\u044c", Role: "amount"}, {Index: 3, Title: "\u0421\u0443\u043c\u043c\u0430 " + recoveryVAT, Role: "vat"}, {Index: 4, Title: recoveryVsego + " \u0441 " + recoveryVAT, Role: "amount"}}
		rec := &domain.Recognition{Fields: recoveryTestFields("8.33", "0", "8.33"), Table: recoveryTestTable(cols, [][]string{{"\u0410\u0440\u0435\u043d\u0434\u0430", "8.33", "1.67", "10.00"}})}
		recoveryCheck(t, rec, "8,33 1,67 10,00 \u0418\u0442\u043e\u0433\u043e 8,33", "8.33", "1.67", "10.00")
	})
	t.Run("083 and 084 broken VAT role but full single row", func(t *testing.T) {
		cols := []domain.TableColumn{{Index: 1, Title: "\u041d\u0430\u0438\u043c\u0435\u043d\u043e\u0432\u0430\u043d\u0438\u0435", Role: "name"}, {Index: 2, Title: "\u0422\u0430\u0440\u0438\u0444", Role: "price"}, {Index: 3, Title: "\u0440\u0443\u0431", Role: "other"}, {Index: 4, Title: recoveryVsego + " \u0441 \u0443\u0447\u0435\u0442\u043e\u043c " + recoveryVAT, Role: "amount"}}
		for _, tc := range []struct{ no, vat, total string }{{"0.64", "0.13", "0.77"}, {"0.16", "0.03", "0.19"}} {
			rec := &domain.Recognition{Fields: recoveryTestFields("", "", tc.total), Table: recoveryTestTable(cols, [][]string{{"\u0412\u043e\u0437\u043c\u0435\u0449\u0435\u043d\u0438\u0435 \u0437\u0430\u0442\u0440\u0430\u0442", tc.no, tc.vat, tc.total}})}
			ocr := tc.no + " " + tc.vat + " " + tc.total + " \u0418\u0442\u043e\u0433\u043e \u041a \u043e\u043f\u043b\u0430\u0442\u0435 " + tc.total
			recoveryCheck(t, rec, ocr, tc.no, tc.vat, tc.total)
		}
	})
	t.Run("085 single item and corrupted footer", func(t *testing.T) {
		cols := []domain.TableColumn{{Index: 1, Title: "\u041d\u0430\u0438\u043c\u0435\u043d\u043e\u0432\u0430\u043d\u0438\u0435", Role: "name"}, {Index: 2, Title: "\u0426\u0435\u043d\u0430", Role: "price"}, {Index: 3, Title: recoveryVAT + ", \u0440\u0443\u0431", Role: "vat"}, {Index: 4, Title: "\u0441 " + recoveryVAT + ", \u0440\u0443\u0431", Role: "amount"}}
		rec := &domain.Recognition{Fields: recoveryTestFields("", "", "120.00"), Table: recoveryTestTable(cols, [][]string{{"\u041a\u043e\u043d\u0432\u0435\u0440\u0442 DL", "100.00", "20.00", "120.00"}, {"OTOTAL", "", "20.00", "120.00"}})}
		recoveryCheck(t, rec, "100.00 20.00 120.00 OTOTAL", "100.00", "20.00", "120.00")
	})
	t.Run("078 VAT annotation row", func(t *testing.T) {
		cols := []domain.TableColumn{{Index: 1, Title: "\u041d\u0430\u0438\u043c\u0435\u043d\u043e\u0432\u0430\u043d\u0438\u0435", Role: "name"}, {Index: 2, Title: "", Role: "other"}}
		rec := &domain.Recognition{Fields: recoveryTestFields("", "", "99.64"), Table: recoveryTestTable(cols, [][]string{{"\u0410\u0440\u0435\u043d\u0434\u043d\u0430\u044f \u043f\u043b\u0430\u0442\u0430", "199.64"}, {"\u0412 \u0442.\u0447. " + recoveryVAT + " 20%", "33.28"}})}
		recoveryCheck(t, rec, "199.64 \u0418\u0422\u041e\u0413\u041e 33.28 \u0412 \u0442.\u0447. \u041d\u0414\u0421 20%", "166.36", "33.28", "199.64")
	})
	t.Run("skip multiple unrelated lines", func(t *testing.T) {
		cols := []domain.TableColumn{{Index: 1, Title: "\u041d\u0430\u0438\u043c\u0435\u043d\u043e\u0432\u0430\u043d\u0438\u0435", Role: "name"}, {Index: 2, Title: recoveryVAT, Role: "vat"}, {Index: 3, Title: "\u0441 " + recoveryVAT, Role: "amount"}}
		rec := &domain.Recognition{Fields: recoveryTestFields("", "", "30.27"), Table: recoveryTestTable(cols, [][]string{{"\u0423\u0441\u043b\u0443\u0433\u0430 \u043f\u0435\u0440\u0432\u0430\u044f", "0.57", "3.43"}, {"\u0423\u0441\u043b\u0443\u0433\u0430 \u0432\u0442\u043e\u0440\u0430\u044f", "0.07", "0.40"}})}
		if RecoverFinancialTotalsFromEvidence(rec, "0.57 3.43 0.07 0.40") {
			t.Fatal("must not replace document-level total from multiposition rows")
		}
	})
	t.Run("manual must be preserved", func(t *testing.T) {
		rec := &domain.Recognition{Fields: recoveryTestFields("", "", "20.00")}
		rec.Fields["total"] = domain.Field{Source: "manual", Value: "20.00", Confidence: 1}
		ocr := "\u0421\u0443\u043c\u043c\u0430 \u041d\u0414\u0421: \u041e\u0434\u0438\u043d \u0440\u0443\u0431\u043b\u044c 67 \u043a\u043e\u043f\u0435\u0435\u043a. \u0412\u0441\u0435\u0433\u043e: \u0414\u0435\u0441\u044f\u0442\u044c \u0440\u0443\u0431\u043b\u0435\u0439 00 \u043a\u043e\u043f\u0435\u0435\u043a."
		if RecoverFinancialTotalsFromEvidence(rec, ocr) {
			t.Fatal("manual value must block recovery")
		}
		if rec.Fields["total"].Value != "20.00" {
			t.Fatal("manual field changed")
		}
	})
}
