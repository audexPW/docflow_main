#!/usr/bin/env bash
# ---------------------------------------------------------------------------
# DocFlow / ПартнерБухгалтер — патч качества распознавания.
#
#  1. Табличная часть больше не выбирается по зашитому приоритету источника.
#     Все разборщики (проекция по координатам, сетка surya, раскладка, модель)
#     чистятся и оцениваются по арифметике самого документа:
#       кол-во x цена = сумма, без НДС + НДС = с НДС, сумма строк = «Итого».
#     Побеждает тот разбор, который сходится с бланком.
#  2. Чистка таблицы: ряд номеров граф (1 2 3 ... 8), подписи-разделители
#     поперёк таблицы («ул. Революционная, 11»), переносы наименования,
#     пустые графы, строки-повторы, О/З/Б вместо 0/3/6, запятая -> точка.
#  3. Реквизиты: выдуманные моделью УНП/ИНН отбрасываются (сверка с текстом),
#     стороны привязываются к подписанным «УНП продавца» / «УНП покупателя»,
#     из наименований срезаются банковские хвосты, итог добирается из «Итого».
#  4. Арендодатель/арендатор добавлены в разбор сторон (счета-протоколы).
#
# Запуск из каталога docflow-deploy:  bash patch-recognition-quality.sh
# ---------------------------------------------------------------------------
set -euo pipefail

ROOT="${1:-$(pwd)}"
REC="$ROOT/docflow/internal/recognize"
[ -f "$REC/pipeline.go" ] || { echo "!! Не нашёл $REC/pipeline.go — запусти из каталога docflow-deploy"; exit 1; }

STAMP="$(date +%Y%m%d-%H%M%S)"
BAK="$ROOT/.backup-recognition-$STAMP"
mkdir -p "$BAK"
cp "$REC/pipeline.go" "$REC/refine_split.go" "$REC/extract.go" "$BAK/"
echo ">> Бэкап исходников: $BAK"
echo ">> Кладу новые файлы..."

cat > "$REC/quality.go" <<'GOEOF_QUALITY_GO'
package recognize

// Чистка табличной части и выбор лучшего кандидата по арифметике документа.
//
// Раньше приоритет между разборщиками таблицы был зашит в код: сперва
// побеждала проекция по координатам слов, потом — сетка детектора. Оба
// правила одинаково слепые: источник выбирается заранее, без единого взгляда
// на то, что он выдал на этом конкретном бланке. Отсюда и «каша»: там, где
// выбранный разбор рассыпался, он всё равно оставался победителем.
//
// Здесь таблица сначала чистится (ряд номеров граф, подписи-разделители,
// переносы наименования, пустые графы, повторы), а потом получает оценку по
// арифметике самого документа: количество × цена = сумма, сумма без НДС + НДС
// = сумма с НДС, сумма строк = «Итого». Бланк проверяет сам себя, и берётся
// тот разбор, который сходится, — независимо от того, кто его собрал.

import (
	"fmt"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"

	"docflow/internal/domain"
)

var (
	// Ячейка целиком является числом (с разделителем разрядов и без).
	reNumericCell = regexp.MustCompile(`^-?\d{1,3}(?:[ \x{00A0}]?\d{3})*(?:[.,]\d{1,6})?\s*%?$`)
	// Мусор в графе наименования: туда заезжают реквизиты из шапки.
	reJunkName = regexp.MustCompile(`(?i)(р\s*/\s*с|расч[ёе]тн|унп|бик|окпо|адрес|тел\.|телефон|в\s+банке|дирекция|грузоотправ|грузополуч|поставщик|покупатель|плательщик|продавец|[A-Z]{2}\d{2}[A-Z]{4}\d{6})`)
	// Деньги внутри ячейки — для отличия подписи-разделителя от позиции.
	reCellMoney = regexp.MustCompile(`\d+[.,]\d{2}\b`)
)

// Замены символов, которые OCR стабильно путает в цифрах.
var ocrDigitFix = strings.NewReplacer(
	"О", "0", "о", "0", "O", "0", "o", "0",
	"З", "3", "з", "3",
	"Б", "6", "б", "6",
	"l", "1", "I", "1", "|", "1", "і", "1",
	"S", "5", "s", "5",
	"\u00A0", " ",
)

// cellNum разбирает содержимое ячейки как число.
func cellNum(s string) (float64, bool) {
	s = strings.TrimSpace(normalizeNumber(s))
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// tidyNumber убирает разделители разрядов и приводит дробную часть к точке.
func tidyNumber(s string) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\u00A0", "")
	s = strings.ReplaceAll(s, " ", "")
	pct := strings.HasSuffix(s, "%")
	s = strings.TrimSuffix(s, "%")
	// Разделителем дробной части считаем последний знак: «1.234,56» и
	// «1,234.56» — одно и то же число, записанное по-разному.
	lastDot := strings.LastIndexByte(s, '.')
	lastCom := strings.LastIndexByte(s, ',')
	cut := lastDot
	if lastCom > cut {
		cut = lastCom
	}
	if cut >= 0 {
		head := strings.NewReplacer(".", "", ",", "").Replace(s[:cut])
		s = head + "." + s[cut+1:]
	}
	if pct {
		s += "%"
	}
	return s
}

// repairNumericCell чинит числовую ячейку, испорченную распознаванием, и
// оставляет всё остальное как есть. Правка применяется только если после
// замен ячейка стала числом целиком: «кВт», «кв.ч» и наименования не трогаем.
func repairNumericCell(s string) string {
	t := strings.TrimSpace(s)
	if t == "" {
		return t
	}
	if reNumericCell.MatchString(t) {
		return tidyNumber(t)
	}
	if !strings.ContainsAny(t, "0123456789") {
		return t
	}
	if c := ocrDigitFix.Replace(t); reNumericCell.MatchString(c) {
		return tidyNumber(c)
	}
	return t
}

// cleanCell приводит ячейку к печатному виду: один пробел между словами,
// без переносов, с починенным числом.
func cleanCell(s string) string {
	s = strings.ReplaceAll(s, "\u00A0", " ")
	s = strings.Join(strings.Fields(s), " ")
	s = strings.Trim(s, " |¦")
	return repairNumericCell(s)
}

// normalizeHeader — то же для подписи графы, но без починки чисел: заголовок
// числом быть не должен.
func normalizeHeader(s string) string {
	s = strings.ReplaceAll(s, "\u00A0", " ")
	s = strings.Join(strings.Fields(s), " ")
	return strings.Trim(s, " |¦,;")
}

// isSectionRow — ряд-подпись поперёк таблицы: заполнена одна ячейка, в ней
// текст и нет денег. В счёте-фактуре так печатают адрес объекта
// («ул. Революционная, 11») между шапкой и позициями; в таблицу он попадал
// отдельной позицией и тянул за собой сдвиг всех колонок.
func isSectionRow(cells []string) bool {
	if len(cells) < 3 {
		return false
	}
	filled, idx := 0, -1
	for i, c := range cells {
		if strings.TrimSpace(c) != "" {
			filled++
			idx = i
		}
	}
	if filled != 1 || idx < 0 {
		return false
	}
	v := cells[idx]
	return hasLetters(v) && !reCellMoney.MatchString(v)
}

// rowEqualsHeader — ряд дословно повторяет шапку (сетка иногда отдаёт её и
// как заголовок, и как первую строку данных).
func rowEqualsHeader(cells, cols []string) bool {
	same, filled := 0, 0
	for i, c := range cells {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		filled++
		if i < len(cols) && strings.EqualFold(c, strings.TrimSpace(cols[i])) {
			same++
		}
	}
	return filled >= 2 && same == filled
}

// CleanFreeTable возвращает вычищенную копию таблицы. Исходная структура не
// меняется — кандидатов сравнивают между собой, и порча одного из них по
// дороге сделала бы сравнение бессмысленным.
func CleanFreeTable(in *domain.FreeTable) *domain.FreeTable {
	if in == nil {
		return nil
	}
	width := len(in.Columns)
	for _, r := range in.Rows {
		if len(r) > width {
			width = len(r)
		}
	}
	if len(in.Totals) > width {
		width = len(in.Totals)
	}
	if width == 0 {
		return nil
	}

	cols := make([]string, width)
	for i := 0; i < width && i < len(in.Columns); i++ {
		cols[i] = normalizeHeader(in.Columns[i])
	}
	roles := rolesFor(cols)

	var (
		rows      [][]string
		totals    []string
		hasTotals bool
	)
	for _, raw := range in.Rows {
		cells := padRow(raw, width)
		for i := range cells {
			cells[i] = cleanCell(cells[i])
		}
		joined := strings.TrimSpace(strings.Join(cells, " "))
		if joined == "" {
			continue
		}
		if isNumberingRow(cells) {
			continue
		}
		if rowEqualsHeader(cells, cols) {
			continue
		}
		if reTotalsRow.MatchString(joined) {
			if !hasTotals {
				totals, hasTotals = cells, true
			}
			continue
		}
		if isSectionRow(cells) {
			// До первой позиции это подпись раздела — она не строка
			// документа. После — почти всегда хвост наименования.
			if len(rows) > 0 {
				appendToFreeName(rows[len(rows)-1], cells, roles)
			}
			continue
		}
		if len(rows) > 0 && isNameContinuation(cells, roles) {
			appendToFreeName(rows[len(rows)-1], cells, roles)
			continue
		}
		if !hasLetters(joined) && !reNumberish.MatchString(joined) {
			continue
		}
		rows = append(rows, cells)
	}
	if !hasTotals && len(in.Totals) > 0 {
		totals = padRow(in.Totals, width)
		for i := range totals {
			totals[i] = cleanCell(totals[i])
		}
		hasTotals = strings.TrimSpace(strings.Join(totals, "")) != ""
	}
	rows = dropDegenerateRows(rows)
	if len(rows) == 0 {
		return nil
	}

	// Пустые графы: подписи нет и во всех строках пусто. Они сдвигают
	// таблицу вправо и в интерфейсе выглядят разрывом.
	keep := make([]int, 0, width)
	for i := 0; i < width; i++ {
		used := strings.TrimSpace(cols[i]) != ""
		if !used {
			for _, r := range rows {
				if strings.TrimSpace(r[i]) != "" {
					used = true
					break
				}
			}
		}
		if !used && hasTotals && strings.TrimSpace(totals[i]) != "" {
			used = true
		}
		if used {
			keep = append(keep, i)
		}
	}
	if len(keep) < 2 {
		return nil
	}
	pick := func(src []string) []string {
		out := make([]string, 0, len(keep))
		for _, i := range keep {
			out = append(out, src[i])
		}
		return out
	}
	out := &domain.FreeTable{Columns: pick(cols), Source: in.Source}
	for _, r := range rows {
		out.Rows = append(out.Rows, pick(r))
	}
	if hasTotals {
		out.Totals = pick(totals)
	}
	out.Roles = rolesFor(out.Columns)
	return out
}

// ScoreFreeTable оценивает разбор от 0 до 1 и возвращает короткую расшифровку
// для журнала. Оценка опирается на внутреннюю арифметику документа, а не на
// то, каким способом таблица собрана.
func ScoreFreeTable(ft *domain.FreeTable) (float64, string) {
	if ft == nil || len(ft.Rows) == 0 || len(ft.Columns) < 2 {
		return 0, "пусто"
	}
	nRows, nCols := len(ft.Rows), len(ft.Columns)

	filled := 0
	for _, r := range ft.Rows {
		for _, c := range r {
			if strings.TrimSpace(c) != "" {
				filled++
			}
		}
	}
	fill := float64(filled) / float64(nRows*nCols)

	score := 0.15
	score += 0.10 * math.Min(1, fill/0.5)

	roles := ft.Roles
	if len(roles) != nCols {
		roles = rolesFor(ft.Columns)
	}
	var hasName, hasAmount, hasQtyPrice bool
	for _, r := range roles {
		switch r {
		case "name":
			hasName = true
		case "amount":
			hasAmount = true
		case "qty", "price", "unit":
			hasQtyPrice = true
		}
	}
	if hasName {
		score += 0.10
	}
	if hasAmount {
		score += 0.10
	}
	if hasQtyPrice {
		score += 0.05
	}

	lines := FreeTableToLines(ft)

	// Наименования: в графе должен стоять товар или услуга, а не реквизит.
	good := 0
	for _, l := range lines {
		n := strings.TrimSpace(l.Name)
		if len([]rune(n)) >= 3 && hasLetters(n) && !reJunkName.MatchString(n) {
			good++
		}
	}
	if len(lines) > 0 {
		score += 0.15 * float64(good) / float64(len(lines))
	}

	// Арифметика строки: количество × цена = сумма.
	mulOK, mulN := 0, 0
	vatOK, vatN := 0, 0
	for _, l := range lines {
		q, ok1 := cellNum(l.Qty)
		p, ok2 := cellNum(l.Price)
		base := l.AmountNoVAT
		if base == "" {
			base = l.Amount
		}
		if a, ok3 := cellNum(base); ok1 && ok2 && ok3 && a > 0 && q > 0 && p > 0 {
			mulN++
			if math.Abs(q*p-a) <= math.Max(0.05, a*0.02) {
				mulOK++
			}
		}
		if l.AmountNoVAT != "" && l.VAT != "" && l.Amount != "" {
			vatN++
			if amountsReconcile(l.AmountNoVAT, l.VAT, l.Amount) {
				vatOK++
			}
		}
	}
	if mulN > 0 {
		score += 0.20 * float64(mulOK) / float64(mulN)
	}
	if vatN > 0 {
		score += 0.10 * float64(vatOK) / float64(vatN)
	}

	// Сумма строк против «Итого» самой таблицы.
	totalsHit := false
	if t := maxMoneyIn(ft.Totals); t > 0 {
		var withVAT, noVAT float64
		for _, l := range lines {
			if v, ok := cellNum(l.Amount); ok {
				withVAT += v
			}
			if v, ok := cellNum(l.AmountNoVAT); ok {
				noVAT += v
			}
		}
		tol := math.Max(0.05, t*0.02)
		if math.Abs(withVAT-t) <= tol || math.Abs(noVAT-t) <= tol {
			score += 0.15
			totalsHit = true
		}
	}

	// Штрафы за характерные развалы разбора.
	seen := map[string]int{}
	for _, r := range ft.Rows {
		seen[strings.ToLower(strings.Join(r, "\x00"))]++
	}
	dups := 0
	for _, n := range seen {
		if n > 1 {
			dups += n - 1
		}
	}
	if dups > 0 {
		score -= 0.20 * float64(dups) / float64(nRows)
	}
	if nCols > 16 {
		score -= 0.05
	}
	if len(lines) == 0 {
		score -= 0.15
	}

	if score < 0 {
		score = 0
	}
	if score > 1 {
		score = 1
	}
	diag := fmt.Sprintf("строк=%d граф=%d заполн=%.0f%% имена=%d/%d кол×цена=%d/%d ндс=%d/%d итог=%v",
		nRows, nCols, fill*100, good, len(lines), mulOK, mulN, vatOK, vatN, totalsHit)
	return score, diag
}

// maxMoneyIn — наибольшее число в ряду. В строке «Итого» их обычно три
// (без НДС, НДС, с НДС), и к оплате идёт наибольшее.
func maxMoneyIn(cells []string) float64 {
	best := 0.0
	for _, c := range cells {
		if v, ok := cellNum(c); ok && v > best {
			best = v
		}
	}
	return best
}

// Небольшая надбавка за источник — только чтобы разрешить ничью. На разницу
// в качестве она не влияет: 0.03 против шага в 0.10–0.20 у арифметики.
var tableSourceBonus = map[string]float64{
	"grid":    0.03,
	"columns": 0.02,
	"layout":  0.01,
	"model":   0.00,
}

// PickBestFreeTable чистит всех кандидатов, оценивает и возвращает лучшего
// вместе со строкой для журнала. TABLE_PRIORITY=grid|columns|layout|model
// по-прежнему работает: названный источник получает решающую надбавку.
func PickBestFreeTable(cands []*domain.FreeTable) (*domain.FreeTable, string) {
	forced := strings.ToLower(strings.TrimSpace(os.Getenv("TABLE_PRIORITY")))
	var (
		best      *domain.FreeTable
		bestScore = -1.0
		parts     []string
	)
	for _, c := range cands {
		cl := CleanFreeTable(c)
		if cl == nil {
			continue
		}
		s, why := ScoreFreeTable(cl)
		s += tableSourceBonus[cl.Source]
		if forced != "" && forced == strings.ToLower(cl.Source) {
			s += 0.5
		}
		parts = append(parts, fmt.Sprintf("%s=%.2f [%s]", cl.Source, s, why))
		if s > bestScore {
			bestScore, best = s, cl
		}
	}
	if len(parts) == 0 {
		return nil, "кандидатов нет"
	}
	return best, strings.Join(parts, "; ")
}
GOEOF_QUALITY_GO

cat > "$REC/fieldfix.go" <<'GOEOF_FIELDFIX_GO'
package recognize

// Проверка реквизитов после правил и после модели.
//
// Две болезни, которые видно на любом бланке заказчика:
//
//  1. Модель придумывает налоговый номер. На счёте, где УНП плательщика вообще
//     не напечатан, в поле organization_unp приезжало правдоподобное
//     девятизначное число, и документ уходил в 1С уже с ошибкой — заметить её
//     потом некому.
//  2. Стороны меняются местами. Продавец и покупатель напечатаны подряд, и
//     модель берёт контрагентом ту сторону, что ближе к концу текста.
//
// Обе лечатся не промптом, а сверкой с самим текстом: номера, которых в
// документе нет, отбрасываются, а стороны привязываются к подписанным
// «УНП продавца» / «УНП покупателя».

import (
	"regexp"
	"strconv"
	"strings"

	"docflow/internal/domain"
)

var (
	// Подписанные налоговые номера сторон. Скобка в «УНП покупателя
	// (заказчика) 590888079» — обычное дело, поэтому окно до цифр широкое.
	reUNPSeller = regexp.MustCompile(`(?i)УНП\s*(?:продавца|поставщика|исполнителя|арендодателя|грузоотправителя|получателя\s+платежа)[^\d\n]{0,25}(\d{9})`)
	reUNPBuyer  = regexp.MustCompile(`(?i)УНП\s*(?:покупателя|заказчика|плательщика|грузополучателя|арендатора)[^\d\n]{0,25}(\d{9})`)

	// Хвосты, которые заезжают в наименование стороны следом за названием.
	rePartyCut = regexp.MustCompile(`(?i)(,?\s*(адрес|р\s*/\s*с|расч[ёе]тн|в\s+банке|банк[:\s]|дирекция|бик|окпо|унп|инн|тел\.|телефон|код\s+банка|e-?mail)).*$`)
)

// Реквизиты, которые обязаны находиться в тексте документа. Номер документа и
// дату сюда не берём: номер модель нередко чинит («No» → «№»), а дату
// переписывает в другой формат, и сверка по цифрам их зря отбросит.
var groundedIDKeys = []string{"unp", "inn", "kpp", "organization_unp"}
var groundedMoneyKeys = []string{"total", "vat_amount", "amount_no_vat"}

func digitsOf(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// groundedInText — цифры значения действительно встречаются в распознанном
// тексте подряд, с поправкой на разделители («36.54», «36 54», «36,54»).
func groundedInText(val, text string) bool {
	d := digitsOf(val)
	if len(d) < 4 {
		return true // слишком коротко, чтобы отличить совпадение от случайности
	}
	var b strings.Builder
	for i, r := range d {
		if i > 0 {
			b.WriteString(`[\s.,\-']{0,3}`)
		}
		b.WriteRune(r)
	}
	re, err := regexp.Compile(b.String())
	if err != nil {
		return true
	}
	return re.MatchString(text)
}

// cleanPartyName отрезает от наименования стороны реквизиты и банк.
func cleanPartyName(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	s = rePartyCut.ReplaceAllString(s, "")
	s = strings.TrimSpace(strings.Trim(strings.TrimSpace(s), ",;:-"))
	if len([]rune(s)) < 3 {
		return ""
	}
	return s
}

// FixFields приводит набор реквизитов в порядок по тексту документа.
// Вызывается последним шагом, поэтому чинит и правила, и ответ модели.
func FixFields(fields map[string]domain.Field, ocrText string) {
	if len(fields) == 0 || strings.TrimSpace(ocrText) == "" {
		return
	}

	// 1. Выдуманные номера — вон. Проверка дешёвая и односторонняя: то, чего
	// в тексте нет, в документе появиться не могло.
	for _, k := range groundedIDKeys {
		if f, ok := fields[k]; ok && f.Value != "" && !groundedInText(f.Value, ocrText) {
			delete(fields, k)
		}
	}
	// Суммы не удаляем: модель иногда справедливо чинит цифру, которую OCR
	// прочёл буквой. Но уверенность роняем, чтобы поле попало на проверку.
	for _, k := range groundedMoneyKeys {
		if f, ok := fields[k]; ok && f.Value != "" && !groundedInText(f.Value, ocrText) {
			if f.Confidence > 0.45 {
				f.Confidence = 0.45
			}
			fields[k] = f
		}
	}

	// 2. Стороны — по подписанным номерам, а не по порядку строк.
	if m := reUNPSeller.FindStringSubmatch(ocrText); m != nil {
		fields["unp"] = domain.Field{Value: m[1], Confidence: 0.9, Source: "rule"}
	}
	if m := reUNPBuyer.FindStringSubmatch(ocrText); m != nil {
		fields["organization_unp"] = domain.Field{Value: m[1], Confidence: 0.9, Source: "rule"}
	}
	// Одна и та же сторона не может быть и продавцом, и покупателем.
	if a, ok := fields["unp"]; ok {
		if b, ok2 := fields["organization_unp"]; ok2 && a.Value == b.Value {
			if b.Confidence < a.Confidence {
				delete(fields, "organization_unp")
			} else {
				delete(fields, "unp")
			}
		}
	}

	// 3. Наименования сторон без банковских хвостов.
	for _, k := range []string{"counterparty", "organization"} {
		f, ok := fields[k]
		if !ok || f.Value == "" {
			continue
		}
		v := cleanPartyName(f.Value)
		if v == "" {
			delete(fields, k)
			continue
		}
		if v != f.Value {
			f.Value = v
			fields[k] = f
		}
	}
	// Контрагент и организация не могут совпадать: так бывает, когда модель
	// взяла одну и ту же строку дважды.
	if a, ok := fields["counterparty"]; ok {
		if b, ok2 := fields["organization"]; ok2 && strings.EqualFold(a.Value, b.Value) {
			delete(fields, "organization")
		}
	}
}

// FillTotalsFromTable добирает итог документа из строки «Итого» таблицы, когда
// в шапке и подвале его не нашли. Значение в таблице напечатано цифрами и
// проверено арифметикой строк — оно надёжнее догадки по всему тексту.
func FillTotalsFromTable(rec *domain.Recognition) {
	if rec == nil || rec.Table == nil || rec.Table.RawCells == nil {
		return
	}
	totals := rec.Table.RawCells.Totals
	if len(totals) == 0 {
		return
	}
	t := maxMoneyIn(totals)
	if t <= 0 {
		return
	}
	if rec.Fields == nil {
		rec.Fields = map[string]domain.Field{}
	}
	cur, ok := rec.Fields["total"]
	if ok && cur.Confidence >= ruleConfidence {
		if v, okv := cellNum(cur.Value); okv && v > 0 {
			return
		}
	}
	rec.Fields["total"] = domain.Field{
		Value:      strconv.FormatFloat(t, 'f', 2, 64),
		Confidence: 0.8,
		Source:     "table",
	}
}
GOEOF_FIELDFIX_GO

cat > "$REC/quality_test.go" <<'GOEOF_QUALITY_TEST_GO'
package recognize

import (
	"strings"
	"testing"

	"docflow/internal/domain"
)

// Счёт-фактура БГУ № 5479: две позиции, ряд номеров граф под шапкой и
// подпись-разделитель «ул. Революционная, 11» между шапкой и позициями.
func bguGood() *domain.FreeTable {
	return &domain.FreeTable{
		Source: "grid",
		Columns: []string{
			"Наименование", "Ед. измер.", "Кол-во", "Тариф",
			"Стоимость всего без НДС, руб.", "Ст. НДС, %",
			"Сумма НДС, руб", "Стоимость всего с учетом НДС, руб",
		},
		Rows: [][]string{
			{"1", "2", "3", "4", "5", "6", "7", "8"},
			{"", "", "", "", "", "", "", "ул. Революционная, 11"},
			{"Возмещение затрат по электроэнергии (по отдельно установленному прибору электрической энергии)", "кВт", "73", "0.41009", "29.94", "20%", "5.99", "35.93"},
			{"Возмещение затрат по электроэнергии (места общего пользования)", "кВт", "1,52", "0,33842", "0,51", "20%", "0,10", "0,61"},
			{"Итого:", "", "", "", "30,45", "", "6,09", "36,54"},
		},
	}
}

// Тот же документ, разобранный вкривь: строки разъехались по графам.
func bguBad() *domain.FreeTable {
	return &domain.FreeTable{
		Source: "columns",
		Columns: []string{
			"Наименование", "Ед. измер.", "Кол-во", "Тариф",
			"Стоимость всего без НДС, руб.", "Ст. НДС, %",
			"Сумма НДС, руб", "Стоимость всего с учетом НДС, руб",
		},
		Rows: [][]string{
			{"Возмещение затрат по", "", "", "", "", "", "", ""},
			{"электроэнергии (по отдельно", "кВт", "0.41009", "29.94", "20", "5.99", "35.93", ""},
			{"установленному прибору", "", "73", "", "", "", "", ""},
			{"Возмещение затрат по", "кВт", "0.33842", "0.51", "20", "0.10", "0.61", ""},
			{"электроэнергии (места", "", "1.52", "", "", "", "", ""},
		},
	}
}

func TestCleanDropsNumberingAndSectionRows(t *testing.T) {
	ft := CleanFreeTable(bguGood())
	if ft == nil {
		t.Fatal("таблица вычищена в ноль")
	}
	if len(ft.Rows) != 2 {
		t.Fatalf("ожидались 2 позиции, получено %d: %v", len(ft.Rows), ft.Rows)
	}
	for _, r := range ft.Rows {
		if isNumberingRow(r) {
			t.Fatalf("ряд номеров граф остался в таблице: %v", r)
		}
		if strings.Contains(strings.Join(r, " "), "Революционная") {
			t.Fatalf("подпись-разделитель осталась позицией: %v", r)
		}
	}
	if len(ft.Totals) == 0 {
		t.Fatal("строка Итого не выделена")
	}
	if got := maxMoneyIn(ft.Totals); got != 36.54 {
		t.Fatalf("итог таблицы %v, ожидалось 36.54", got)
	}
	// Запятые в числах приведены к точке.
	if ft.Rows[1][2] != "1.52" {
		t.Fatalf("количество не нормализовано: %q", ft.Rows[1][2])
	}
}

func TestScorePrefersArithmeticallyConsistentTable(t *testing.T) {
	good, _ := ScoreFreeTable(CleanFreeTable(bguGood()))
	bad, _ := ScoreFreeTable(CleanFreeTable(bguBad()))
	if good <= bad {
		t.Fatalf("развалившийся разбор не должен выигрывать: good=%.2f bad=%.2f", good, bad)
	}
	if good < 0.7 {
		t.Fatalf("верный разбор оценён слишком низко: %.2f", good)
	}
}

func TestPickBestIgnoresSourceOrder(t *testing.T) {
	// Проекция подана первой и с более высоким приоритетом источника — но
	// сходится по арифметике сетка, её и надо взять.
	best, diag := PickBestFreeTable([]*domain.FreeTable{bguBad(), bguGood()})
	if best == nil {
		t.Fatal("кандидат не выбран")
	}
	if best.Source != "grid" {
		t.Fatalf("выбран %q вместо grid; %s", best.Source, diag)
	}
	if len(best.Rows) != 2 {
		t.Fatalf("в выбранной таблице %d строк: %v", len(best.Rows), best.Rows)
	}
}

func TestRepairNumericCellLeavesTextAlone(t *testing.T) {
	cases := map[string]string{
		"З0,45":                       "30.45",
		"1 234,56":                    "1234.56",
		"0.41009":                     "0.41009",
		"20%":                         "20%",
		"кВт":                         "кВт",
		"кв.ч":                        "кв.ч",
		"17.03.20":                    "17.03.20",
		"BY23PJCB3012080588100000933": "BY23PJCB3012080588100000933",
		"Возмещение затрат":           "Возмещение затрат",
	}
	for in, want := range cases {
		if got := repairNumericCell(in); got != want {
			t.Errorf("repairNumericCell(%q) = %q, ожидалось %q", in, got, want)
		}
	}
}

const bguText = `СЧЕТ-ФАКТУРА № 5479
17 Ноября 2023 г.
Продавец: Белорусский государственный университет.
Адрес г.Минск, пр.Независимости,4.
УНП продавца 100235722.
Банковские реквизиты: р/с № BY05BLBB36420100235722001002.
в Дирекция ОАО Белинвестбанк по г. Минску и Минской обл.,BLBBBY2X
Покупатель ООО "КофеВенд".
Адрес 220002, г. Минск, ул. Сторожевска, 8, пом. 8.
УНП покупателя (заказчика) 590888079.
К оплате 36.54 (Тридцать шесть белорусских рублей 54 копейки)`

func TestFixFieldsDropsInventedTaxIDAndOrdersParties(t *testing.T) {
	fields := map[string]domain.Field{
		// Модель придумала УНП плательщика — в тексте его нет.
		"organization_unp": {Value: "200020001", Confidence: 0.85, Source: "model"},
		// И перепутала стороны местами.
		"unp":          {Value: "590888079", Confidence: 0.85, Source: "model"},
		"counterparty": {Value: `ООО "КофеВенд", УНП 590888079`, Confidence: 0.85, Source: "model"},
		"organization": {Value: "Белорусский государственный университет. Адрес г.Минск, пр.Независимости,4", Confidence: 0.85, Source: "model"},
	}
	FixFields(fields, bguText)

	if got := fields["unp"].Value; got != "100235722" {
		t.Fatalf("УНП продавца = %q, ожидалось 100235722", got)
	}
	if got := fields["organization_unp"].Value; got != "590888079" {
		t.Fatalf("УНП покупателя = %q, ожидалось 590888079", got)
	}
	if got := fields["counterparty"].Value; strings.Contains(got, "УНП") {
		t.Fatalf("в наименовании контрагента остались реквизиты: %q", got)
	}
	if got := fields["organization"].Value; strings.Contains(strings.ToLower(got), "адрес") {
		t.Fatalf("в наименовании организации остался адрес: %q", got)
	}
}

func TestFixFieldsKeepsGroundedValues(t *testing.T) {
	fields := map[string]domain.Field{
		"unp":   {Value: "100235722", Confidence: 0.85, Source: "model"},
		"total": {Value: "36.54", Confidence: 0.85, Source: "model"},
	}
	FixFields(fields, bguText)
	if fields["unp"].Value != "100235722" {
		t.Fatal("верный УНП отброшен")
	}
	if f := fields["total"]; f.Value != "36.54" || f.Confidence < 0.8 {
		t.Fatalf("верная сумма испорчена: %+v", f)
	}
}

func TestFillTotalsFromTable(t *testing.T) {
	rec := domain.Recognition{
		Fields: map[string]domain.Field{},
		Table:  FreeTableToTable(CleanFreeTable(bguGood())),
	}
	FillTotalsFromTable(&rec)
	if got := rec.Fields["total"].Value; got != "36.54" {
		t.Fatalf("итог из таблицы = %q, ожидалось 36.54", got)
	}
}
GOEOF_QUALITY_TEST_GO

echo ">> Правлю pipeline.go / refine_split.go / extract.go..."
python3 - "$REC" <<'PYPATCH_EOF'
import sys, os
rec = sys.argv[1]

def load(n):
    return open(os.path.join(rec, n), encoding="utf-8").read()

def save(n, s):
    open(os.path.join(rec, n), "w", encoding="utf-8").write(s)

fail = []

# ---------------- pipeline.go: выбор таблицы по оценке ----------------
s = load("pipeline.go")
if "PickBestFreeTable" in s:
    print("   pipeline.go уже пропатчен, пропускаю")
else:
    a = s.find("\t// Проекция колонок по координатам слов.")
    b = s.find("\ttypeConf := byRules.DocTypeConfidence")
    if a < 0 or b < 0 or b <= a:
        fail.append("pipeline.go: не найден блок выбора таблицы")
    else:
        new = "\n".join([
            "\t// Кандидаты на табличную часть. Раньше приоритет между ними был зашит",
            "\t// в код и не зависел от того, что каждый из них выдал на этом бланке.",
            "\t// Теперь кандидаты чистятся и оцениваются по арифметике документа",
            "\t// (кол-во x цена = сумма, без НДС + НДС = с НДС, сумма строк = «Итого»),",
            "\t// и берётся тот разбор, который сходится.",
            "\tcands := make([]*domain.FreeTable, 0, 3)",
            "\tif byRules.Table != nil && byRules.Table.RawCells != nil {",
            "\t\tcands = append(cands, byRules.Table.RawCells)",
            "\t}",
            "\tif ft := TableFromWords(page.Words); ft != nil {",
            "\t\tp.log.Info(\"таблица собрана проекцией колонок\",",
            "\t\t\t\"колонок\", len(ft.Columns), \"строк\", len(ft.Rows),",
            "\t\t\t\"шапка\", strings.Join(ft.Columns, \" | \"))",
            "\t\tcands = append(cands, ft)",
            "\t} else if len(page.Words) > 0 {",
            "\t\tp.log.Info(\"проекция колонок не дала таблицу\", \"слов\", len(page.Words))",
            "\t}",
            "",
            "\tcellCount := 0",
            "\tfor _, g := range page.Grids {",
            "\t\tcellCount += len(g.Cells)",
            "\t}",
            "\tp.log.Info(\"grid diag: получено от surya\", \"tables\", len(page.Grids), \"cells\", cellCount)",
            "\tif ft := FreeTableFromGrid2(page.Grids); ft != nil {",
            "\t\tp.log.Info(\"grid diag: таблица собрана\", \"columns\", len(ft.Columns), \"rows\", len(ft.Rows))",
            "\t\tcands = append(cands, ft)",
            "\t} else {",
            "\t\tp.log.Warn(\"grid diag: сетка не использована\", \"причина\", GridDiag())",
            "\t}",
            "",
            "\tif best, diag := PickBestFreeTable(cands); best != nil {",
            "\t\tp.log.Info(\"табличная часть выбрана\", \"источник\", best.Source,",
            "\t\t\t\"колонок\", len(best.Columns), \"строк\", len(best.Rows),",
            "\t\t\t\"шапка\", strings.Join(best.Columns, \" | \"), \"оценки\", diag)",
            "\t\tfor i, r := range best.Rows {",
            "\t\t\tp.log.Info(\"таблица: строка\", \"n\", i+1, \"ячейки\", strings.Join(r, \" | \"))",
            "\t\t}",
            "\t\tbyRules.Table = FreeTableToTable(best)",
            "\t\tbyRules.Lines = FreeTableToLines(best)",
            "\t} else {",
            "\t\tp.log.Warn(\"табличная часть не собрана ни одним разборщиком\")",
            "\t}",
            "",
            "",
        ])
        s = s[:a] + new + s[b:]
        anchor = "\t// Счета учёта подбираем последним шагом"
        if anchor not in s:
            fail.append("pipeline.go: не найден вызов ApplyAccounts")
        else:
            ins = "\n".join([
                "\t// Реквизиты сверяем с текстом документа: выдуманные моделью номера",
                "\t// отбрасываем, стороны привязываем к подписанным «УНП продавца» /",
                "\t// «УНП покупателя», из наименований убираем банковские хвосты.",
                "\tFixFields(rec.Fields, ocrText)",
                "\tFillTotalsFromTable(&rec)",
                "",
                "",
            ])
            s = s.replace(anchor, ins + anchor, 1)
            save("pipeline.go", s)
            print("   pipeline.go пропатчен")

# ---------------- refine_split.go: таблица модели через ту же оценку -------
s = load("refine_split.go")
if "табличная часть после модели" in s:
    print("   refine_split.go уже пропатчен, пропускаю")
else:
    old_grid = "\tgridOK := tableIsGeometric(byRules.Table)\n\t// Таблицу спрашиваем"
    old_blk = "\n".join([
        "\tlines := byRules.Lines",
        "\ttable := byRules.Table",
        "\tif ft := convertModelTable(tabRes.Table); ft != nil && !gridOK {",
        "\t\ttable = FreeTableToTable(ft)",
        "\t\tlines = FreeTableToLines(ft)",
        "\t}",
        "\t// Счета учёта модель отдаёт в lines[]; переносим по совпадению наименования.",
        "\tif acc := convertModelLines(tabRes.Lines); len(acc) > 0 {",
        "\t\tif len(lines) == 0 && !gridOK && table == nil {",
    ])
    new_blk = "\n".join([
        "\tlines := byRules.Lines",
        "\ttable := byRules.Table",
        "\t// Таблицу от модели не берём на веру и не отвергаем по источнику: она",
        "\t// проходит ту же чистку и ту же проверку арифметикой, что и разборы по",
        "\t// координатам. Побеждает та, что сходится с документом.",
        "\tif ft := convertModelTable(tabRes.Table); ft != nil {",
        "\t\tvar geom *domain.FreeTable",
        "\t\tif byRules.Table != nil {",
        "\t\t\tgeom = byRules.Table.RawCells",
        "\t\t}",
        "\t\tif best, diag := PickBestFreeTable([]*domain.FreeTable{geom, ft}); best != nil {",
        "\t\t\tp.log.Info(\"табличная часть после модели\", \"источник\", best.Source, \"оценки\", diag)",
        "\t\t\ttable = FreeTableToTable(best)",
        "\t\t\tlines = FreeTableToLines(best)",
        "\t\t}",
        "\t}",
        "\t// Счета учёта модель отдаёт в lines[]; переносим по совпадению наименования.",
        "\tif acc := convertModelLines(tabRes.Lines); len(acc) > 0 {",
        "\t\tif len(lines) == 0 && table == nil {",
    ])
    if old_grid not in s or old_blk not in s:
        fail.append("refine_split.go: не найден блок сборки ответа модели")
    else:
        s = s.replace(old_grid, "\t// Таблицу спрашиваем", 1)
        s = s.replace(old_blk, new_blk, 1)
        save("refine_split.go", s)
        print("   refine_split.go пропатчен")

# ---------------- extract.go: арендодатель / арендатор ----------------
s = load("extract.go")
pairs = [
    ("(?:поставщик|продавец|исполнитель|грузоотправитель|подрядчик)",
     "(?:поставщик|продавец|исполнитель|грузоотправитель|подрядчик|арендодатель|наймодатель)"),
    ("(?:покупатель|плательщик|заказчик|грузополучатель)",
     "(?:покупатель|плательщик|заказчик|грузополучатель|арендатор|наниматель)"),
]
changed = False
for a, b in pairs:
    if b in s:
        continue
    if a not in s:
        fail.append("extract.go: не найден шаблон сторон " + a)
        continue
    s = s.replace(a, b, 1)
    changed = True
if changed:
    save("extract.go", s)
    print("   extract.go пропатчен")
else:
    print("   extract.go уже пропатчен, пропускаю")

if fail:
    print("")
    print("!! НЕ ПРИМЕНЕНО:")
    for f in fail:
        print("   - " + f)
    sys.exit(2)
PYPATCH_EOF

echo
echo ">> Пересобираю бэкенд..."
cd "$ROOT"
docker compose build docflow-backend
docker compose up -d docflow-backend

echo
echo ">> Готово. Смотреть, что выбралось и почему:"
echo "   docker compose logs -f --tail=300 docflow-backend | grep -E 'табличная часть|оценки|grid diag'"
echo
echo ">> Откат:"
echo "   cp $BAK/*.go $REC/ && rm -f $REC/quality.go $REC/fieldfix.go $REC/quality_test.go && docker compose build docflow-backend && docker compose up -d docflow-backend"
