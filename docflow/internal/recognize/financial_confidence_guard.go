package recognize

import (
	"fmt"
	"sort"
	"strings"

	"docflow/internal/domain"
)

// financialConfidenceGuardCap is deliberately below the default auto-pass
// confidence threshold. The review/fault markers below enforce the hold
// regardless of configuration of that threshold.
const financialConfidenceGuardCap = 0.69

var financialGuardKeys = []string{"amount_no_vat", "vat_amount", "total"}

// financialPositiveVATInTable detects a contradiction to an inferred zero VAT.
// A positive VAT cell in a labelled VAT column is evidence that the document
// may have taxable positions; it is not necessarily an independently verified
// document-level VAT total.
func financialPositiveVATInTable(rec *domain.Recognition) bool {
	if rec.Table == nil {
		return false
	}
	cols := make(map[int]domain.TableColumn, len(rec.Table.Columns))
	for _, col := range rec.Table.Columns {
		cols[col.Index] = col
	}
	for _, row := range rec.Table.Rows {
		for _, cell := range row.Cells {
			col := cols[cell.Column]
			role := cell.Role
			if role == "" {
				role = col.Role
			}
			if role != "vat" || !strings.Contains(strings.ToLower(col.Title), "\u043d\u0434\u0441") {
				continue
			}
			if cents, ok := groundedMoneyCents(cell.Value); ok && cents > 0 {
				return true
			}
		}
	}
	return false
}

// financialExplicitNoVAT is intentionally conservative. A lone numerical
// zero anywhere in OCR does NOT confirm the document's overall VAT is zero.
func financialExplicitNoVAT(rec *domain.Recognition, ocrText string) bool {
	if financialPositiveVATInTable(rec) {
		return false
	}
	if spoken, ok := groundedMoneyCents(VATFromWords(ocrText)); ok {
		return spoken == 0
	}
	// A document-level 'without VAT' clause is useful evidence, but this
	// heuristic must remain opt-in: mixed-VAT documents may contain this text
	// on individual rows. A positive VAT cell above blocks that exception.
	normalized := strings.ToLower(strings.Join(strings.Fields(ocrText), " "))
	return strings.Contains(normalized, "\u0431\u0435\u0437 \u043d\u0434\u0441")
}

// financialTotalInWords looks only for words following an explicit total label.
// The ordinary ParseMoneyWords is reused, not a separate money-language model.
func financialTotalInWords(ocrText string, cents int64) bool {
	lower := strings.ToLower(ocrText)
	labels := []string{
		"\u0432\u0441\u0435\u0433\u043e:",
		"\u0432\u0441\u0435\u0433\u043e \u043d\u0430 \u0441\u0443\u043c\u043c\u0443:",
		"\u043a \u043e\u043f\u043b\u0430\u0442\u0435:",
		"\u0438\u0442\u043e\u0433\u043e:",
	}
	for _, label := range labels {
		from := 0
		for from < len(lower) {
			at := strings.Index(lower[from:], label)
			if at < 0 {
				break
			}
			start := from + at + len(label)
			end := start + 180
			if end > len(ocrText) {
				end = len(ocrText)
			}
			if found, ok := groundedMoneyCents(ParseMoneyWords(ocrText[start:end])); ok && found == cents {
				return true
			}
			from = start
		}
	}
	return false
}

// financialFieldHasIndependentEvidence tests a printed monetary value or an
// explicitly verbalized amount. Table provenance is accepted for this guard;
// table correctness remains the responsibility of the separate table validator.
func financialFieldHasIndependentEvidence(rec *domain.Recognition, key string, fld domain.Field, ocrText string) bool {
	if fld.Source == "manual" || fld.Source == "table" {
		return true
	}
	cents, ok := groundedMoneyCents(fld.Value)
	if !ok || cents < 0 {
		return false
	}
	if key == "vat_amount" {
		if cents == 0 {
			return financialExplicitNoVAT(rec, ocrText)
		}
		// An explicitly written VAT amount is stronger than an incidental
		// numeric match elsewhere on the page.
		if spoken, ok := groundedMoneyCents(VATFromWords(ocrText)); ok {
			return spoken == cents
		}
	}
	if groundedMoneyInOCR(ocrText, cents) {
		return true
	}
	if key == "total" {
		return financialTotalInWords(ocrText, cents)
	}
	return false
}

func financialUniqueAppend(in []string, value string) []string {
	for _, existing := range in {
		if existing == value {
			return in
		}
	}
	return append(in, value)
}

// ApplyFinancialConfidenceGuard prevents arithmetic self-confirmation from
// certifying two unverified source amounts. It is invoked ONLY under the
// FINANCIAL_CONFIDENCE_GUARD_ENABLED flag, after DecideAutoPass.
//
// It NEVER alters extracted values. If independent evidence is insufficient,
// it lowers confidence, adds a recognized 'total' fault, and prevents export.
// This is a safety gate rather than a new accuracy scoring formula.
func ApplyFinancialConfidenceGuard(rec *domain.Recognition, ocrText string) bool {
	if rec == nil || len(rec.Fields) == 0 || strings.TrimSpace(ocrText) == "" {
		return false
	}
	if !amountsConfirmed(rec) {
		return false
	}
	derivedCount := 0
	for _, key := range financialGuardKeys {
		fld := rec.Fields[key]
		if fld.Source == "manual" {
			return false // existing human verification wins
		}
		if fld.Source == "derived" {
			derivedCount++
		}
	}
	if derivedCount == 0 {
		return false
	}

	independent := 0
	var unsupported []string
	for _, key := range financialGuardKeys {
		fld := rec.Fields[key]
		if fld.Source == "derived" {
			continue
		}
		independent++
		if !financialFieldHasIndependentEvidence(rec, key, fld, ocrText) {
			unsupported = append(unsupported, key)
		}
	}
	if independent >= 2 && len(unsupported) == 0 {
		return false
	}

	for _, key := range financialGuardKeys {
		fld := rec.Fields[key]
		if fld.Confidence > financialConfidenceGuardCap {
			fld.Confidence = financialConfidenceGuardCap
			rec.Fields[key] = fld
		}
		rec.Review = financialUniqueAppend(rec.Review, key)
	}
	sort.Strings(rec.Review)
	rec.Faults = financialUniqueAppend(rec.Faults, "total")
	sort.Strings(rec.Faults)
	rec.AutoPass = false
	if rec.ReviewNotes == nil {
		rec.ReviewNotes = make(map[string]string)
	}
	if rec.ReviewNotes["total"] == "" {
		rec.ReviewNotes["total"] = fmt.Sprintf("financial source evidence incomplete: unsupported=%v; arithmetic alone cannot confirm values", unsupported)
	}
	return true
}
