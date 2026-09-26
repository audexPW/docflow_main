package recognize

// Роли граф табличной части: подпись шапки плюс арифметика строк.
//
// Позиции собраны верно, а сумма в 1С не уходит, потому что графе досталась
// не та роль. На бланках заказчика так ломается больше половины несошедшихся
// таблиц:
//
//   - подпись разорвана переносом по строкам шапки: «Коли- чество»,
//     «изме- рения», «Стои-» / «мость»;
//   - OCR путает буквы: «Стоимось», «нлс» вместо «ндс»;
//   - «с НДС», «всего с учетом НДС», «Стоимость … НДС, руб» без слова «сумма»
//     считались суммой налога, и итог по строке уезжал в графу НДС;
//   - «НДС, руб. всего без» (слова переставлены) — тоже налог вместо суммы;
//   - вторая строка шапки («Сумма | НДС | Сумма НДС | Всего с НДС») попадала
//     в данные, а графы оставались без подписи.
//
// Порядок: сначала подпись (columnRole), потом вторая строка шапки
// дописывается к подписям (absorbHeaderRows), и последним — арифметика
// (inferRolesByArithmetic): если по подписям строка не сходится, а среди
// граф есть три, где «без НДС + НДС = с НДС» копейка в копейку, роли
// ставятся по ним. Цифры OCR читает надёжнее подписей.
//
// TABLE_HEADER_FIX=false возвращает прежнее поведение.

import (
	"math"
	"regexp"
	"strconv"
	"strings"

	"docflow/internal/domain"
)

// HeaderFixEnabled — чинить ли роли граф по подписи и арифметике.
func HeaderFixEnabled() bool { return envBool("TABLE_HEADER_FIX", true) }

var (
	// Перенос слова в подписи: «Коли- чество», «изме-\nрения».
	reHyphenBreak = regexp.MustCompile(`(\p{L})-\s+(\p{L})`)
	// Буквы, которые OCR стабильно путает в подписях граф.
	headerOCRFix = strings.NewReplacer(
		"нлс", "ндс", "ндc", "ндс", "hдс", "ндс", "нд с", "ндс",
		"учётом", "учетом", "стоимось", "стоимость", "стоимоть", "стоимость",
		"сумм а", "сумма",
	)
)

func normalizeRoleTitle(title string) string {
	t := strings.ToLower(strings.Join(strings.Fields(title), " "))
	t = reHyphenBreak.ReplaceAllString(t, "$1$2")
	return headerOCRFix.Replace(t)
}

// columnRole — роль графы по подписи. Сначала прежние правила guessRole по
// нормализованной подписи, затем то, что они не узнают.
func columnRole(title string) string {
	if !HeaderFixEnabled() {
		return guessRole(title)
	}
	t := normalizeRoleTitle(title)
	if t == "" {
		return ""
	}
	hasVAT := strings.Contains(t, "ндс") || strings.Contains(t, "налог")
	withVAT := strings.Contains(t, "с ндс") || strings.Contains(t, "с учетом") ||
		strings.Contains(t, "включая") || strings.Contains(t, "с налог")
	withoutVAT := strings.Contains(t, "без ндс") || (hasVAT && strings.Contains(t, "без"))
	money := strings.Contains(t, "стоимост") || strings.Contains(t, "сумм") ||
		strings.Contains(t, "всего") || strings.Contains(t, "итого") ||
		t == "мость" || strings.HasPrefix(t, "мость ") || strings.HasSuffix(t, "стои-") || t == "стои"

	switch {
	case strings.Contains(t, "кол-во"), strings.Contains(t, "колич"), strings.Contains(t, "к-во"),
		t == "чество", strings.HasPrefix(t, "чество "):
		return "qty"
	case strings.Contains(t, "ставка"), strings.Contains(t, "%"):
		return "vat_rate"
	case strings.Contains(t, "цена"), strings.Contains(t, "тариф"):
		return "price"
	case money && ((withVAT && (hasVAT || strings.Contains(t, "всего"))) || withoutVAT):
		return "amount"
	case money && hasVAT && strings.Contains(t, "стоимост") && !strings.Contains(t, "сумм"):
		// «Стоимость … НДС, руб» — обрывок «Стоимость всего с учётом НДС».
		// Налог в бланках подписывают «Сумма НДС», а не «Стоимость».
		return "amount"
	case !money && withVAT && hasVAT:
		// Голое «с НДС» — нижняя строка подписи «Стоимость с НДС».
		return "amount"
	}
	if r := guessRole(t); r != "" && r != "other" {
		return r
	}
	switch {
	case money:
		return "amount"
	case strings.Contains(t, "измер"), strings.HasPrefix(t, "изме"), strings.Contains(t, "единиц"):
		return "unit"
	}
	return guessRole(t)
}

// absorbHeaderRows — строки над первой позицией, в которых нет денег, а
// подписи граф есть, дописываются к подписям. Номера граф («2», «3», «6 %»)
// в подпись не идут.
func absorbHeaderRows(rows [][]string, cols []string) ([][]string, []string) {
	if !HeaderFixEnabled() || len(rows) == 0 {
		return rows, cols
	}
	cols = append([]string(nil), cols...)
	out := make([][]string, 0, len(rows))
	seenData := false
	for _, r := range rows {
		if seenData || rowMoneyCells(r) > 0 || rowNumberCells(r) >= 2 && !isNumberingRow(r) && !rowOnlySmallInts(r) {
			// Первая строка с данными: ниже неё шапки уже нет.
			seenData = true
			out = append(out, r)
			continue
		}
		known := 0
		for _, c := range r {
			c = strings.TrimSpace(c)
			if c == "" || mlIsNum(c) {
				continue
			}
			// Узнаём подпись строгими правилами: «с учётом мест общего
			// пользования» подписью графы не является.
			if role := guessRole(normalizeRoleTitle(c)); role != "" && role != "other" && role != "name" {
				known++
			}
		}
		if known < 2 {
			out = append(out, r)
			continue
		}
		for i, c := range r {
			c = strings.TrimSpace(c)
			if c == "" || mlIsNum(c) || i >= len(cols) {
				continue
			}
			cols[i] = strings.TrimSpace(cols[i] + " " + c)
		}
	}
	return out, cols
}

func rowMoneyCells(r []string) int {
	n := 0
	for _, c := range r {
		c = strings.TrimSpace(c)
		if mlIsNum(c) && !strings.HasSuffix(c, "%") && reCellMoney.MatchString(c) {
			n++
		}
	}
	return n
}

func rowNumberCells(r []string) int {
	n := 0
	for _, c := range r {
		if mlIsNum(strings.TrimSpace(c)) {
			n++
		}
	}
	return n
}

// rowOnlySmallInts — все числа строки — номера граф (1…30).
func rowOnlySmallInts(r []string) bool {
	n := 0
	for _, c := range r {
		c = strings.TrimSpace(c)
		if !mlIsNum(c) {
			continue
		}
		v, err := strconv.Atoi(strings.TrimSuffix(c, "%"))
		if err != nil || v < 0 || v > 30 {
			return false
		}
		n++
	}
	return n > 0
}

func cellValue(r []string, i int) (float64, bool) {
	if i >= len(r) {
		return 0, false
	}
	c := strings.TrimSpace(r[i])
	if !mlIsNum(c) || strings.HasSuffix(c, "%") {
		return 0, false
	}
	v, err := strconv.ParseFloat(normalizeNumber(c), 64)
	return v, err == nil
}

// rolesReconcile — доля строк с деньгами, где при текущих ролях
// «без НДС + НДС = итог» сходится.
func rolesReconcile(rows [][]string, cols, roles []string) (ok, n int) {
	ft := freeTableWithRoles(cols, roles, rows)
	for _, l := range FreeTableToLines(ft) {
		if l.Amount == "" && l.AmountNoVAT == "" {
			continue
		}
		n++
		if l.AmountNoVAT != "" && l.VAT != "" && l.Amount != "" && amountsReconcile(l.AmountNoVAT, l.VAT, l.Amount) {
			ok++
		}
	}
	return ok, n
}

func freeTableWithRoles(cols, roles []string, rows [][]string) *domain.FreeTable {
	return &domain.FreeTable{Columns: cols, Roles: roles, Rows: rows}
}

// inferRolesByArithmetic ставит роли денежных граф по сходимости строк, если
// роли по подписям её не дают.
func inferRolesByArithmetic(rows [][]string, cols, roles []string) []string {
	if !HeaderFixEnabled() || len(rows) == 0 {
		return roles
	}
	width := len(roles)
	nameCol := mlNameCol(roles)
	if ok, n := rolesReconcile(rows, cols, roles); n > 0 && ok*2 > n {
		return roles
	}
	fixed := func(i int) bool {
		switch roleAt(roles, i) {
		case "qty", "price", "vat_rate", "index", "unit":
			return true
		}
		return i == nameCol
	}

	type triple struct{ a, b, c, support, eligible int }
	best := triple{support: 0}
	for a := 0; a < width; a++ {
		for b := a + 1; b < width; b++ {
			for c := 0; c < width; c++ {
				if c == a || c == b || fixed(a) || fixed(b) || fixed(c) {
					continue
				}
				t := triple{a: a, b: b, c: c}
				for _, r := range rows {
					va, oka := cellValue(r, a)
					vb, okb := cellValue(r, b)
					vc, okc := cellValue(r, c)
					if !oka || !okb || !okc || va <= 0 || vb <= 0 || vc <= 0 || !reCellMoney.MatchString(r[c]) {
						continue
					}
					t.eligible++
					if math.Abs(va+vb-vc) < 0.005 {
						t.support++
					}
				}
				if t.support == 0 || t.support*5 < t.eligible*3 {
					continue
				}
				if t.support > best.support || (t.support == best.support && c > best.c) {
					best = t
				}
			}
		}
	}
	if best.support == 0 {
		return roles
	}
	out := append([]string(nil), roles...)
	// НДС — меньшее из двух слагаемых: ставка в бланках заказчика не выше 25%.
	sumA, sumB := 0.0, 0.0
	for _, r := range rows {
		va, oka := cellValue(r, best.a)
		vb, okb := cellValue(r, best.b)
		if oka && okb {
			sumA += va
			sumB += vb
		}
	}
	noVAT, vat := best.a, best.b
	if sumA < sumB {
		noVAT, vat = best.b, best.a
	}
	for i := range out {
		if i == best.c || i == noVAT || i == vat {
			continue
		}
		if out[i] == "amount" || out[i] == "vat" {
			out[i] = "other"
		}
	}
	out[best.c] = "amount"
	out[noVAT] = "amount"
	out[vat] = "vat"
	// Итог должен быть правее суммы без налога: applyAmounts считает итогом
	// последнюю денежную графу, если подпись молчит.
	if best.c < noVAT {
		out[noVAT] = "other"
	}
	if ok, n := rolesReconcile(rows, cols, out); n == 0 || ok*2 <= n {
		return roles
	}
	return out
}

// fixNameColumn — наименование ищется по содержимому, если графа, названная
// наименованием, на деле пустая или числовая. На IMG_0214 первая графа без
// подписи («№») получала роль name, а сами услуги стояли во второй графе с
// подписью «таможенных операций …» — и первая позиция терялась.
func fixNameColumn(rows [][]string, roles []string) []string {
	if !HeaderFixEnabled() || len(rows) == 0 {
		return roles
	}
	textRows := func(col int) int {
		n := 0
		for _, r := range rows {
			if col < len(r) {
				c := strings.TrimSpace(r[col])
				if len([]rune(c)) >= 4 && hasLetters(c) && !mlIsNum(c) {
					n++
				}
			}
		}
		return n
	}
	cur := mlNameCol(roles)
	curN := textRows(cur)
	best, bestN := cur, curN
	for i, r := range roles {
		if i == cur || (r != "" && r != "other") {
			continue
		}
		if n := textRows(i); n > bestN {
			best, bestN = i, n
		}
	}
	// Меняем, только если в нынешней графе наименований заметно меньше.
	if best == cur || bestN*2 <= curN*3 || bestN*2 < len(rows) {
		return roles
	}
	out := append([]string(nil), roles...)
	if out[cur] == "name" {
		out[cur] = "index"
		if curN > 0 {
			out[cur] = "other"
		}
	}
	out[best] = "name"
	return out
}

// Слово «Итого» не в начале строки: «23,54 Итого | | 4,71 | 28,25».
var reTotalsWordInCell = regexp.MustCompile(`(?i)(^|[\s|])(итого|ит0го|итог0)([\s:.,|]|$)`)

var reHeaderWords = regexp.MustCompile(`(?i)(наименование|ед(\.|иница)\s*изм|кол-во|количество|цена|сумма|ставка|стоимость)`)

// clearHeaderLikeCells — ячейка, в которой подряд стоят две и больше подписей
// граф («Наименование работы (услуги), единица измерения … Кол-во»), — это
// шапка, прилипшая к строке данных. Как наименование позиции её брать нельзя.
func clearHeaderLikeCells(cells []string) {
	for i, c := range cells {
		if len(reHeaderWords.FindAllString(c, -1)) >= 2 && !reCellMoney.MatchString(c) {
			cells[i] = ""
		}
	}
}
