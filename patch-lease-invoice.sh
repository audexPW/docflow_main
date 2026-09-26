#!/usr/bin/env bash
# ============================================================================
#  DocFlow: разбор счёта-протокола аренды
#
#  По документу «СЧЕТ-ПРОТОКОЛ № У-000032 от 31.01.2023» (ООО «Эстурс» →
#  ООО «КофеВенд») нашлось четыре дефекта.
#
#  1) УНП сторон перепутаны местами. Это самое опасное: оба номера настоящие,
#     оба из этого документа, просто не у тех сторон — в 1С уходит контрагент
#     с чужим УНП, и глазами это не ловится.
#
#     Две причины сразу. Стороны здесь подписаны «Арендодатель» и «Арендатор»,
#     а в правилах были только поставщик/покупатель/плательщик — правила не
#     сработали вовсе, и поля отдались модели. И даже сработай они, УНП
#     искался только в строке метки, а в этом бланке он пятой строкой блока,
#     после адреса, расчётного счёта и банка.
#
#     Добавлены метки аренды, хранения и комиссии, а налоговый номер теперь
#     ищется во всём блоке стороны — от её метки до метки следующей.
#
#  2) В «Сумму с НДС» попадало 5,95 вместо 35,71. Под таблицей идут две
#     строки прописью подряд: «Сумма НДС: Пять белорусских рублей 95 копеек»
#     и «Всего: Тридцать пять белорусских рублей 71 копейка». Цифрами итога
#     в тексте нет. Добавлен разбор итога прописью — с метками «всего»,
#     «итого», «к оплате», но БЕЗ «сумма ндс», иначе налог снова уедет в итог.
#
#  3) Сумма НДС не извлекалась: разбор прописью знал только метку «в т.ч.
#     НДС», а здесь написано «Сумма НДС:». Метка добавлена.
#
#  4) В заголовке графы светился HTML-тег: «Ставка НДС,<br>%». Переносы
#     внутри ячейки surya отдаёт тегом, и он уходил в 1С вместе с подписью.
#
#  Запуск из /opt/docflow-deploy:  bash patch-lease-invoice.sh
# ============================================================================
set -euo pipefail
cd "$(dirname "$0")"

GO="docflow"
TS="$(date +%Y%m%d-%H%M%S)"

[ -d "$GO/internal/recognize" ] || { echo "Не вижу $GO/internal/recognize"; exit 1; }
grep -q "func VATFromWords" "$GO/internal/recognize/words.go" 2>/dev/null || {
  echo "Сначала нужен patch-required-fields.sh — нет words.go."; exit 1; }

for f in "$GO/internal/recognize/extract.go" "$GO/internal/recognize/words.go" \
         "$GO/internal/recognize/columns.go"; do
  cp "$f" "$f.bak.lease-$TS"
done

echo "→ 1/3 extract.go: стороны аренды и УНП по блоку"
cat > "$GO/internal/recognize/extract.go" <<'GOEOF'
package recognize

import (
	"regexp"
	"strings"

	"docflow/internal/domain"
)

var (
	reINN        = regexp.MustCompile(`(?i)ИНН[\s:]*?(\d{10}|\d{12})`)
	reUNP        = regexp.MustCompile(`(?i)УНП[\s:№]*?(\d{9})`) // Беларусь: 9 цифр
	reKPP        = regexp.MustCompile(`(?i)КПП[\s:]*?(\d{9})`)
	reDateDotted = regexp.MustCompile(`\b(\d{2}\.\d{2}\.\d{4})\b`)
	// Номер ищем сразу за названием документа: первый «№» в тексте сплошь и
	// рядом принадлежит договору-основанию или УНП, а не самому документу.
	reAnchoredNumber = regexp.MustCompile(`(?i)(счёт|счет|счёт-фактура|счет-фактура|эсчф|накладная|тн|ттн|акт|упд|товарный чек|договор|заказ)[^\n№N]{0,40}(?:№|N|No)[\s:]*([0-9A-Za-zА-Яа-я\-/]+)`)
	reDocNumber      = regexp.MustCompile(`(?i)(?:№|N|No)[\s:]*([0-9A-Za-zА-Яа-я\-/]+)`)
	reContractNumber = regexp.MustCompile(`(?i)(?:договор|контракт|основание|соглашени)\w*[^\n№N]{0,30}(?:№|N|No)[\s:]*([0-9A-Za-zА-Яа-я\-/]+)`)
	// Дата договора-основания: «от ДД.ММ.ГГГГ» в той же строке, что и его
	// номер, — иначе легко подхватить дату самого документа вместо неё.
	reContractDate = regexp.MustCompile(`(?i)(?:договор|контракт|основание|соглашени)\w*[^\n]{0,60}?от[\s:]*(\d{2}\.\d{2}\.\d{4})`)
	// Окно после якорного слова, в котором ищется сумма. Цифры в окне
	// разрешены намеренно: строка итогов часто состоит из трёх колонок
	// («31,49  6,29  37,78» — без НДС, НДС, с НДС), и нужная сумма стоит не
	// первой. Какое из чисел итоговое, решает extractTotal.
	reTotal = regexp.MustCompile(`(?i)(итого|всего к оплате|сумма к оплате|к оплате|всего)([^\n]{0,80})`)
	reMoney = regexp.MustCompile(`\b(\d[\d\s]*[.,]\d{2})\b`)

	// Стороны сделки. Контрагент — тот, кто выставил документ; организация —
	// фирма заказчика, на которую документ оформлен. Различаются по подписи
	// строки, потому что по одному только УНП понять, чей он, невозможно.
	// \b в Go RE2 — граница ASCII-слова, а кириллица для неё «не буква»:
	// с \b эти шаблоны не находили ни одной русской подписи. Поэтому граница
	// задана явно — началом строки или небуквенным символом.
	reCounterpartyLine = regexp.MustCompile(`(?im)^[^\n]{0,40}?(?:поставщик|продавец|исполнитель|грузоотправитель|подрядчик|арендодатель|наймодатель|ссудодатель|комитент|хранитель)[^\p{L}\n]{0,3}[:\-][ \t]*(.+)$`)
	reOrganizationLine = regexp.MustCompile(`(?im)^[^\n]{0,40}?(?:покупатель|плательщик|заказчик|грузополучатель|арендатор|наниматель|ссудополучатель|комиссионер|поклажедатель)[^\p{L}\n]{0,3}[:\-][ \t]*(.+)$`)
	reTaxLabel         = regexp.MustCompile(`(?i)(УНП|ИНН|УНН)`)
	// Метка любой стороны — граница блока реквизитов.
	reAnyPartyLabel = regexp.MustCompile(`(?im)^[^\n]{0,40}?(?:поставщик|продавец|исполнитель|грузоотправитель|подрядчик|арендодатель|наймодатель|ссудодатель|комитент|хранитель|покупатель|плательщик|заказчик|грузополучатель|арендатор|наниматель|ссудополучатель|комиссионер|поклажедатель)[^\p{L}\n]{0,3}[:\-]`)
	rePartyTail     = regexp.MustCompile(`(?i)(адрес|р/с|расч|тел\.|тел\s|банк|факс|e-mail|почтовый)`)

	// Сумма без НДС отдельным реквизитом: заказчик требует все три суммы —
	// без НДС, НДС и с НДС — независимо от того, есть ли они в бланке.
	// Не вычисляем вычитанием: «Итого» в разных формах бывает и с налогом, и
	// без, и ошибка тут молча уедет в проводку. Нет в документе — поле
	// остаётся пустым и подсвечивается бухгалтеру.
	reAmountNoVAT = regexp.MustCompile(`(?i)(?:сумма|стоимость|итого|всего)\s+без\s+ндс[^\d\n]{0,20}([\d\s]+[.,]\d{2})`)

	reVATAmount = regexp.MustCompile(`(?i)(?:в\s*т\.?\s*ч\.?\s*ндс|в\s+том\s+числе\s+ндс|сумма\s+ндс|ндс\s+итого|итого\s+ндс)\s*(?:\d{1,2}\s*%)?\s*[:\-]?\s*([\d\s]+[.,]\d{2})`)
	reCurrCode  = regexp.MustCompile(`\b(BYN|RUB|RUR|USD|EUR)\b`)
	// \w в Go RE2 — только ASCII, поэтому кириллические хвосты слов задаются явно.
	reCurrWord = regexp.MustCompile(`(?i)(бел\.?\s*руб|белорусск[а-яёА-ЯЁ]*\s+рубл|росс?ийск[а-яёА-ЯЁ]*\s+рубл|доллар[а-яёА-ЯЁ]*\s+США|евро)`)
)

// extractByRules достаёт частые реквизиты. Значения этого прохода имеют source
// "rule"; модель затем может их дополнить или перекрыть более точными.
func extractByRules(ocrText string) map[string]domain.Field {
	fields := make(map[string]domain.Field)

	// Стороны сделки разбираем первыми: от них зависит, чей налоговый
	// идентификатор считать идентификатором контрагента.
	cpName, cpTax := party(ocrText, reCounterpartyLine)
	orgName, orgTax := party(ocrText, reOrganizationLine)

	if cpName != "" {
		fields["counterparty"] = ruleField(cpName)
	}
	if orgName != "" {
		fields["organization"] = ruleField(orgName)
	}
	if orgTax != "" {
		fields["organization_unp"] = ruleField(orgTax)
	}

	if unp := pickTaxID(ocrText, reUNP, cpTax, orgTax); unp != "" {
		fields["unp"] = ruleField(unp)
	}
	if inn := pickTaxID(ocrText, reINN, cpTax, orgTax); inn != "" {
		fields["inn"] = ruleField(inn)
	}
	if m := reKPP.FindStringSubmatch(ocrText); m != nil {
		fields["kpp"] = ruleField(m[1])
	}
	if num, conf := extractDocNumber(ocrText); num != "" {
		fields["number"] = domain.Field{Value: num, Confidence: conf, Source: "rule"}
	}
	// Договор-основание — отдельно от номера самого документа. Раньше
	// извлекался только для того, чтобы исключить его из number, и никуда
	// дальше не уходил.
	if contract := contractNumber(ocrText); contract != "" {
		fields["contract_number"] = ruleField(contract)
		if date := contractDate(ocrText); date != "" {
			fields["contract_date"] = ruleField(date)
		}
	}
	if m := reDateDotted.FindStringSubmatch(ocrText); m != nil {
		fields["date"] = ruleField(m[1])
	}
	if amount, conf := extractTotal(ocrText); amount != "" {
		fields["total"] = domain.Field{Value: amount, Confidence: conf, Source: "rule"}
	} else if v := TotalFromWords(ocrText); v != "" {
		// Итог прописью под таблицей: «Всего: Тридцать пять белорусских
		// рублей 71 копейка». Без этого в поле итога попадала сумма НДС,
		// напечатанная строкой выше.
		fields["total"] = ruleField(v)
	}
	if m := reVATAmount.FindStringSubmatch(ocrText); m != nil {
		fields["vat_amount"] = ruleField(normalizeMoney(m[1]))
	} else if v := VATFromWords(ocrText); v != "" {
		// Сумма НДС прописью под таблицей: «в т.ч. НДС: Семь рублей 26
		// копеек». В самой таблице отдельной графы с НДС может не быть вовсе —
		// заказчик показал именно такой счёт, и поле оставалось пустым, хотя
		// значение в документе есть, просто написано буквами.
		fields["vat_amount"] = ruleField(v)
	}
	if m := reAmountNoVAT.FindStringSubmatch(ocrText); m != nil {
		fields["amount_no_vat"] = ruleField(normalizeMoney(m[1]))
	}
	if cur := extractCurrency(ocrText); cur != "" {
		fields["currency"] = ruleField(cur)
	}

	return fields
}

// party достаёт наименование стороны и её налоговый номер из строки вида
// «Поставщик: ООО "Ромашка", УНП 191234567».
func party(text string, re *regexp.Regexp) (name, taxID string) {
	loc := re.FindStringSubmatchIndex(text)
	if loc == nil {
		return "", ""
	}
	tail := strings.TrimSpace(text[loc[2]:loc[3]])

	// Налоговый номер ищем во всём блоке стороны, а не только в её строке.
	// В счёте-протоколе аренды блок занимает пять строк: название, адрес,
	// расчётный счёт, банк, и только потом УНП. Пока смотрели одну строку,
	// номер не находился, поля отдавались модели — и она приписывала
	// арендодателю УНП арендатора, а тому — чужой. Ошибка тихая: оба номера
	// настоящие, оба из этого документа, просто не у тех сторон.
	// Блок начинаем с самой стороны, а не после совпадения метки: в обычном
	// счёте УНП стоит в той же строке, и старт после match его отрезал.
	block := partyBlock(text, loc[2])
	if t := reUNP.FindStringSubmatch(block); t != nil {
		taxID = t[1]
	} else if t := reINN.FindStringSubmatch(block); t != nil {
		taxID = t[1]
	}

	// Наименование — до налогового номера или до адреса/реквизитов банка.
	if loc := reTaxLabel.FindStringIndex(tail); loc != nil {
		tail = tail[:loc[0]]
	}
	if loc := rePartyTail.FindStringIndex(tail); loc != nil {
		tail = tail[:loc[0]]
	}
	name = strings.TrimSpace(strings.Trim(strings.TrimSpace(tail), ",;:-"))
	if len([]rune(name)) < 3 {
		name = ""
	}
	return name, taxID
}

// pickTaxID выбирает налоговый номер контрагента. Если строка поставщика
// подписана — берём номер оттуда. Иначе берём первый номер в тексте, но
// пропускаем тот, что уже опознан как номер собственной организации: иначе в
// 1С контрагентом станет сам заказчик.
// partyBlock — реквизиты одной стороны: от её метки до метки следующей.
// Ограничение по длине и по числу строк не даёт блоку утечь в подписи и в
// табличную часть, где встречаются посторонние девятизначные числа.
func partyBlock(text string, from int) string {
	rest := text[from:]
	if len(rest) > 600 {
		rest = rest[:600]
	}
	if loc := reAnyPartyLabel.FindStringIndex(rest); loc != nil {
		rest = rest[:loc[0]]
	}
	lines := strings.SplitN(rest, "\n", 8)
	if len(lines) > 7 {
		lines = lines[:7]
	}
	return strings.Join(lines, "\n")
}

func pickTaxID(text string, re *regexp.Regexp, cpTax, orgTax string) string {
	if cpTax != "" && matchesShape(re, cpTax) {
		return cpTax
	}
	for _, m := range re.FindAllStringSubmatch(text, -1) {
		if orgTax != "" && m[1] == orgTax {
			continue
		}
		return m[1]
	}
	return ""
}

// matchesShape проверяет, что номер подходит под формат этой регулярки
// (УНП — 9 цифр, ИНН — 10 или 12): один и тот же номер не может быть обоими.
func matchesShape(re *regexp.Regexp, id string) bool {
	switch re {
	case reUNP:
		return len(id) == 9
	case reINN:
		return len(id) == 10 || len(id) == 12
	}
	return false
}

func extractCurrency(text string) string {
	if m := reCurrCode.FindStringSubmatch(text); m != nil {
		if strings.EqualFold(m[1], "RUR") {
			return "RUB"
		}
		return strings.ToUpper(m[1])
	}
	m := reCurrWord.FindStringSubmatch(text)
	if m == nil {
		return ""
	}
	w := strings.ToLower(m[1])
	switch {
	case strings.HasPrefix(w, "бел"):
		return "BYN"
	case strings.HasPrefix(w, "рос"), strings.HasPrefix(w, "росс"):
		return "RUB"
	case strings.HasPrefix(w, "доллар"):
		return "USD"
	case strings.HasPrefix(w, "евро"):
		return "EUR"
	}
	return ""
}

// extractDocNumber возвращает номер документа и уверенность в нём.
// Номер, найденный сразу после названия документа, надёжен. Номер, взятый
// «первым попавшимся №», — догадка, и она помечается низкой уверенностью,
// чтобы оператор увидел поле как требующее проверки, а не как факт.
func extractDocNumber(text string) (string, float64) {
	contract := contractNumber(text)

	// Проходим все привязанные к названию номера: если документ — не договор,
	// номер договора-основания пропускаем.
	var anchoredContract string
	for _, m := range reAnchoredNumber.FindAllStringSubmatch(text, -1) {
		num := strings.Trim(m[2], "-/")
		if num == "" {
			continue
		}
		if contract != "" && num == contract {
			anchoredContract = num
			continue
		}
		return num, ruleConfidence
	}
	// Единственный найденный номер — договорный: значит это и есть договор.
	if anchoredContract != "" {
		return anchoredContract, ruleConfidence
	}

	for _, m := range reDocNumber.FindAllStringSubmatch(text, -1) {
		num := strings.Trim(m[1], "-/")
		if num == "" || (contract != "" && num == contract) {
			continue
		}
		return num, guessConfidence
	}
	return "", 0
}

// contractNumber возвращает номер договора-основания, если он в тексте есть.
// Он часто напечатан выше номера самого документа и раньше подхватывался
// вместо него.
func contractNumber(text string) string {
	if m := reContractNumber.FindStringSubmatch(text); m != nil {
		return strings.Trim(m[1], "-/")
	}
	return ""
}

// contractDate возвращает дату договора-основания («от ДД.ММ.ГГГГ» рядом с
// его номером). Раньше не извлекалась вовсе — 1С получала номер договора
// без даты, этого недостаточно для реквизита «Основание».
func contractDate(text string) string {
	if m := reContractDate.FindStringSubmatch(text); m != nil {
		return m[1]
	}
	return ""
}

// extractTotal ищет итоговую сумму по якорным словам («итого», «всего к
// оплате»). Если якоря нет, возвращается максимальная сумма в тексте — но это
// именно догадка: в документе максимальной может оказаться сумма прописью,
// лимит по договору или сумма прошлого периода. Такое значение отдаётся с
// низкой уверенностью, и оператор увидит его как требующее проверки.
func extractTotal(text string) (string, float64) {
	for _, m := range reTotal.FindAllStringSubmatch(text, -1) {
		window := m[2]
		lowerWindow := strings.ToLower(window)

		// Если ближайшее число после якоря сразу подписано как НДС —
		// это не итоговая сумма, якорь пропускаем целиком.
		if loc := reMoney.FindStringIndex(window); loc != nil {
			pre := lowerWindow[:loc[0]]
			if strings.Contains(pre, "ндс") || strings.Contains(pre, "налог") {
				continue
			}
		}

		// Строка вида «Итого: 31,49 6,29 37,78» — три колонки (сумма,
		// НДС, сумма с НДС). Итоговая сумма к оплате всегда наибольшая
		// из них, поэтому берём максимум, а не первое число.
		var best string
		var bestVal float64
		for _, mv := range reMoney.FindAllString(window, -1) {
			val := moneyToFloat(mv)
			if val > bestVal {
				bestVal = val
				best = mv
			}
		}
		if best != "" {
			return normalizeMoney(best), ruleConfidence
		}
	}

	var best string
	var bestVal float64
	for _, m := range reMoney.FindAllStringSubmatch(text, -1) {
		val := moneyToFloat(m[1])
		if val > bestVal {
			bestVal = val
			best = m[1]
		}
	}
	if best == "" {
		return "", 0
	}
	return normalizeMoney(best), guessConfidence
}

func normalizeMoney(s string) string {
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, ",", ".")
	return s
}

// moneyToFloat даёт грубую числовую оценку строки суммы. Точность не важна —
// значение нужно только чтобы сравнить кандидатов и выбрать максимальный.
func moneyToFloat(s string) float64 {
	s = normalizeMoney(s)
	var f float64
	for i := 0; i < len(s); i++ {
		if c := s[i]; c >= '0' && c <= '9' {
			f = f*10 + float64(c-'0')
		}
	}
	return f
}

// Уверенность значений, найденных правилами.
//
// ruleConfidence — значение стоит рядом со своим якорем («Итого: …», «Счёт
// №…»), ошибиться сложно. guessConfidence — значение выведено эвристикой без
// якоря; оно может быть верным, но проверять его обязательно, поэтому
// интерфейс подсвечивает такие поля, а в 1С они уходят уже видимыми как
// неточные.
const (
	ruleConfidence  = 0.7
	guessConfidence = 0.3
)

func ruleField(v string) domain.Field {
	return domain.Field{Value: strings.TrimSpace(v), Confidence: ruleConfidence, Source: "rule"}
}
GOEOF
echo "   ok"

echo "→ 2/3 words.go: итог и НДС прописью"
cat > "$GO/internal/recognize/words.go" <<'GOEOF'
package recognize

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Суммы прописью.
//
// В белорусских бланках итог и НДС часто печатаются под таблицей словами:
// «К оплате: Сорок четыре рубля 00 копеек, в т.ч. НДС: Семь рублей 26 копеек».
// Цифрами в самой таблице сумма НДС при этом может отсутствовать вовсе —
// заказчик показал именно такой счёт. Раньше поле оставалось пустым, хотя
// значение в документе есть, просто написано буквами.

var wordUnits = map[string]int{
	"ноль": 0, "нуль": 0,
	"один": 1, "одна": 1, "два": 2, "две": 2, "три": 3, "четыре": 4,
	"пять": 5, "шесть": 6, "семь": 7, "восемь": 8, "девять": 9,
	"десять": 10, "одиннадцать": 11, "двенадцать": 12, "тринадцать": 13,
	"четырнадцать": 14, "пятнадцать": 15, "шестнадцать": 16,
	"семнадцать": 17, "восемнадцать": 18, "девятнадцать": 19,
	"двадцать": 20, "тридцать": 30, "сорок": 40, "пятьдесят": 50,
	"шестьдесят": 60, "семьдесят": 70, "восемьдесят": 80, "девяносто": 90,
	"сто": 100, "двести": 200, "триста": 300, "четыреста": 400,
	"пятьсот": 500, "шестьсот": 600, "семьсот": 700,
	"восемьсот": 800, "девятьсот": 900,
}

// wordScales — множители разрядов. «Тысяча» и «миллион» во всех падежах и
// родах, как их печатают в бланках.
var wordScales = map[string]int{
	"тысяча": 1000, "тысячи": 1000, "тысяч": 1000, "тысячу": 1000,
	"миллион": 1000000, "миллиона": 1000000, "миллионов": 1000000,
}

// wordSkip — слова, которые встречаются внутри суммы прописью, но на значение
// не влияют: «Тридцать шесть белорусских рублей 78 копеек». Список закрытый и
// короткий намеренно — пропускать всё незнакомое нельзя, иначе «сорок мяу
// четыре» разберётся как 44 и уйдёт в бухгалтерию.
var wordSkip = map[string]bool{
	"белорусских": true, "белорусский": true, "белорусские": true,
	"российских": true, "российский": true, "российские": true,
	"и": true,
}

// reMoneyInWords — сумма прописью: рублёвая часть словами, копейки цифрами или
// словами. Копейки в бланках чаще цифрами («Семь рублей 26 копеек»), но
// встречается и полностью словесная запись.
var reMoneyInWords = regexp.MustCompile(
	`(?i)([а-яё]+(?:[\s-]+[а-яё]+){0,9})\s+рубл\S*\s*(\d{1,2}|[а-яё]+(?:\s+[а-яё]+)?)\s*коп\S*`)

// moneyWordsTail — сумма прописью, общий хвост для меток ниже.
const moneyWordsTail = `([а-яё]+(?:[\s-]+[а-яё]+){0,9}\s+рубл\S*\s*(?:\d{1,2}|[а-яё]+(?:\s+[а-яё]+)?)\s*коп\S*)`

// reVATWords — сумма НДС прописью. Меток несколько: «в т.ч. НДС» пишут в
// счетах, «Сумма НДС:» — в счетах-протоколах, где под таблицей идут две
// строки подряд, налог и итог.
var reVATWords = regexp.MustCompile(
	`(?i)(?:в\s*т\.?\s*ч\.?\s*ндс|в\s+том\s+числе\s+ндс|в\s+т\.?\s*ч\.?\s+ндс|сумма\s+ндс)\s*[:\-]?\s*` +
		moneyWordsTail)

// reTotalWords — итог прописью: «Всего: Тридцать пять белорусских рублей 71
// копейка». Метка «сумма ндс» сюда намеренно не входит, иначе налог уедет в
// поле итога — ровно это и произошло на счёте-протоколе, где в «Сумму с НДС»
// попало 5,95 вместо 35,71.
var reTotalWords = regexp.MustCompile(
	`(?i)(?:всего\s+к\s+оплате|итого\s+к\s+оплате|к\s+оплате|всего|итого)\s*(?:с\s+ндс)?\s*[:\-]\s*` +
		moneyWordsTail)

// parseRussianInt переводит числительное прописью в число.
// Возвращает -1, если хотя бы одно слово не опознано: частичный разбор здесь
// опаснее отказа — «сорок» вместо «сорок четыре» уйдёт в бухгалтерию молча.
func parseRussianInt(s string) int {
	words := strings.Fields(strings.ToLower(strings.ReplaceAll(s, "-", " ")))
	if len(words) == 0 {
		return -1
	}
	total, group, seen := 0, 0, false
	for _, w := range words {
		w = strings.Trim(w, ".,:;()")
		if w == "" {
			continue
		}
		if wordSkip[w] {
			continue
		}
		if v, ok := wordUnits[w]; ok {
			group += v
			seen = true
			continue
		}
		if mul, ok := wordScales[w]; ok {
			if group == 0 {
				group = 1 // «тысяча рублей» без числительного перед ней
			}
			total += group * mul
			group = 0
			seen = true
			continue
		}
		return -1
	}
	if !seen {
		return -1
	}
	return total + group
}

// ParseMoneyWords разбирает сумму прописью в строку вида «44.00».
// Пустая строка — разобрать не удалось.
func ParseMoneyWords(s string) string {
	m := reMoneyInWords.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	rub := parseRussianInt(m[1])
	if rub < 0 {
		return ""
	}

	kop := 0
	k := strings.TrimSpace(m[2])
	if v, err := strconv.Atoi(k); err == nil {
		kop = v
	} else {
		kop = parseRussianInt(k)
	}
	if kop < 0 || kop > 99 {
		return ""
	}
	return fmt.Sprintf("%d.%02d", rub, kop)
}

// VATFromWords достаёт сумму НДС, напечатанную под таблицей прописью.
func VATFromWords(text string) string {
	m := reVATWords.FindStringSubmatch(text)
	if m == nil {
		return ""
	}
	return ParseMoneyWords(m[1])
}

// TotalFromWords достаёт итоговую сумму, напечатанную прописью.
func TotalFromWords(text string) string {
	m := reTotalWords.FindStringSubmatch(text)
	if m == nil {
		return ""
	}
	return ParseMoneyWords(m[1])
}
GOEOF
echo "   ok"

echo "→ 3/3 columns.go: чистка HTML-тегов из ячеек"
cat > "$GO/internal/recognize/columns.go" <<'GOEOF'
package recognize

import (
	"math"
	"regexp"
	"sort"
	"strings"

	"docflow/internal/domain"
)

// Табличная часть по координатам слов — проекция колонок.
//
// Почему не по шапке и не моделью. Разбор по шапке (FreeTableFromGrid) требует
// узнать в одной строке хотя бы две подписи граф. В белорусских бланках шапка
// почти всегда в две-три строки: «Сумма» на одной, «без НДС, руб.» на другой, а
// ниже ещё ряд с номерами граф «1 2 3 4 5». Одной строки, где видно две
// знакомые подписи, там попросту нет — разбор отваливается, и таблица
// достаётся модели, которая сводит её к шести типовым колонкам. Отсюда и
// «Сумма с НДС» в графе «Сумма», и ставка 20 в графе «НДС».
//
// Здесь колонки берутся из геометрии страницы: по вертикальным полосам, где ни
// в одной строке таблицы нет ни одного слова. Это и есть границы граф, как их
// видит человек. Состав колонок при этом не навязан: сколько полос нашлось —
// столько граф и уйдёт в интерфейс и в 1С, с подписями из шапки оригинала.

var (
	// Ряд номеров граф под шапкой: «1 2 3 4 5». Данными не является.
	reColNumbering = regexp.MustCompile(`^\d{1,2}$`)
	// Мусор от surya: формулы приходят разметкой LaTeX внутри <math>.
	reMathTag = regexp.MustCompile(`(?s)<math[^>]*>.*?</math>`)
	// Переносы строк внутри ячейки surya отдаёт HTML-тегом. В заголовке графы
	// он выглядел как «Ставка НДС,<br>%» и уходил в 1С вместе с подписью.
	reHTMLTag   = regexp.MustCompile(`(?i)<\s*/?\s*(br|p|div|span|td|tr|th|table|tbody)[^>]*>`)
	reTexMacro  = regexp.MustCompile(`\\[a-zA-Z]+\s*`)
	reTexBraces = regexp.MustCompile(`[{}$]`)
)

// StripMath убирает разметку формул из распознанного текста. Surya отдаёт
// «№ п/п» как <math>N_{\overline{2}}</math>, и такая подпись уезжает в
// заголовок колонки, а оттуда — в 1С.
// reNoSign — как OCR читает знак номера: «NΩ», «N°», «No», «Ne», «N2».
var reNoSign = regexp.MustCompile(`(?i)^(n[oeº°ΩΩ]|n°2|№)$`)

// normalizeNoSign приводит эти написания к «№». Заменяем только когда слово
// целиком совпадает: внутри текста «no» — обычное слово, и трогать его нельзя.
func normalizeNoSign(s string) string {
	if reNoSign.MatchString(s) {
		return "№"
	}
	return s
}

func StripMath(s string) string {
	if !strings.ContainsAny(s, "<\\{$") {
		return s
	}
	s = reMathTag.ReplaceAllString(s, " № ")
	s = reHTMLTag.ReplaceAllString(s, " ")
	s = reTexMacro.ReplaceAllString(s, " ")
	s = reTexBraces.ReplaceAllString(s, " ")
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}

// token — слово с координатами; единица, которой оперирует проекция.
type token struct {
	Text string
	X0   float64
	Y0   float64
	X1   float64
	Y1   float64
}

// tokenRow — строка страницы: слова, перекрывающиеся по вертикали.
type tokenRow struct {
	Toks []token
	Top  float64
	Bot  float64
}

// TableFromWords собирает табличную часть из слов с координатами.
// Возвращает nil, если полосу таблицы на странице найти не удалось — тогда
// работают прежние пути разбора.
func TableFromWords(words []wordBox) *domain.FreeTable {
	toks := make([]token, 0, len(words))
	for _, w := range words {
		t := normalizeNoSign(StripMath(strings.TrimSpace(w.Text)))
		if t == "" {
			continue
		}
		if w.X1 <= w.X0 || w.Y1 <= w.Y0 {
			continue
		}
		toks = append(toks, token{Text: t, X0: w.X0, Y0: w.Y0, X1: w.X1, Y1: w.Y1})
	}
	if len(toks) < 8 {
		return nil
	}

	rows := groupTokenRows(toks)
	if len(rows) < 3 {
		return nil
	}

	head, first, last := findTableBand(rows)
	if head < 0 {
		return nil
	}

	// Границы колонок считаем ТОЛЬКО по строкам данных.
	//
	// Раньше в расчёт шла вся полоса вместе с шапкой и «Итого», и это
	// склеивало графы: подпись «Наименование товара» тянется через зазор
	// между «Ед. изм.» и «Кол-во», а «Всего отпущено на сумму» в итоговой
	// строке перекрывает сразу половину граф. Достаточно одного такого слова,
	// чтобы две колонки слились навсегда — на счёте №112 из девяти граф
	// оставалось четыре. В строках данных значение всегда стоит внутри своей
	// клетки и чужих зазоров не пересекает.
	bands := columnBands(dataRows(rows[first : last+1]))
	if len(bands) < 2 {
		// Данных не хватило (одна строка, вся из одного слова) — считаем по
		// всей полосе, как раньше: хуже, но лучше, чем отказ от таблицы.
		bands = columnBands(rows[head : last+1])
	}
	if len(bands) < 2 {
		return nil
	}

	// Шапку набираем вверх от первой строки данных, а не вниз от найденного
	// заголовка. Поиск сверху цеплял строки документа, лежащие над таблицей:
	// «Дополнение: Договор аренды№49 от17.03.20г ... за октябрь 2023г» и
	// «руб.» разрезались по графам и приклеивались к настоящим подписям —
	// в интерфейсе выходило «2023г Кол-во» и «руб. Сумма с НДС».
	cols := headerTitles(headerRowsAbove(rows, head, first, bands), bands)
	ft := &domain.FreeTable{Columns: cols, Roles: rolesFor(cols), Source: "columns"}

	// pending — первая половина перенесённого наименования, ждущая строки с
	// числами, к которой она относится.
	var pending []string
	for i := first; i <= last; i++ {
		cells := rowCells(rows[i], bands)
		joined := strings.TrimSpace(strings.Join(cells, " "))
		if joined == "" {
			continue
		}
		if isNumberingRow(cells) {
			continue
		}
		if reTotalsRow.MatchString(joined) {
			if len(ft.Totals) == 0 {
				ft.Totals = cells
			}
			continue
		}
		if isJunkRow(cells, ft.Roles) {
			// Обрывок вроде одинокого «01» в графе единиц измерения. На
			// перекошенном скане такие огрызки строк ловятся регулярно и
			// висят в таблице пустыми позициями.
			continue
		}
		if isNameContinuation(cells, ft.Roles) {
			// Перенос наименования. В бланках он бывает и вниз, и вверх:
			// «Возмещение части эксплуатационных» стоит отдельной строкой, а
			// цифры выровнены по её второй половине — «расходов по
			// обслуживанию здания». Если позиция уже есть, дописываем к ней;
			// если ещё нет, придерживаем текст до первой строки с числами.
			if len(ft.Rows) > 0 {
				appendToFreeName(ft.Rows[len(ft.Rows)-1], cells, ft.Roles)
			} else {
				pending = mergeCells(pending, cells)
			}
			continue
		}
		if !hasLetters(joined) && !reNumberish.MatchString(joined) {
			continue
		}
		if pending != nil {
			cells = prependCells(pending, cells)
			pending = nil
		}
		ft.Rows = append(ft.Rows, cells)
	}

	ft.Rows = dropDegenerateRows(ft.Rows)
	if len(ft.Rows) == 0 {
		return nil
	}
	return ft
}

// groupTokenRows собирает слова в строки по вертикальному перекрытию. Порог по
// перекрытию, а не по расстоянию между верхами боксов: на скане с наклоном
// строка уезжает, и сравнение верхов рвёт её пополам.
func groupTokenRows(toks []token) []tokenRow {
	sorted := make([]token, len(toks))
	copy(sorted, toks)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Y0 == sorted[j].Y0 {
			return sorted[i].X0 < sorted[j].X0
		}
		return sorted[i].Y0 < sorted[j].Y0
	})

	var rows []tokenRow
	for _, t := range sorted {
		placed := false
		for i := range rows {
			h := math.Max(rows[i].Bot-rows[i].Top, 1)
			ov := math.Min(rows[i].Bot, t.Y1) - math.Max(rows[i].Top, t.Y0)
			if ov > h*0.35 {
				rows[i].Toks = append(rows[i].Toks, t)
				rows[i].Top = math.Min(rows[i].Top, t.Y0)
				rows[i].Bot = math.Max(rows[i].Bot, t.Y1)
				placed = true
				break
			}
		}
		if !placed {
			rows = append(rows, tokenRow{Toks: []token{t}, Top: t.Y0, Bot: t.Y1})
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Top < rows[j].Top })
	for i := range rows {
		r := rows[i].Toks
		sort.SliceStable(r, func(a, b int) bool { return r[a].X0 < r[b].X0 })
	}
	return rows
}

// findTableBand находит шапку и границы полосы данных.
//
// Шапка ищется как окно из одной-трёх подряд идущих строк, в котором суммарно
// опознаётся не меньше двух подписей граф. Окно, а не строка: подпись «Сумма
// без НДС, руб.» переносится, и в отдельно взятой строке видно только «Сумма».
//
// Возвращает (индекс первой строки шапки, индекс первой строки данных,
// индекс последней строки данных). head < 0 — таблицы нет.
func findTableBand(rows []tokenRow) (head, first, last int) {
	head = -1
	for i := range rows {
		for w := 1; w <= 3 && i+w <= len(rows); w++ {
			known, cells := 0, 0
			seen := map[string]bool{}
			for _, r := range rows[i : i+w] {
				for _, t := range r.Toks {
					cells++
					role := guessRole(t.Text)
					if role != "" && role != "other" && !seen[role] {
						seen[role] = true
						known++
					}
				}
			}
			// Две разные знакомые подписи и не меньше трёх слов — это шапка,
			// а не случайное «Сумма НДС» в тексте договора.
			if known >= 2 && cells >= 3 {
				head = i
				first = i + w
				break
			}
		}
		if head >= 0 {
			break
		}
	}
	if head < 0 || first >= len(rows) {
		return -1, 0, 0
	}

	// Шапка почти всегда переносится: «Ставка» / «НДС %», «Сумма» / «с НДС».
	// Дотягиваем её вниз, пока строки не содержат ни одного числа — позиция
	// без единой цифры в бухгалтерской таблице не встречается.
	for first < len(rows)-1 && isHeaderContinuation(rows[first]) {
		first++
	}

	last = first - 1
	for i := first; i < len(rows); i++ {
		joined := rowText(rows[i])
		// «Итого» проверяем раньше стоп-слов: reTableStop содержит и «итого», и
		// «всего», и без этой перестановки строка итога обрывала полосу, так и
		// не попав в таблицу.
		if reTotalsRow.MatchString(joined) {
			last = i
			break
		}
		if reTableStop.MatchString(joined) {
			break
		}
		// Полоса кончилась: пошёл сплошной текст без чисел и без структуры.
		if len(rows[i].Toks) < 2 && !reNumberish.MatchString(joined) {
			// одиночная строка-хвост наименования — не обрываем
			if i > first && len([]rune(joined)) > 60 {
				break
			}
		}
		last = i
	}
	if last < first {
		return -1, 0, 0
	}
	return head, first, last
}

// isHeaderContinuation — перенос подписей граф на следующую строку. Отличаем
// от позиции по отсутствию чисел: «НДС %» и «с НДС» цифр не несут, а любая
// позиция таблицы несёт хотя бы количество или сумму.
func isHeaderContinuation(r tokenRow) bool {
	if len(r.Toks) == 0 {
		return false
	}
	total := 0
	for _, t := range r.Toks {
		if reNumberish.MatchString(t.Text) {
			return false
		}
		n := len([]rune(t.Text))
		total += n + 1
		// Отсутствия цифр мало: первая половина перенесённого наименования
		// («Возмещение части эксплуатационных») тоже без цифр и раньше
		// уезжала в шапку, а вторая оставалась позицией с обрезанным
		// названием. Подписи граф короткие — «Ед. изм.», «НДС %», «с НДС»;
		// строка наименования длиннее и по отдельному слову, и целиком.
		if n > 14 {
			return false
		}
	}
	return total <= 40
}

func rowText(r tokenRow) string {
	parts := make([]string, 0, len(r.Toks))
	for _, t := range r.Toks {
		parts = append(parts, t.Text)
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

// columnBands находит границы граф: проецируем боксы всех слов полосы на ось X
// и режем по зазорам, где нет ни одного слова. Ширина зазора — от средней
// ширины символа в полосе, чтобы пробел между словами внутри графы не сошёл за
// границу колонки.
func columnBands(rows []tokenRow) [][2]float64 {
	type seg struct{ a, b float64 }
	var segs []seg
	var widths []float64
	minX, maxX := math.Inf(1), math.Inf(-1)
	for _, r := range rows {
		for _, t := range r.Toks {
			segs = append(segs, seg{t.X0, t.X1})
			if n := len([]rune(t.Text)); n > 0 {
				widths = append(widths, (t.X1-t.X0)/float64(n))
			}
			minX = math.Min(minX, t.X0)
			maxX = math.Max(maxX, t.X1)
		}
	}
	if len(segs) < 4 || math.IsInf(minX, 1) {
		return nil
	}
	charW := median(widths)
	if charW <= 0 {
		charW = 5
	}
	// Порог зазора намеренно маленький. Границей графы становится не всякий
	// широкий разрыв, а только вертикальная полоса, свободная ВО ВСЕХ строках
	// полосы сразу: пробел между словами внутри наименования в одной строке
	// перекрыт длинным наименованием в другой, и полосой не станет. Большой
	// порог, наоборот, склеивает соседние узкие графы — «Ед. изм.» с «Кол-во».
	gapMin := math.Max(charW*0.35, 2)

	sort.Slice(segs, func(i, j int) bool { return segs[i].a < segs[j].a })
	var merged []seg
	cur := segs[0]
	for _, s := range segs[1:] {
		if s.a <= cur.b+gapMin {
			if s.b > cur.b {
				cur.b = s.b
			}
			continue
		}
		merged = append(merged, cur)
		cur = s
	}
	merged = append(merged, cur)

	bands := make([][2]float64, 0, len(merged))
	for i, m := range merged {
		lo := m.a
		hi := m.b
		// Границу между графами ставим по середине зазора: слово, чуть
		// вылезшее за свою графу, останется в ней.
		if i > 0 {
			lo = (merged[i-1].b + m.a) / 2
		} else {
			lo = m.a - charW
		}
		if i < len(merged)-1 {
			hi = (m.b + merged[i+1].a) / 2
		} else {
			hi = m.b + charW
		}
		bands = append(bands, [2]float64{lo, hi})
	}
	return bands
}

// dataRows отбрасывает из полосы итоговую строку и строки-переносы
// наименования: границы граф считаются по настоящим позициям, где каждое
// значение стоит в своей клетке.
func dataRows(rows []tokenRow) []tokenRow {
	out := make([]tokenRow, 0, len(rows))
	for _, r := range rows {
		joined := rowText(r)
		if joined == "" || reTotalsRow.MatchString(joined) {
			continue
		}
		// Строка без единого числа — это перенос наименования, а не позиция.
		if !reNumberish.MatchString(joined) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// bandOf возвращает индекс графы, в которую попадает центр слова.
func bandOf(t token, bands [][2]float64) int {
	c := (t.X0 + t.X1) / 2
	for i, b := range bands {
		if c >= b[0] && c < b[1] {
			return i
		}
	}
	if c < bands[0][0] {
		return 0
	}
	return len(bands) - 1
}

func rowCells(r tokenRow, bands [][2]float64) []string {
	buf := make([][]string, len(bands))
	for _, t := range r.Toks {
		// Бокс, растянутый через несколько граф, режем по словам. Surya
		// нередко отдаёт всю левую часть шапки одной строкой — «№
		// Наименование товара Ед. изм. Кол-во Цена». Целиком такой бокс
		// уходил в графу наименования, и роли пяти колонок определялись по
		// одной подписи: «Кол-во» в ней срабатывало первым, поэтому графа
		// наименования уезжала в количество.
		for i, part := range splitAcrossBands(t, bands) {
			if part != "" {
				buf[i] = append(buf[i], part)
			}
		}
	}
	out := make([]string, len(bands))
	for i, parts := range buf {
		out[i] = strings.TrimSpace(strings.Join(parts, " "))
	}
	return out
}

// splitAcrossBands раскладывает слова одного бокса по графам. Координата слова
// оценивается пропорционально его длине в символах: точных границ внутри бокса
// движок не даёт, но графы шире одного символа, и такой оценки достаточно.
// Бокс, целиком лежащий в одной графе, возвращается без изменений.
func splitAcrossBands(t token, bands [][2]float64) []string {
	out := make([]string, len(bands))
	words := strings.Fields(t.Text)
	if len(words) < 2 {
		out[bandOf(t, bands)] = t.Text
		return out
	}
	// Бокс не выходит за свою графу — резать нечего.
	i0, i1 := bandIndexAt(t.X0, bands), bandIndexAt(t.X1, bands)
	if i0 == i1 {
		out[i0] = t.Text
		return out
	}

	width := t.X1 - t.X0
	total := 0
	for _, w := range words {
		total += len([]rune(w))
	}
	total += len(words) - 1 // пробелы
	if total <= 0 {
		out[bandOf(t, bands)] = t.Text
		return out
	}
	per := width / float64(total)

	cur := t.X0
	parts := make([][]string, len(bands))
	for _, w := range words {
		n := float64(len([]rune(w)))
		center := cur + n*per/2
		idx := bandIndexAt(center, bands)
		parts[idx] = append(parts[idx], w)
		cur += (n + 1) * per
	}
	for i := range parts {
		out[i] = strings.Join(parts[i], " ")
	}
	return out
}

// bandIndexAt — номер графы, в которую попадает координата x.
func bandIndexAt(x float64, bands [][2]float64) int {
	for i, b := range bands {
		if x >= b[0] && x < b[1] {
			return i
		}
	}
	if x < bands[0][0] {
		return 0
	}
	return len(bands) - 1
}

// headerTitles склеивает подписи граф из всех строк шапки. Многострочная шапка
// («Сумма» / «без НДС, руб.») собирается в один заголовок в порядке сверху вниз.
func headerTitles(headRows []tokenRow, bands [][2]float64) []string {
	parts := make([][]string, len(bands))
	for _, r := range headRows {
		cells := rowCells(r, bands)
		if isNumberingRow(cells) {
			continue // ряд «1 2 3 4 5» под шапкой — не подпись
		}
		for i, v := range cells {
			if v != "" {
				parts[i] = append(parts[i], v)
			}
		}
	}
	out := make([]string, len(bands))
	for i := range parts {
		out[i] = strings.TrimSpace(strings.Join(parts[i], " "))
	}
	return out
}

// headerRowsAbove отбирает строки шапки из полосы над данными.
//
// Именно отбирает, а не обходит с обрывом: обход вверх до первой неподходящей
// строки терял всю шапку целиком, если ближайшая к данным строка почему-то не
// проходила отбор. Здесь берётся тот же диапазон, что и раньше, и из него
// выбрасываются строки, похожие на текст документа. Если выбросить пришлось
// всё — возвращаем диапазон как есть: лишние слова в заголовке неприятны, но
// заголовки без названий хуже.
func headerRowsAbove(rows []tokenRow, head, first int, bands [][2]float64) []tokenRow {
	lo := head
	if lo < 0 {
		lo = 0
	}
	if first > len(rows) {
		first = len(rows)
	}
	if lo >= first {
		return nil
	}
	span := rows[lo:first]
	picked := make([]tokenRow, 0, len(span))
	started := false
	for _, r := range span {
		wide, narrow := headerRowKind(r, bands)
		if wide {
			picked = append(picked, r)
			started = true
			continue
		}
		// Строка, укладывающаяся в одну графу, идёт в шапку только после того,
		// как началась сама шапка. Так «НДС %» под «Ставка» и «с НДС» под
		// «Сумма» остаются подписями, а «руб.» над правым краем таблицы —
		// нет: он стоит выше первой строки заголовков.
		if narrow && started {
			picked = append(picked, r)
		}
	}
	if len(picked) == 0 {
		return span
	}
	// Больше трёх рядов подписей не бывает; берём ближайшие к данным.
	if len(picked) > 3 {
		picked = picked[len(picked)-3:]
	}
	return picked
}

// headerRowKind классифицирует строку над данными.
//
// wide — полноценный ряд подписей: раскладывается минимум по двум графам.
// narrow — обрывок подписи в одной графе («НДС %», «с НДС»): в шапку идёт
// только как продолжение уже начатой шапки.
//
// В обоих случаях в строке не должно быть цифр: в подписи графы их не бывает,
// а в строке документа над таблицей они есть почти всегда — «аренды№49»,
// «от17.03.20г», «2023г». Отбор по цифрам, а не по длине текста: surya
// склеивает всю левую часть шапки в один широкий бокс, и ограничение по длине
// выбросило бы настоящий заголовок вместе с мусором.
func headerRowKind(r tokenRow, bands [][2]float64) (wide, narrow bool) {
	if len(r.Toks) == 0 {
		return false, false
	}
	for _, t := range r.Toks {
		if reNumberish.MatchString(t.Text) {
			return false, false
		}
	}
	filled := 0
	for _, c := range rowCells(r, bands) {
		if strings.TrimSpace(c) != "" {
			filled++
		}
	}
	switch {
	case filled >= 2:
		return true, false
	case filled == 1:
		return false, true
	}
	return false, false
}

// isNumberingRow распознаёт ряд с номерами граф: все непустые ячейки — числа
// подряд, начиная с 1. В бланках он стоит под шапкой и данными не является.
func isNumberingRow(cells []string) bool {
	n, prev := 0, 0
	for _, c := range cells {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if !reColNumbering.MatchString(c) {
			return false
		}
		v := 0
		for _, r := range c {
			v = v*10 + int(r-'0')
		}
		if v <= prev {
			return false
		}
		prev = v
		n++
	}
	return n >= 3 && prev <= len(cells)+2
}

// isNameContinuation — строка, где заполнена только текстовая графа: это хвост
// наименования предыдущей позиции, перенесённый на новую строку.
func isNameContinuation(cells []string, roles []string) bool {
	filled, nameOnly := 0, true
	for i, c := range cells {
		if strings.TrimSpace(c) == "" {
			continue
		}
		filled++
		if reNumberish.MatchString(c) {
			return false
		}
		if i < len(roles) && roles[i] != "name" && roles[i] != "other" && roles[i] != "" {
			nameOnly = false
		}
	}
	return filled > 0 && filled <= 2 && nameOnly
}

// isJunkRow — строка-огрызок: заполнена ровно одна графа, в ней короткое
// число, и это не наименование. Настоящая позиция так не выглядит: у неё либо
// есть наименование, либо заполнено несколько числовых граф.
func isJunkRow(cells []string, roles []string) bool {
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
	if idx < len(roles) && roles[idx] == "name" {
		return false
	}
	v := strings.TrimSpace(cells[idx])
	return len([]rune(v)) <= 2 && reNumberish.MatchString(v)
}

// mergeCells дописывает вторую половину перенесённого наименования к первой.
func mergeCells(acc []string, cells []string) []string {
	if acc == nil {
		out := make([]string, len(cells))
		copy(out, cells)
		return out
	}
	for i := range acc {
		if i >= len(cells) {
			break
		}
		v := strings.TrimSpace(cells[i])
		if v == "" {
			continue
		}
		if acc[i] == "" {
			acc[i] = v
		} else {
			acc[i] = strings.TrimSpace(acc[i] + " " + v)
		}
	}
	return acc
}

// prependCells ставит придержанный текст перед содержимым строки: наименование
// читается в том же порядке, в каком напечатано.
func prependCells(pending []string, cells []string) []string {
	out := make([]string, len(cells))
	copy(out, cells)
	for i := range out {
		if i >= len(pending) {
			break
		}
		p := strings.TrimSpace(pending[i])
		if p == "" {
			continue
		}
		if out[i] == "" {
			out[i] = p
		} else {
			out[i] = strings.TrimSpace(p + " " + out[i])
		}
	}
	return out
}

func appendToFreeName(row []string, cells []string, roles []string) {
	for i, c := range cells {
		c = strings.TrimSpace(c)
		if c == "" || i >= len(row) {
			continue
		}
		if row[i] == "" {
			row[i] = c
			continue
		}
		row[i] = strings.TrimSpace(row[i] + " " + c)
	}
	_ = roles
}

// dropDegenerateRows выкидывает строки-повторы: когда детектор или проекция
// растиражировали один и тот же текст по нескольким рядам. В интерфейсе это
// выглядело как восемь одинаковых строк вместо двух настоящих позиций.
func dropDegenerateRows(rows [][]string) [][]string {
	seen := map[string]bool{}
	out := make([][]string, 0, len(rows))
	for _, r := range rows {
		key := strings.Join(r, "\x00")
		if strings.TrimSpace(strings.ReplaceAll(key, "\x00", "")) == "" {
			continue
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, r)
	}
	return out
}
GOEOF
cat > "$GO/internal/recognize/lease_test.go" <<'GOEOF'
package recognize

import "testing"

// Счёт-протокол аренды: стороны подписаны «Арендодатель» и «Арендатор», УНП
// каждой стоит пятой строкой её блока, а суммы под таблицей напечатаны
// прописью двумя строками подряд — сначала налог, потом итог.
const leaseProtocol = `
Арендодатель: ООО "Эстурс"
г.Минск ул.Шаранговича, 7/3, ком.21А
р/с № BY29ALFA30122115400020270000
Банк: ЗАО "Альфа-Банк". Отделение "На Притыцкого" в г. Минске БИК ALFABY2X
тел.
УНП 100230072
Арендатор: ООО "КофеВенд"
г.Минск ул.Сторожевская, 8, помещение 8
р/с № BY96BPSB30123133260199330000, Банк: ОАО "Сбер Банк" г. Минск БИК BPSBBY2X
УНП 590888079

Предмет счета  Ед.изм.  Количество  Цена  Стоимость  Ставка НДС  Сумма НДС  Сумма с НДС
Электроэнергия  кВт  61  0,41  24,80  20 %  4,96  29,76
Эксплуатационные расходы  руб  61  0,08  4,96  20 %  0,99  5,95
Итого:  29,76  x  5,95  35,71

Сумма НДС:  Пять белорусских рублей 95 копеек
Всего:  Тридцать пять белорусских рублей 71 копейка
`

func TestLeasePartiesAndTaxIDs(t *testing.T) {
	f := extractByRules(leaseProtocol)

	if got := f["counterparty"].Value; got != `ООО "Эстурс"` {
		t.Errorf("контрагент: %q, ожидался арендодатель", got)
	}
	if got := f["organization"].Value; got != `ООО "КофеВенд"` {
		t.Errorf("организация: %q, ожидался арендатор", got)
	}
	// Самое опасное: оба УНП настоящие и оба из документа, но у разных сторон.
	if got := f["unp"].Value; got != "100230072" {
		t.Errorf("УНП контрагента: %q, ожидался УНП арендодателя 100230072", got)
	}
	if got := f["organization_unp"].Value; got != "590888079" {
		t.Errorf("УНП организации: %q, ожидался УНП арендатора 590888079", got)
	}
}

func TestLeaseTotalsInWords(t *testing.T) {
	f := extractByRules(leaseProtocol)

	if got := f["vat_amount"].Value; got != "5.95" {
		t.Errorf("сумма НДС: %q, ожидалось 5.95", got)
	}
	// Раньше сюда попадал налог со строки выше — 5,95 вместо 35,71.
	if got := f["total"].Value; got != "35.71" {
		t.Errorf("сумма с НДС: %q, ожидалось 35.71", got)
	}
}
GOEOF
echo "   ok"

if command -v go >/dev/null 2>&1; then
  (cd "$GO" && gofmt -l internal/recognize/ && go vet ./internal/recognize/ && go test ./internal/recognize/) || {
    echo; echo "Не собралось. Откат:"
    for f in extract.go words.go columns.go; do
      echo "  cp $GO/internal/recognize/$f.bak.lease-$TS $GO/internal/recognize/$f"
    done
    exit 1; }
fi

docker compose up -d --build docflow-backend

cat <<MSG

Готово. Бэкапы: *.bak.lease-$TS

Распознайте счёт-протокол заново. Ожидаемо:
  Контрагент       ООО "Эстурс"     УНП контрагента   100230072
  Организация      ООО "КофеВенд"   УНП организации   590888079
  Сумма НДС        5.95
  Сумма с НДС      35.71
MSG
