package recognize

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"docflow/internal/domain"
)

// Financial evidence recovery is intentionally separate from confidence scoring.
// It only changes amounts when a document-level total AND a nonzero VAT amount
// are independently identifiable in labeled OCR text or a well-constrained table.
// Arithmetic alone never selects document totals.
const (
	recoveryVAT    = "\u043d\u0434\u0441"
	recoveryItogo  = "\u0438\u0442\u043e\u0433\u043e"
	recoveryVsego  = "\u0432\u0441\u0435\u0433\u043e"
	recoveryOplate = "\u043a \u043e\u043f\u043b\u0430\u0442\u0435"
)

var (
	recoveryTotalDigitRE  = regexp.MustCompile(`(?i)(?:` + recoveryItogo + `|` + recoveryVsego + `|` + recoveryOplate + `)\s*[:=]?\s*([0-9]{1,9}[.,][0-9]{1,2})(?:\D|$)`)
	recoveryTotalsLabelRE = regexp.MustCompile(`(?i)(?:` + recoveryItogo + `|` + recoveryVsego + `)(?:\s+(?:на\s+сумму|к\s+оплате|по\s+документу))?\s*:\s*|` + recoveryOplate + `\s*:\s*`)
	recoveryItogoRE       = regexp.MustCompile(`(?i)` + recoveryItogo + `|` + recoveryVsego + `|` + recoveryOplate)
	// A row is a summary only if its label cell names an aggregate.
	recoverySummaryLabelRE = regexp.MustCompile(`(?i)^\s*(?:итого|всего)(?:\s+(?:к\s+оплате|с\s+ндс|без\s+ндс))?\s*[:;.,-]?\s*$|^\s*(?:к\s+оплате|сумма\s+ндс|o?total)\s*[:;.,-]?\s*$`)
	// Standalone VAT annotation caption, optionally with a tax rate.
	recoveryVATAnnotationLabelRE = regexp.MustCompile(`(?i)^\s*(?:в\s*т\.?\s*ч\.?|в\s+том\s+числе)\s*ндс(?:\s+\d+(?:[.,]\d+)?\s*%)?\s*[:;.,-]?\s*$`)
)

type recoveryPair struct {
	total     int64
	vat       int64
	kind      string
	fromWords bool
}

// recoveryWordEvidence is a source-local, labeled amount and its OCR span.
// Positions are byte offsets in the SAME OCR string, not document-wide values.
type recoveryWordEvidence struct {
	start int
	end   int
	cents int64
}

// A two-amount footer may contain punctuation or whitespace between labeled
// amounts. It must NOT contain a page break, product title, section heading,
// other amount, or arbitrary text; otherwise amounts could originate in
// unrelated parts of a multi-section document.
func recoveryEvidenceGap(gap string) bool {
	if len(gap) > 64 {
		return false
	}
	for _, r := range gap {
		if r == '\f' || unicode.IsLetter(r) || unicode.IsDigit(r) || r == '<' || r == '>' {
			return false
		}
	}
	return true
}

// Find every LABELED written-out total. The monetary expression must follow
// its label directly (apart from punctuation and spacing).
func recoveryWordTotalSpans(text string) []recoveryWordEvidence {
	var evidence []recoveryWordEvidence
	for _, label := range recoveryTotalsLabelRE.FindAllStringIndex(text, -1) {
		to := label[1] + 155
		if to > len(text) {
			to = len(text)
		}
		after := text[label[1]:to]
		money := reMoneyInWords.FindStringIndex(after)
		if money == nil || !recoveryEvidenceGap(after[:money[0]]) {
			continue
		}
		if cents, ok := groundedMoneyCents(ParseMoneyWords(after[money[0]:money[1]])); ok && cents > 0 {
			evidence = append(evidence, recoveryWordEvidence{
				start: label[0], end: label[1] + money[1], cents: cents,
			})
		}
	}
	return evidence
}

func recoveryWordVATSpans(text string) []recoveryWordEvidence {
	var evidence []recoveryWordEvidence
	for _, m := range reVATWords.FindAllStringSubmatchIndex(text, -1) {
		if len(m) < 4 || m[2] < 0 || m[3] < 0 {
			continue
		}
		if cents, ok := groundedMoneyCents(ParseMoneyWords(text[m[2]:m[3]])); ok && cents > 0 {
			evidence = append(evidence, recoveryWordEvidence{
				start: m[0], end: m[1], cents: cents,
			})
		}
	}
	return evidence
}

func recoveryWordSpansAdjacent(text string, a, b recoveryWordEvidence) bool {
	if a.end <= b.start {
		return recoveryEvidenceGap(text[a.end:b.start])
	}
	if b.end <= a.start {
		return recoveryEvidenceGap(text[b.end:a.start])
	}
	// Overlapping matches are not independent evidence.
	return false
}

func recoveryDigitTotal(text string) (int64, bool) {
	vals := map[int64]bool{}
	for _, m := range recoveryTotalDigitRE.FindAllStringSubmatch(text, -1) {
		if val, ok := groundedMoneyCents(m[1]); ok && val > 0 {
			vals[val] = true
		}
	}
	if len(vals) != 1 {
		return 0, false
	}
	for val := range vals {
		return val, true
	}
	return 0, false
}

func recoveryTableTotalTitle(s string) bool {
	return strings.Contains(s, "\u0441 "+recoveryVAT) ||
		strings.Contains(s, "\u0441 \u0443\u0447\u0435\u0442\u043e\u043c "+recoveryVAT) ||
		strings.Contains(s, "\u0441 \u0443\u0447\u0451\u0442\u043e\u043c "+recoveryVAT)
}

func recoveryTableVATTitle(s string) bool {
	return strings.Contains(s, recoveryVAT) &&
		!strings.Contains(s, "%") &&
		!strings.Contains(s, "\u0441\u0442\u0430\u0432\u043a") &&
		!recoveryTableTotalTitle(s)
}

// A summary is an explicitly labeled row, not a row containing a VAT-related
// word in its product description. Non-summary text in another textual cell
// makes the row ambiguous; fail closed rather than drop a potentially real item.
func recoveryRowIsSummary(cells []groundedCell) bool {
	found := false
	for _, cell := range cells {
		if cell.role != "name" && cell.role != "other" && cell.role != "unit" && cell.role != "" {
			continue
		}
		v := strings.TrimSpace(cell.value)
		if v == "" || v == "x" || v == "X" || v == "-" {
			continue
		}
		if _, numeric := groundedMoneyCents(v); numeric {
			continue
		}
		if recoverySummaryLabelRE.MatchString(v) || recoveryVATAnnotationLabelRE.MatchString(v) {
			found = true
			continue
		}
		// Any meaningful text not identified as a caption is a possible
		// real line. A conservative recovery must keep it in the row count.
		if groundedTableNameRE.MatchString(v) {
			return false
		}
	}
	return found
}

// A row with an unreadable item name but surviving financial numbers still
// counts against the single-item assumption. Ignoring such rows would turn an
// incomplete multiposition table into a seemingly complete single-line table.
func recoveryRowHasFinancialNumber(cells []groundedCell) bool {
	for _, cell := range cells {
		switch cell.role {
		case "amount", "vat", "price", "other", "":
			n, ok := groundedMoneyCents(cell.value)
			if ok && n > 0 {
				return true
			}
		}
	}
	return false
}

func recoveryTableInfo(t *domain.Table) ([][]groundedCell, int) {
	rows := groundedTableRows(t)
	items := 0
	for _, row := range rows {
		if recoveryRowIsSummary(row) {
			continue
		}
		if groundedRealItem(row) || recoveryRowHasFinancialNumber(row) {
			items++
		}
	}
	return rows, items
}

// A separate VAT annotation line is not a purchasable item. The caption must
// identify the entire label cell; searching within arbitrary product names
// would confuse VAT-related services with tax subtotals.
func recoveryRowIsVATAnnotation(cells []groundedCell) bool {
	for _, cell := range cells {
		if cell.role == "name" || cell.role == "other" || cell.role == "unit" || cell.role == "" {
			if recoveryVATAnnotationLabelRE.MatchString(strings.TrimSpace(cell.value)) {
				return true
			}
		}
	}
	return false
}

// Price is a unit price. It may stand for a row base only if its quantity is
// absent or exactly one. Decimal quantities such as 0.5 must not pass.
func recoveryUnitQuantity(s string) bool {
	s = strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(s), "\u00a0", ""), " ", "")
	s = strings.ReplaceAll(s, ",", ".")
	if s == "" {
		return true
	}
	v, err := strconv.ParseFloat(s, 64)
	return err == nil && v == 1
}

// Never combine the first written-out VAT anywhere in OCR with a total
// elsewhere. Accept only a unique adjacent pair of explicitly labeled
// amounts, with no prose or page boundary separating them. Ambiguity => abstain.
func recoveryFromOCRWords(text string) (recoveryPair, bool) {
	totals := recoveryWordTotalSpans(text)
	vats := recoveryWordVATSpans(text)
	pairs := map[[2]int64]bool{}
	for _, total := range totals {
		for _, vat := range vats {
			if total.cents <= vat.cents || vat.cents <= 0 {
				continue
			}
			if recoveryWordSpansAdjacent(text, total, vat) {
				pairs[[2]int64{total.cents, vat.cents}] = true
			}
		}
	}
	if len(pairs) != 1 {
		return recoveryPair{}, false
	}
	for p := range pairs {
		return recoveryPair{total: p[0], vat: p[1], kind: "rule", fromWords: true}, true
	}
	return recoveryPair{}, false
}

// recoveryFromTableCorroboratedWordTotals can bridge OCR layout damage WITHOUT
// treating long-distance written amounts as a pair on their own. Each amount
// must have its own explicit label AND a complete matching monetary identity
// must be visible in one coherent table region. Conflicts => abstain.
// This supplements (never weakens) the strict local written-amount rule.
func recoveryFromTableCorroboratedWordTotals(t *domain.Table, ocr string) (recoveryPair, bool) {
	if t == nil || strings.TrimSpace(ocr) == "" {
		return recoveryPair{}, false
	}
	writtenTotal := recoveryWordTotalSpans(ocr)
	writtenVAT := recoveryWordVATSpans(ocr)
	// More than one separately labeled total or VAT can denote another act,
	// appendix, subtotal or page. We cannot choose safely from those.
	if len(writtenTotal) != 1 || len(writtenVAT) != 1 {
		return recoveryPair{}, false
	}
	total := writtenTotal[0].cents
	vat := writtenVAT[0].cents
	if total <= vat || vat <= 0 {
		return recoveryPair{}, false
	}
	// A physically separate page is a firm boundary even if the same numbers
	// are found in a damaged table. Page-aware segmentation belongs upstream.
	first, second := writtenTotal[0], writtenVAT[0]
	if first.start > second.start {
		first, second = second, first
	}
	if first.end > second.start || strings.Contains(ocr[first.end:second.start], "\f") {
		return recoveryPair{}, false
	}
	base := total - vat
	for _, money := range []int64{base, vat, total} {
		if !groundedMoneyInOCR(ocr, money) {
			return recoveryPair{}, false
		}
	}

	// Strong table pattern A: an explicit aggregate row contains VAT+gross;
	// the corresponding base is also present somewhere in the table.
	tableHasBase := false
	for _, row := range t.Rows {
		for _, cell := range row.Cells {
			n, ok := groundedMoneyCents(cell.Value)
			if ok && n == base {
				tableHasBase = true
			}
		}
	}
	aggregatePair := false
	if tableHasBase && len(t.TotalsRow) > 0 {
		label, hasVAT, hasGross, conflictingAmount := false, false, false, false
		for _, cell := range t.TotalsRow {
			value := strings.TrimSpace(cell.Value)
			if recoverySummaryLabelRE.MatchString(value) {
				label = true
				continue
			}
			n, ok := groundedMoneyCents(value)
			if !ok {
				continue
			}
			switch n {
			case vat:
				hasVAT = true
			case total:
				hasGross = true
			case base, 0:
			default:
				conflictingAmount = true
			}
		}
		aggregatePair = label && hasVAT && hasGross && !conflictingAmount
	}

	// Strong table pattern B: a neighboring row shows both VAT and gross,
	// while another row explicitly proves the base as quantity*unit-price.
	// This handles OCR tables where one physical item is broken over rows.
	// It must NOT trigger on an arbitrary arithmetic trio of loose numbers.
	rows := groundedTableRows(t)
	pairRows := make([]int, 0, 2)
	for i, row := range rows {
		hasVAT, hasGross := false, false
		for _, cell := range row {
			if cell.role == "qty" || cell.role == "price" || cell.role == "vat_rate" || cell.role == "index" {
				continue
			}
			n, ok := groundedMoneyCents(cell.value)
			if !ok {
				continue
			}
			if n == vat {
				hasVAT = true
			}
			if n == total {
				hasGross = true
			}
		}
		if hasVAT && hasGross {
			pairRows = append(pairRows, i)
		}
	}
	qtyPriceRows := make([]int, 0, 2)
	completeItemRows := 0
	for i, row := range rows {
		var qty float64
		qtyOK := false
		var price int64
		priceOK := false
		baseInAmountCell := false
		rowHasComputedAmount := false
		var monetaryCells []int64
		for _, cell := range row {
			if cell.role == "qty" {
				val := strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(cell.value), " ", ""), ",", ".")
				if v, err := strconv.ParseFloat(val, 64); err == nil && !math.IsNaN(v) && !math.IsInf(v, 0) && v > 0 && v <= 1e6 {
					qty, qtyOK = v, true
				}
			} else if cell.role == "price" {
				price, priceOK = groundedMoneyCents(cell.value)
			} else if cell.role == "amount" || cell.role == "other" || cell.role == "vat" || cell.role == "" {
				if n, ok := groundedMoneyCents(cell.value); ok {
					monetaryCells = append(monetaryCells, n)
					if n == base {
						baseInAmountCell = true
					}
				}
			}
		}
		if qtyOK && priceOK {
			for _, n := range monetaryCells {
				if math.Abs(qty*float64(price)-float64(n)) < 0.00001 {
					rowHasComputedAmount = true
				}
			}
			if rowHasComputedAmount {
				completeItemRows++
			}
		}
		if qtyOK && priceOK && baseInAmountCell && math.Abs(qty*float64(price)-float64(base)) < 0.00001 {
			qtyPriceRows = append(qtyPriceRows, i)
		}
	}
	calculatedPair := false
	// One (and only one) qualifying monetary item; otherwise an item subtotal
	// could be mistaken for the full document total.
	if completeItemRows == 1 && len(qtyPriceRows) == 1 && len(pairRows) == 1 {
		distance := qtyPriceRows[0] - pairRows[0]
		if distance < 0 {
			distance = -distance
		}
		calculatedPair = distance <= 1
	}
	if completeItemRows > 1 || (!aggregatePair && !calculatedPair) {
		return recoveryPair{}, false
	}
	return recoveryPair{total: total, vat: vat, kind: "table", fromWords: true}, true
}

func recoveryFromLabeledTable(t *domain.Table, ocr string) (recoveryPair, bool) {
	if t == nil {
		return recoveryPair{}, false
	}
	rows, items := recoveryTableInfo(t)
	if items != 1 {
		return recoveryPair{}, false
	}
	pairs := map[[2]int64]bool{}
	for _, row := range rows {
		if recoveryRowIsSummary(row) || !groundedRealItem(row) {
			continue
		}
		var vats, totals []int64
		for _, cell := range row {
			n, ok := groundedMoneyCents(cell.value)
			if !ok {
				continue
			}
			if cell.role == "vat" && recoveryTableVATTitle(cell.title) {
				vats = append(vats, n)
			}
			if cell.role == "amount" && recoveryTableTotalTitle(cell.title) {
				totals = append(totals, n)
			}
		}
		for _, vat := range vats {
			for _, total := range totals {
				if vat > 0 && total > vat && groundedMoneyInOCR(ocr, vat) && groundedMoneyInOCR(ocr, total) && groundedMoneyInOCR(ocr, total-vat) {
					pairs[[2]int64{total, vat}] = true
				}
			}
		}
	}
	if len(pairs) != 1 {
		return recoveryPair{}, false
	}
	for p := range pairs {
		return recoveryPair{total: p[0], vat: p[1], kind: "table"}, true
	}
	return recoveryPair{}, false
}

// OCR footer with a numeric total, a single table line containing a labeled
// VAT cell, and an independently printed base amount.
func recoveryFromVATRowAndOCRTotal(t *domain.Table, ocr string) (recoveryPair, bool) {
	if t == nil {
		return recoveryPair{}, false
	}
	total, tok := recoveryDigitTotal(ocr)
	if !tok {
		return recoveryPair{}, false
	}
	rows, items := recoveryTableInfo(t)
	if items != 1 {
		return recoveryPair{}, false
	}
	vals := map[int64]bool{}
	for _, row := range rows {
		if recoveryRowIsSummary(row) || !groundedRealItem(row) {
			continue
		}
		var vatVals []int64
		candidates := map[int64]bool{}
		for _, c := range row {
			n, ok := groundedMoneyCents(c.value)
			if !ok {
				continue
			}
			if c.role == "vat" && recoveryTableVATTitle(c.title) {
				vatVals = append(vatVals, n)
			}
			if c.role == "amount" || c.role == "price" {
				candidates[n] = true
			}
		}
		for _, vat := range vatVals {
			if vat > 0 && total > vat && groundedMoneyInOCR(ocr, vat) && groundedMoneyInOCR(ocr, total-vat) && candidates[total-vat] {
				vals[vat] = true
			}
		}
	}
	if len(vals) != 1 {
		return recoveryPair{}, false
	}
	for vat := range vals {
		return recoveryPair{total: total, vat: vat, kind: "table"}, true
	}
	return recoveryPair{}, false
}

// Single table row with a labeled VAT-inclusive total, a unique tariff/base
// amount and one otherwise unclassified amount that closes the arithmetic.
// Used only when an independently reported final total already agrees.
func recoveryFromSingleUnclassifiedVAT(t *domain.Table, rec *domain.Recognition, ocr string) (recoveryPair, bool) {
	if t == nil {
		return recoveryPair{}, false
	}
	rows, items := recoveryTableInfo(t)
	if items != 1 {
		return recoveryPair{}, false
	}
	current, known := groundedMoneyCents(rec.Fields["total"].Value)
	if !known || current <= 0 {
		return recoveryPair{}, false
	}
	pairs := map[[2]int64]bool{}
	for _, row := range rows {
		if !groundedRealItem(row) || recoveryRowIsSummary(row) {
			continue
		}
		var totals, bases, others []int64
		qtyBad := false
		for _, c := range row {
			if c.role == "qty" && !recoveryUnitQuantity(c.value) {
				qtyBad = true
			}
			n, ok := groundedMoneyCents(c.value)
			if !ok {
				continue
			}
			switch c.role {
			case "qty":
				// Quantity is not a monetary input.
			case "amount":
				if recoveryTableTotalTitle(c.title) {
					totals = append(totals, n)
				}
			case "price":
				bases = append(bases, n)
			case "other":
				others = append(others, n)
			}
		}
		if qtyBad {
			continue
		}
		for _, total := range totals {
			for _, base := range bases {
				for _, vat := range others {
					if total != current || base <= 0 || vat <= 0 || base+vat != total {
						continue
					}
					if !groundedMoneyInOCR(ocr, base) || !groundedMoneyInOCR(ocr, vat) || !groundedMoneyInOCR(ocr, total) {
						continue
					}
					pairs[[2]int64{total, vat}] = true
				}
			}
		}
	}
	if len(pairs) != 1 {
		return recoveryPair{}, false
	}
	for p := range pairs {
		return recoveryPair{total: p[0], vat: p[1], kind: "table"}, true
	}
	return recoveryPair{}, false
}

// A printed "incl. VAT" annotation row provides VAT while the neighboring
// item row provides the total. Both values must be found near an OCR total label.
func recoveryFromVATAnnotation(t *domain.Table, ocr string) (recoveryPair, bool) {
	if t == nil || len(t.Rows) != 2 {
		return recoveryPair{}, false
	}
	rows := groundedTableRows(t)
	var vats, totals []int64
	for _, r := range rows {
		if recoveryRowIsVATAnnotation(r) {
			for _, c := range r {
				// A real item with quantity/price is not a tax annotation.
				if (c.role == "qty" || c.role == "price") && strings.TrimSpace(c.value) != "" {
					return recoveryPair{}, false
				}
				if c.role == "other" || c.role == "amount" || c.role == "" {
					if n, ok := groundedMoneyCents(c.value); ok && n > 0 {
						vats = append(vats, n)
					}
				}
			}
		} else {
			if !groundedRealItem(r) {
				continue
			}
			for _, c := range r {
				if c.role == "other" || c.role == "amount" || c.role == "" {
					if n, ok := groundedMoneyCents(c.value); ok && n > 0 {
						totals = append(totals, n)
					}
				}
			}
		}
	}
	if len(vats) != 1 || len(totals) != 1 || totals[0] <= vats[0] {
		return recoveryPair{}, false
	}
	if !groundedMoneyInOCR(ocr, vats[0]) || !groundedMoneyInOCR(ocr, totals[0]) || !recoveryTotalNearLabel(ocr, totals[0]) {
		return recoveryPair{}, false
	}
	return recoveryPair{total: totals[0], vat: vats[0], kind: "rule"}, true
}

func recoveryTotalNearLabel(ocr string, total int64) bool {
	for _, m := range recoveryItogoRE.FindAllStringIndex(ocr, -1) {
		from := m[0] - 90
		if from < 0 {
			from = 0
		}
		to := m[1] + 90
		if to > len(ocr) {
			to = len(ocr)
		}
		if groundedMoneyInOCR(ocr[from:to], total) {
			return true
		}
	}
	return false
}

// RecoverFinancialTotalsFromEvidence is additive and opt-in. It does not
// modify recognized money on manual input, and it rejects conflicting
// protected sources unless TWO amounts were separately written in words.
func RecoverFinancialTotalsFromEvidence(rec *domain.Recognition, ocr string) bool {
	if rec == nil || strings.TrimSpace(ocr) == "" {
		return false
	}
	var candidates []recoveryPair
	if p, ok := recoveryFromOCRWords(ocr); ok {
		candidates = append(candidates, p)
	}
	if rec.Table != nil {
		if p, ok := recoveryFromTableCorroboratedWordTotals(rec.Table, ocr); ok {
			candidates = append(candidates, p)
		}
		if p, ok := recoveryFromLabeledTable(rec.Table, ocr); ok {
			candidates = append(candidates, p)
		}
		if p, ok := recoveryFromVATRowAndOCRTotal(rec.Table, ocr); ok {
			candidates = append(candidates, p)
		}
		if p, ok := recoveryFromSingleUnclassifiedVAT(rec.Table, rec, ocr); ok {
			candidates = append(candidates, p)
		}
		if p, ok := recoveryFromVATAnnotation(rec.Table, ocr); ok {
			candidates = append(candidates, p)
		}
	}
	if len(candidates) == 0 {
		return false
	}
	pair := candidates[0]
	for _, p := range candidates[1:] {
		if p.total != pair.total || p.vat != pair.vat {
			return false
		}
		if p.fromWords {
			pair.fromWords = true
		}
	}
	if pair.total <= pair.vat || pair.vat <= 0 {
		return false
	}
	candidate := map[string]int64{
		"total": pair.total, "vat_amount": pair.vat, "amount_no_vat": pair.total - pair.vat,
	}
	for k, v := range candidate {
		f, ok := rec.Fields[k]
		if !ok || f.Value == "" {
			continue
		}
		if f.Source == "manual" {
			return false
		}
		if f.Source == "rule" || f.Source == "table" {
			if n, ok := groundedMoneyCents(f.Value); !ok || n != v {
				if !pair.fromWords {
					return false
				}
			}
		}
	}
	if rec.Fields == nil {
		rec.Fields = map[string]domain.Field{}
	}
	changed := false
	for _, k := range []string{"amount_no_vat", "vat_amount", "total"} {
		n := candidate[k]
		prior := rec.Fields[k]
		if v, ok := groundedMoneyCents(prior.Value); ok && v == n {
			continue
		}
		source := "derived"
		if k == "vat_amount" || k == "total" {
			source = pair.kind
		}
		rec.Fields[k] = domain.Field{Value: groundedFormatCents(n), Source: source, Confidence: 0.8}
		changed = true
	}
	return changed
}
