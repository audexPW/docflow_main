package recognize

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"docflow/internal/domain"
)

// A conservative override only when one monetary table line is supported by
// the complete table structure and by amounts printed in the OCR text.
var (
	groundedTableMoneyRE   = regexp.MustCompile(`^[+-]?[0-9]+(?:[.,][0-9]{1,2})?$`)
	groundedTableNameRE    = regexp.MustCompile(`\p{L}{5,}`)
	groundedTableSummaryRE = regexp.MustCompile(`(?i)итог|всего|сумма\s+ндс`)
)

type groundedCell struct{ role, title, value string }

func groundedMoneyCents(s string) (int64, bool) {
	s = strings.ReplaceAll(strings.TrimSpace(s), " ", "")
	s = strings.ReplaceAll(s, "\u00a0", "")
	s = strings.ReplaceAll(s, ",", ".")
	if !groundedTableMoneyRE.MatchString(s) {
		return 0, false
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || math.Abs(n) > 1e12 {
		return 0, false
	}
	return int64(math.Round(n * 100)), true
}

func groundedFormatCents(n int64) string { return fmt.Sprintf("%d.%02d", n/100, n%100) }

func groundedMoneyInOCR(text string, cents int64) bool {
	if cents < 0 {
		return false
	}
	amount := groundedFormatCents(cents)
	for _, needle := range []string{amount, strings.ReplaceAll(amount, ".", ",")} {
		for from := 0; from < len(text); {
			pos := strings.Index(text[from:], needle)
			if pos < 0 {
				break
			}
			pos += from
			end := pos + len(needle)
			leftDigit := pos > 0 && text[pos-1] >= '0' && text[pos-1] <= '9'
			rightDigit := end < len(text) && text[end] >= '0' && text[end] <= '9'
			if !leftDigit && !rightDigit {
				return true
			}
			from = pos + 1
		}
	}
	return false
}

func groundedTableRows(t *domain.Table) [][]groundedCell {
	cols := make(map[int]domain.TableColumn, len(t.Columns))
	for _, c := range t.Columns {
		cols[c.Index] = c
	}
	rows := make([][]groundedCell, 0, len(t.Rows))
	for _, row := range t.Rows {
		cells := make([]groundedCell, 0, len(row.Cells))
		for _, cell := range row.Cells {
			col := cols[cell.Column]
			role := cell.Role
			if role == "" {
				role = col.Role
			}
			cells = append(cells, groundedCell{role: role, title: strings.ToLower(col.Title), value: strings.TrimSpace(cell.Value)})
		}
		rows = append(rows, cells)
	}
	return rows
}

func groundedRealItem(cells []groundedCell) bool {
	for _, cell := range cells {
		if (cell.role == "name" || cell.role == "unit") && groundedTableNameRE.MatchString(cell.value) {
			return true
		}
	}
	return false
}

func groundedSummaryRow(cells []groundedCell, total, vat int64) bool {
	var label strings.Builder
	for _, cell := range cells {
		if cell.role == "name" || cell.role == "unit" || cell.role == "other" {
			label.WriteString(cell.value)
			label.WriteByte(' ')
		}
	}
	if !groundedTableSummaryRE.MatchString(label.String()) {
		return false
	}
	for _, cell := range cells {
		n, ok := groundedMoneyCents(cell.value)
		if !ok {
			continue
		}
		if cell.role == "qty" || cell.role == "price" {
			return false
		}
		if cell.role == "amount" || cell.role == "vat" || cell.role == "other" {
			if n != total && n != vat && n != 0 {
				return false
			}
		}
	}
	return true
}

// ReconcileGroundedSingleLineAmounts uses a table line only when every other
// detected row is a summary row and all three values are present in OCR.
// It never overrides conflicting manual, rule or previously tabular values.
func ReconcileGroundedSingleLineAmounts(rec *domain.Recognition, ocrText string) bool {
	if rec == nil || rec.Table == nil || len(rec.Table.Rows) == 0 || len(rec.Table.Columns) == 0 || ocrText == "" {
		return false
	}
	rows := groundedTableRows(rec.Table)
	var candidate map[string]int64
	candidateCount := 0
	for i, row := range rows {
		if !groundedRealItem(row) {
			continue
		}
		var amounts, vats []int64
		for _, cell := range row {
			n, ok := groundedMoneyCents(cell.value)
			if !ok {
				continue
			}
			switch cell.role {
			case "amount":
				if !strings.Contains(cell.title, "без ндс") && !strings.Contains(cell.title, "б/ндс") {
					amounts = append(amounts, n)
				}
			case "vat":
				if strings.Contains(cell.title, "ндс") && !strings.Contains(cell.title, "%") && !strings.Contains(cell.title, "ставк") {
					vats = append(vats, n)
				}
			}
		}
		if len(amounts) != 1 || len(vats) != 1 {
			continue
		}
		total, vat := amounts[0], vats[0]
		if total <= 0 || vat < 0 || vat > total {
			continue
		}
		noVAT := total - vat
		if !groundedMoneyInOCR(ocrText, total) || !groundedMoneyInOCR(ocrText, vat) || !groundedMoneyInOCR(ocrText, noVAT) {
			continue
		}
		complete := true
		for j, other := range rows {
			if j != i && !groundedSummaryRow(other, total, vat) {
				complete = false
				break
			}
		}
		if !complete {
			continue
		}
		candidate = map[string]int64{"total": total, "vat_amount": vat, "amount_no_vat": noVAT}
		candidateCount++
	}
	if candidateCount != 1 {
		return false
	}
	// Before changing anything, reject a conflicting field with protected provenance.
	for key, next := range candidate {
		f, exists := rec.Fields[key]
		if !exists || f.Source == "" || f.Source == "model" || f.Source == "derived" {
			continue
		}
		previous, valid := groundedMoneyCents(f.Value)
		if !valid || previous != next {
			return false
		}
	}
	if rec.Fields == nil {
		rec.Fields = make(map[string]domain.Field)
	}
	changed := false
	for _, key := range []string{"total", "vat_amount", "amount_no_vat"} {
		next := candidate[key]
		prev := rec.Fields[key]
		if v, ok := groundedMoneyCents(prev.Value); ok && v == next {
			continue
		}
		source := "table"
		if key == "amount_no_vat" {
			source = "derived"
		}
		rec.Fields[key] = domain.Field{Value: groundedFormatCents(next), Confidence: 0.8, Source: source}
		changed = true
	}
	return changed
}
