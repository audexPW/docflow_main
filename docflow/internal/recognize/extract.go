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
	// Пробел внутри числа — только разделитель разрядов («1 234,56»), но не
	// перенос строки: `\s` склеивал год в конце одной строки с суммой в
	// начале следующей («за август 2023\n29,62» → 202329.62) и такое число
	// выигрывало у настоящего итога как максимальное.
	reMoney = regexp.MustCompile(`\b(\d[\d \x{00A0}]*[.,]\d{2})\b`)

	// Стороны сделки. Контрагент — тот, кто выставил документ; организация —
	// фирма заказчика, на которую документ оформлен. Различаются по подписи
	// строки, потому что по одному только УНП понять, чей он, невозможно.
	// \b в Go RE2 — граница ASCII-слова, а кириллица для неё «не буква»:
	// с \b эти шаблоны не находили ни одной русской подписи. Поэтому граница
	// задана явно — началом строки или небуквенным символом.
	reCounterpartyLine = regexp.MustCompile(`(?im)^[^\n]{0,40}?(?:поставщик|продавец|исполнитель|грузоотправитель|подрядчик|арендодатель|наймодатель)(?:\s+и\s+его\s+адрес)?[^\p{L}\n]{0,3}[:\-][ \t]*(.*)$`)
	reOrganizationLine = regexp.MustCompile(`(?im)^[^\n]{0,40}?(?:покупатель|плательщик|заказчик|грузополучатель|арендатор|наниматель)(?:\s+и\s+его\s+адрес)?[^\p{L}\n]{0,3}[:\-][ \t]*(.*)$`)
	reTaxLabel         = regexp.MustCompile(`(?i)(УНП|ИНН|УНН)`)
	rePartyTail        = regexp.MustCompile(`(?i)(адрес|р/с|расч|тел\.|тел\s|банк|факс|e-mail|почтовый)`)

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
	return extractByRulesZoned(ocrText, Zones{Head: ocrText})
}

// extractByRulesZoned — то же самое, но с оглядкой на то, где реквизит
// напечатан. Реквизиты шапки ищутся ТОЛЬКО в шапке, суммы — в шапке, строке
// «Итого» и подвале. Позиции таблицы в реквизиты не идут никогда: именно
// оттуда раньше приезжал номер документа, взятый из графы «Кол-во», и дата
// позиции вместо даты документа.
func extractByRulesZoned(ocrText string, z Zones) map[string]domain.Field {
	fields := make(map[string]domain.Field)

	headText := z.HeaderText(ocrText)
	amountsText := z.AmountsText(ocrText)

	// Стороны сделки разбираем первыми: от них зависит, чей налоговый
	// идентификатор считать идентификатором контрагента.
	cpName, cpTax := party(headText, reCounterpartyLine)
	orgName, orgTax := party(headText, reOrganizationLine)

	if cpName != "" {
		fields["counterparty"] = ruleField(cpName)
	}
	if orgName != "" {
		fields["organization"] = ruleField(orgName)
	}
	if orgTax != "" {
		fields["organization_unp"] = ruleField(orgTax)
	}

	if unp := pickTaxID(headText, reUNP, cpTax, orgTax); unp != "" {
		fields["unp"] = ruleField(unp)
	}
	if inn := pickTaxID(headText, reINN, cpTax, orgTax); inn != "" {
		fields["inn"] = ruleField(inn)
	}
	if m := reKPP.FindStringSubmatch(headText); m != nil {
		fields["kpp"] = ruleField(m[1])
	}
	if num, conf := extractDocNumber(headText); num != "" {
		fields["number"] = domain.Field{Value: num, Confidence: conf, Source: "rule"}
	}
	// Договор-основание — отдельно от номера самого документа. Раньше
	// извлекался только для того, чтобы исключить его из number, и никуда
	// дальше не уходил.
	if contract := contractNumber(headText); contract != "" {
		fields["contract_number"] = ruleField(contract)
		if date := contractDate(headText); date != "" {
			fields["contract_date"] = ruleField(date)
		}
	}
	if m := reDateDotted.FindStringSubmatch(headText); m != nil {
		fields["date"] = ruleField(m[1])
	} else if d := dateFromWords(headText); d != "" {
		// Точечной даты в бланке нет — пробуем дату прописью. Порядок веток
		// важен: где ДД.ММ.ГГГГ есть, поведение остаётся прежним.
		fields["date"] = ruleField(d)
	}
	if amount, conf := extractTotal(amountsText); amount != "" {
		fields["total"] = domain.Field{Value: amount, Confidence: conf, Source: "rule"}
	}
	if m := reVATAmount.FindStringSubmatch(amountsText); m != nil {
		fields["vat_amount"] = ruleField(normalizeMoney(m[1]))
	} else if v := VATFromWords(amountsText); v != "" {
		// Сумма НДС прописью под таблицей: «в т.ч. НДС: Семь рублей 26
		// копеек». В самой таблице отдельной графы с НДС может не быть вовсе —
		// заказчик показал именно такой счёт, и поле оставалось пустым, хотя
		// значение в документе есть, просто написано буквами.
		fields["vat_amount"] = ruleField(v)
	}
	if m := reAmountNoVAT.FindStringSubmatch(amountsText); m != nil {
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

	// В бланке справа от подписи «Поставщик и его адрес:» нередко стоит
	// название самого документа — «СЧЕТ-ФАКТУРА»: в тексте с раскладкой обе
	// колонки листа попадают в одну строку. Наименование стороны при этом
	// напечатано строкой ниже. Если после подписи стоит название документа
	// или пусто — берём следующую строку.
	if isDocTitleText(tail) || len([]rune(cleanPartyTail(tail))) < 3 {
		if next := nextLine(text, loc[1]); next != "" && !isDocTitleText(next) {
			tail = next
		}
	}

	if t := reUNP.FindStringSubmatch(tail); t != nil {
		taxID = t[1]
	} else if t := reINN.FindStringSubmatch(tail); t != nil {
		taxID = t[1]
	}

	name = cleanPartyTail(tail)
	if isDocTitleText(name) {
		name = ""
	}
	return name, taxID
}

// cleanPartyTail обрезает наименование стороны до налогового номера или до
// адреса и реквизитов банка.
func cleanPartyTail(tail string) string {
	// Подпись графы, приехавшая вместе со значением: «Наименование ООО …».
	tail = rePartyLabelPrefix.ReplaceAllString(strings.TrimSpace(tail), "")
	if loc := reTaxLabel.FindStringIndex(tail); loc != nil {
		tail = tail[:loc[0]]
	}
	if loc := rePartyTail.FindStringIndex(tail); loc != nil {
		tail = tail[:loc[0]]
	}
	name := strings.TrimSpace(strings.Trim(strings.TrimSpace(tail), ",;:-"))
	if len([]rune(name)) < 3 {
		return ""
	}
	// Значение, которое само является подписью строки («покупатель»,
	// «поставщик»), стороной сделки не является: так в контрагенты попадало
	// слово из соседней колонки бланка.
	if rePartyLabelOnly.MatchString(name) {
		return ""
	}
	return name
}

var (
	rePartyLabelPrefix = regexp.MustCompile(`(?i)^(наименование|наименование\s+организации|организация)[\s:,-]+`)
	rePartyLabelOnly   = regexp.MustCompile(`(?i)^(поставщик|продавец|исполнитель|подрядчик|арендодатель|наймодатель|покупатель|плательщик|заказчик|арендатор|наниматель|грузоотправитель|грузополучатель)[\s:,.-]*$`)
)

// reDocTitleOnly — строка целиком является названием документа.
var reDocTitleOnly = regexp.MustCompile(`(?i)^\s*(счет|счёт|счет-фактура|счёт-фактура|эсчф|акт|акт\s+[а-яё\s-]{0,30}|товарная\s+накладная|товарно-транспортная\s+накладная|накладная|упд|доверенность|договор)\s*(№\s*\S+)?\s*$`)

func isDocTitleText(s string) bool {
	return reDocTitleOnly.MatchString(strings.TrimSpace(s))
}

// nextLine возвращает следующую непустую строку текста после позиции pos.
func nextLine(text string, pos int) string {
	if pos >= len(text) {
		return ""
	}
	rest := text[pos:]
	for _, line := range strings.Split(rest, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

// pickTaxID выбирает налоговый номер контрагента. Если строка поставщика
// подписана — берём номер оттуда. Иначе берём первый номер в тексте, но
// пропускаем тот, что уже опознан как номер собственной организации: иначе в
// 1С контрагентом станет сам заказчик.
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
		// Номер внутри кавычек — часть названия организации, а не номер
		// документа: «Филиал "Троллейбусный парк №5"», ОАО «Стройтрест №7».
		if numberInsideQuotes(text, "№"+num) || numberInsideQuotes(text, num) {
			continue
		}
		return num, ruleConfidence
	}
	// Строгий якорь не достал до номера: в бланках заголовок, подзаголовок и
	// номер разнесены по строкам. Пробуем то же самое с окном пошире и с
	// разрешёнными переносами — но всё ещё привязываясь к названию документа,
	// а не хватая первый «№» на странице.
	for _, m := range reWideAnchoredNumber.FindAllStringSubmatch(text, -1) {
		num := strings.Trim(m[2], "-/")
		if num == "" || looksLikeAccountNumber(num) {
			continue
		}
		if contract != "" && num == contract {
			continue
		}
		if numberInsideQuotes(text, "№"+num) || numberInsideQuotes(text, num) {
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
		// Банковский счёт из шапки под «№» — не номер документа.
		if looksLikeAccountNumber(num) {
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

		// Цифрами итога рядом с якорем нет — на белорусских бланках он
		// часто напечатан прописью: «Всего Сорок семь белорусских рублей
		// 74 копейки». Значение в документе есть, просто буквами.
		if v := ParseMoneyWords(window); v != "" {
			return v, ruleConfidence
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
