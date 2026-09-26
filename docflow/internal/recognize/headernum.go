package recognize

import (
	"regexp"
	"strings"
)

// Номер документа по месту на листе.
//
// В белорусских бланках номер печатается рядом с названием документа — справа
// от него или прямо под ним, и знака «№» при этом может не быть вовсе. На
// счёте-фактуре Троллейбусного парка настоящий номер 118 стоит под словом
// СЧЕТ-ФАКТУРА в правом верхнем углу; правила его не видели, зато находили
// «№5» внутри названия организации — «Филиал "Троллейбусный парк №5"» — и
// именно эта пятёрка уезжала в 1С как номер документа.
//
// Здесь номер ищется по координатам: находим слово-название документа и
// смотрим, что стоит справа от него на той же строке или ниже в той же
// вертикальной полосе.

var (
	// Слова-названия документов, к которым привязан номер.
	// «Счет-акт на оплату аренды № А0000000860» — составное название через
	// дефис встречается в белорусских бланках постоянно.
	reTitleWord = regexp.MustCompile(`(?i)^(счет|счёт|счет-фактура|счёт-фактура|счет-акт|счёт-акт|эсчф|накладная|акт|тн|ттн|тн-2|ттн-1|упд|доверенность|договор)[-,.:]?$`)
	// Номер: цифры, возможно с буквами и разделителями. Не дата и не сумма.
	// Номер начинается и с буквы: в белорусских бланках это серия — АП4531279,
	// У-000484, Э0000000750. Требование «первый символ цифра» теряло их все.
	// Буквенная часть не длиннее двух символов, иначе это слово, а не номер.
	reNumToken = regexp.MustCompile(`^(?:№\s*)?([0-9A-Za-zА-Яа-я]{0,2}-?[0-9][0-9A-Za-zА-Яа-я\-/]{0,19})$`)
	// Строки, рядом с которыми номер документа не стоит.
	// Месяцы словами отсюда убраны намеренно: «Счет-фактура № 85906 от 11
	// Августа 2023 г.» — обычная дата документа, а строка отвергалась целиком
	// вместе с настоящим номером. Месяц мешает, только если строка ИМ и
	// начинается («Август 2023» в графе периода) — это ловится ниже.
	reNotNumberRow = regexp.MustCompile(`(?i)(унп|унн|инн|окпо|р/с|расч|бик|bic|поручени|телефон|тел\.|индекс)`)
	reMonthStart   = regexp.MustCompile(`(?i)^(январ|феврал|март|апрел|мая|май|июн|июл|август|сентябр|октябр|ноябр|декабр)`)
	// «Договор» — название документа, но самое слабое: если на листе есть акт
	// или счёт, их номер важнее.
	reContractTitle = regexp.MustCompile(`(?i)^(договор|контракт)[-,.:]?$`)
	reDateLike      = regexp.MustCompile(`^\d{1,2}[.\-/]\d{1,2}([.\-/]\d{2,4})?$`)
	// «б/н» — документ выписан без номера, и любой номер в этой строке чужой.
	reNoNumberMark = regexp.MustCompile(`(?i)(^|\s)б\s?/\s?н($|\s|,|\.)`)
	// Знак «№» после этих слов открывает номер ДОГОВОРА, а не документа:
	// «Акт выполненных работ б/н по договору аренды от 01.03.2022г. №42».
	// Знак «№» после этих слов открывает номер ЧУЖОГО документа: договора,
	// счёта поставщика, накладной, спецификации. Пример: «Акт выполненных
	// работ к счету № 45» ставил акту номер счёта, и документы схлопывались.
	reOtherNumOwner = regexp.MustCompile(`(?i)^(договор|контракт|заказ|заявк|смет|спецификаци|приложени|счет|счёт|счет-фактур|счёт-фактур|накладн|поручени|платежк|требовани|претензи|письм|прейскурант|тариф|акту$|акта$|актом$)`)
	// «Форма № 868», «бланк № 5» — номер бланка, но только когда слово стоит прямо
	// перед знаком. Префиксом «форм/бланк» по всей строке стирался собственный
	// номер: «АКТ списания бланков … № 12», «по формированию отчетности № 15».
	reFormOwner = regexp.MustCompile(`(?i)^(форма|формы|форме|бланк|бланка|образец|образца)[.,:]?$`)
	// «Лицевой счет № 123456», «Расчетный счет» — реквизит, а не название документа
	reAccountWordBefore = regexp.MustCompile(`^(лицев|расчетн|расчётн|текущ|р/с|р/сч)`)
	// год бывает с приклеенной «г»: «2024г», «2023г.» — это не номер документа
	reYearLike = regexp.MustCompile(`^(19|20)\d{2}\s*(г|г\.|года|году)?[.,]?$`)
)

// latinLookalikes приводит латинские буквы, неотличимые по начертанию от
// кириллических, к кириллице: распознавание регулярно отдаёт AKT вместо АКТ и
// CЧET вместо СЧЕТ, и слово-название документа переставало опознаваться.
var latinLookalikes = strings.NewReplacer(
	"A", "А", "B", "В", "C", "С", "E", "Е", "H", "Н", "K", "К", "M", "М",
	"O", "О", "P", "Р", "T", "Т", "X", "Х", "Y", "У",
	"a", "а", "c", "с", "e", "е", "o", "о", "p", "р", "x", "х", "y", "у",
)

// HeaderNumberFromWords возвращает номер документа, найденный по расположению
// на странице. Пустая строка — найти не удалось.
func HeaderNumberFromWords(words []wordBox) string {
	toks := make([]token, 0, len(words))
	for _, w := range words {
		t := normalizeNoSign(StripMath(strings.TrimSpace(w.Text)))
		if reTitleWord.MatchString(latinLookalikes.Replace(t)) {
			t = latinLookalikes.Replace(t)
		}
		if t == "" || w.X1 <= w.X0 || w.Y1 <= w.Y0 {
			continue
		}
		toks = append(toks, token{Text: t, X0: w.X0, Y0: w.Y0, X1: w.X1, Y1: w.Y1, Line: w.Line, Seq: w.Seq})
	}
	if len(toks) < 5 {
		return ""
	}
	rows := groupTokenRows(toks)
	if len(rows) < 2 {
		return ""
	}
	// Полосу таблицы ниже не трогаем: номер документа в ней не печатают.
	limit := len(rows)
	if _, first, _ := findTableBand(rows); first > 0 && first < limit {
		limit = first
	}

	// Тип документа задаёт самое верхнее название на листе. Строки реквизитов
	// не в счёт: «Расчетный счет» — не счёт.
	//
	// «Договор» — тоже название документа. Если сверху акт или
	// счёт, номер договора ему чужой при любом раскладе («АКТ № б/н», ниже
	// «Договор аренды №А-48-22» — в 1С уезжал номер договора), и пустое поле
	// лучше. Обратно: если сверху договор, слово «акт» ниже — это текст
	// договора («Акт сдачи-приемки подписывается сторонами … 5 дней»), а не
	// заголовок, и номер берётся только у договора.
	top := -1
	for i := 0; i < limit; i++ {
		if titleTokenIndex(rows[i]) >= 0 && !reNotNumberRow.MatchString(rowText(rows[i])) {
			top = i
			break
		}
	}
	if top < 0 {
		return ""
	}
	wantContract := isContractRow(rows[top])
	for i := top; i < limit; i++ {
		ti := titleTokenIndex(rows[i])
		if ti < 0 || isContractRow(rows[i]) != wantContract {
			continue
		}
		n, stop := numberForTitle(rows, i, ti, limit)
		if n != "" {
			return n
		}
		if stop {
			return ""
		}
	}
	return ""
}

func isContractRow(r tokenRow) bool {
	ti := titleTokenIndex(r)
	return ti >= 0 && reContractTitle.MatchString(strings.Trim(r.Toks[ti].Text, "«»\"'"))
}

// numberForTitle ищет номер у названия в строке i. stop — документ прямо
// объявлен без номера («б/н»): искать дальше нельзя, любой найденный будет чужим.
func numberForTitle(rows []tokenRow, i, ti, limit int) (string, bool) {
	title := rows[i].Toks[ti]
	if noNumberMark(rows[i], ti) {
		return "", true
	}
	// Сначала — справа от названия на той же строке.
	if n := numberRightOf(rows[i], ti); n != "" {
		return n, false
	}
	// Затем — ниже.
	for j := i + 1; j < limit && j <= i+3; j++ {
		tj := titleTokenIndex(rows[j])
		if reNotNumberRow.MatchString(rowText(rows[j])) {
			if tj >= 0 {
				break
			}
			continue
		}
		// Строка ниже может нести ДРУГОЕ название («Договор № 5/20 от …» слева, в
		// реквизитах плательщика) и при этом номер нашего документа справа, в полосе
		// названия. Обрыв поиска на такой строке терял номер 118.
		// Поэтому сначала проверка по вертикали, но номер, который принадлежит тому
		// другому названию, не берётся.
		otherNum := ""
		if tj >= 0 {
			otherNum = numberRightOf(rows[j], tj)
		}
		// Строго под названием, в той же вертикальной полосе: так набран бланк,
		// где название стоит шапкой, а номер под ним. Эта проверка идёт раньше
		// переноса: на счёте-фактуре номер 118 стоит под
		// названием, а слева на той же строке «Банк: ЦБУ № 527».
		//
		// Проверки владельца, места и даты — те же, что справа от названия,
		// иначе «пом. 8» из адреса, «Помещение № 5», номер договора с
		// перенесённой строки «Акт … по договору» / «аренды № 42» уезжали номером
		// документа. Владелец ищется по склейке «хвост строки названия + строка ниже».
		ctx := tokenRow{Toks: append(append([]token{}, rows[i].Toks[ti:]...), rows[j].Toks...)}
		off := len(rows[i].Toks) - ti
		for k, t := range rows[j].Toks {
			if !xOverlaps(title, t) || isAddressPart(rows[j], k) || afterPlaceWord(rows[j], k) || isDatePart(rows[j], k) {
				continue
			}
			if k > 0 && strings.TrimSpace(rows[j].Toks[k-1].Text) == "№" && afterPlaceWord(rows[j], k-1) {
				continue
			}
			if numberBelongsToOther(ctx, off+k) {
				continue
			}
			if n := plainNumber(t.Text); n != "" {
				if tj >= 0 && n == otherNum {
					continue
				}
				return n, false
			}
		}
		if tj >= 0 {
			// ниже другое название — это уже другая строка документа
			// («Договор аренды №А-48-22»), переноса нашего заголовка там нет
			break
		}
		// Заголовок в две строки: «AKT» отдельно, под ним «сдачи-приемки
		// выполненных работ №У-000484». Только сразу под названием, только по
		// знаку «№», и строка читается как ПРОДОЛЖЕНИЕ названия: так «по
		// договору» в конце первой строки делает номер со второй чужим, а
		// «Помещение № 5» или индекс без знака номером не становятся.
		if j == i+1 {
			joined := tokenRow{Toks: append(append([]token{}, rows[i].Toks[ti:]...), rows[j].Toks...)}
			if noNumberMark(joined, 0) {
				return "", true
			}
			if n := numberBySign(joined, 0); n != "" {
				return n, false
			}
			if n := numberAfterDanglingSign(rows[i], ti, rows[j]); n != "" {
				return n, false
			}
			// Под названием одно число и больше ничего: номер бланка под
			// штрихкодом («ТОВАРНАЯ НАКЛАДНАЯ» / «1333337», IMG_0001).
			if len(rows[j].Toks) == 1 && rows[j].Toks[0].X0 >= title.X0 {
				if n := plainNumber(rows[j].Toks[0].Text); n != "" {
					return n, false
				}
			}
		}
	}
	return "", false
}

// numberAfterDanglingSign — строка названия кончается знаком «№», а сам номер
// распознавание унесло на строку ниже, правее знака: «СЧЕТ-ФАКТУРА №» /
// «31.08.2023 г. 810» (IMG_0161; на бумаге номер стоит в той же строке).
func numberAfterDanglingSign(r tokenRow, ti int, next tokenRow) string {
	last := len(r.Toks) - 1
	if last <= ti || strings.TrimSpace(r.Toks[last].Text) != "№" || numberBelongsToOther(r, last) {
		return ""
	}
	sign := r.Toks[last]
	for k, t := range next.Toks {
		// «от 15 сентября 2023 г.» правее знака — день даты, а не номер
		if t.X0 <= sign.X0 || isAddressPart(next, k) || isDatePart(next, k) {
			continue
		}
		if n := plainNumber(t.Text); n != "" {
			return n
		}
	}
	return ""
}

// noNumberMark — после названия стоит «б/н», относящийся к самому документу, а
// не к чужому («по договору б/н»).
var reNoNumberToken = regexp.MustCompile(`(?i)^(№\s*)?б\s?/\s?н[.,:;]?$`)

func noNumberMark(r tokenRow, from int) bool {
	for k := from + 1; k < len(r.Toks); k++ {
		// Свой номер по знаку стоит раньше «б/н» — пометка относится к документу,
		// на который ссылаются: «Счет-фактура №371 от 31.08.2023 Договор б/н»
		// стирал номер 371.
		if ownNumberBySignAt(r, k) {
			return false
		}
		w := latinLookalikes.Replace(strings.TrimSpace(r.Toks[k].Text))
		if reNoNumberToken.MatchString(w) && !numberBelongsToOther(r, k) {
			return true
		}
	}
	return false
}

// ownNumberBySignAt — в позиции k стоит знак «№» (отдельно или слитно с номером),
// за ним годный номер, и номер не чужой.
func ownNumberBySignAt(r tokenRow, k int) bool {
	t := strings.TrimSpace(r.Toks[k].Text)
	switch {
	case t == "№":
		return k+1 < len(r.Toks) && plainNumberAfterSign(r.Toks[k+1].Text) != "" && !numberBelongsToOther(r, k)
	case strings.HasPrefix(t, "№"):
		return plainNumberAfterSign(strings.TrimPrefix(t, "№")) != "" && !numberBelongsToOther(r, k)
	}
	return false
}

// isAddressPart — число является частью адреса: за ним «г.», «ул.», перед ним
// «Адрес:». Почтовый индекс под одиноким «АКТ» уезжал номером документа.
var reAddressNext = regexp.MustCompile(`(?i)^(г|гор|ул|пр|пр-т|д)(\.|,|$)`)

func isAddressPart(r tokenRow, k int) bool {
	if k+1 < len(r.Toks) && reAddressNext.MatchString(strings.TrimSpace(r.Toks[k+1].Text)) {
		return true
	}
	return k > 0 && strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.Toks[k-1].Text)), "адрес")
}

func titleTokenIndex(r tokenRow) int {
	for i, t := range r.Toks {
		if reTitleWord.MatchString(strings.Trim(t.Text, "«»\"'")) {
			if i > 0 {
				prev := strings.ToLower(strings.TrimSpace(r.Toks[i-1].Text))
				if reAccountWordBefore.MatchString(prev) {
					continue
				}
			}
			return i
		}
	}
	return -1
}

func numberRightOf(r tokenRow, from int) string {
	if n := numberBySign(r, from); n != "" {
		return n
	}
	if rowRejected(r) {
		return ""
	}
	// Знака нет — как раньше, ближайшее подходящее слово справа. Но не любое:
	// после снятия месяцев из стоп-списка сюда стал попадать ДЕНЬ из даты
	// («СЧЕТ-ФАКТУРА от 11 Августа 2023 г.» давал номер «11») и номера
	// помещений.
	for i := from + 1; i < len(r.Toks) && i <= from+3; i++ {
		// владельца проверяем и здесь: без этого «Форма № 868» отдавала номер
		// бланка как номер документа — знак блокировался, а запасной проход
		// брал ту же цифру следующим шагом
		if isDatePart(r, i) || afterPlaceWord(r, i) || numberBelongsToOther(r, i) || isAddressPart(r, i) {
			continue
		}
		if n := plainNumber(r.Toks[i].Text); n != "" {
			return n
		}
	}
	return ""
}

// rowRejected — строка, в которой номер документа не ищется: реквизиты, период,
// пометка «б/н» до знака номера.
func rowRejected(r tokenRow) bool {
	line := rowText(r)
	if reNotNumberRow.MatchString(line) || reMonthStart.MatchString(strings.TrimSpace(line)) {
		return true
	}
	// «б/н» относится к документу, рядом с которым стоит. Проверяем только
	// участок ДО первого знака номера: иначе пометка про чужой документ в конце
	// строки стирала собственный номер.
	head := line
	if i := strings.Index(line, "№"); i > 0 {
		head = line[:i]
	}
	return reNoNumberMark.MatchString(latinLookalikes.Replace(head))
}

// numberBySign — номер, на который указывает знак «№» правее слова from.
func numberBySign(r tokenRow, from int) string {
	if rowRejected(r) {
		return ""
	}
	line := rowText(r)
	// Сначала — по знаку «№»: он прямой указатель, и номер может стоять дальше
	// трёх слов от названия («АКТ оказанных услуг № АП4531279 от 14.09.2023»).
	// Знак бывает и слитно с номером: «№У-000484».
	for i := from + 1; i < len(r.Toks); i++ {
		t := strings.TrimSpace(r.Toks[i].Text)
		if t == "№" {
			// чей это номер: если выше по строке стоял «договор», то не наш
			if i > 0 && numberBelongsToOther(r, i) {
				continue
			}
			if i+1 < len(r.Toks) {
				if n := plainNumberAfterSign(r.Toks[i+1].Text); n != "" {
					// «Филиал "Троллейбусный парк №5"» — номер внутри названия
					// организации, а не документа. Это исходный дефект, ради
					// которого написан файл; в пути по знаку он вернулся.
					if numberInsideQuotes(line, n) || afterPlaceWord(r, i) {
						continue
					}
					return n
				}
			}
			continue
		}
		if strings.HasPrefix(t, "№") && len(t) > len("№") {
			if numberBelongsToOther(r, i) {
				continue
			}
			if n := plainNumberAfterSign(strings.TrimPrefix(t, "№")); n != "" {
				if numberInsideQuotes(line, n) || afterPlaceWord(r, i) {
					continue
				}
				return n
			}
		}
	}
	return ""
}

// plainNumber проверяет, что слово годится в номер документа: это не дата,
// не год, не сумма, не банковский счёт и не налоговый номер.
func plainNumber(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || reDateLike.MatchString(s) || reYearLike.MatchString(s) {
		return ""
	}
	if strings.ContainsAny(s, ".,") && strings.IndexAny(s, "0123456789") >= 0 {
		// «0,35», «10.07.01.» — это суммы и коды, а не номер.
		return ""
	}
	m := reNumToken.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	num := strings.Trim(m[1], "-/")
	if num == "" || looksLikeAccountNumber(num) {
		return ""
	}
	// Девять цифр подряд — это УНП, а не номер документа.
	if len(num) == 9 && isAllDigits(num) {
		return ""
	}
	// «000» — это «ООО» из названия организации, прочитанное цифрами
	if strings.Trim(num, "0") == "" {
		return ""
	}
	return num
}

func isAllDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return len(s) > 0
}

// numberInsideQuotes — найденный номер стоит внутри кавычек, то есть входит в
// название организации: «Филиал "Троллейбусный парк №5"», ОАО «Стройтрест №7».
// Такое значение номером документа не является.
func numberInsideQuotes(text, num string) bool {
	if num == "" {
		return false
	}
	for _, line := range strings.Split(text, "\n") {
		idx := strings.Index(line, num)
		if idx < 0 {
			continue
		}
		if quotedAt(line, idx) {
			return true
		}
	}
	return false
}

// quotedAt — позиция в строке находится внутри пары кавычек.
func quotedAt(line string, pos int) bool {
	runes := []rune(line)
	// Переводим байтовую позицию в рунную.
	rpos := len([]rune(line[:pos]))
	depth := 0
	for i := 0; i < rpos && i < len(runes); i++ {
		switch runes[i] {
		case '"':
			depth = 1 - depth
		case '«':
			depth = 1
		case '»':
			depth = 0
		}
	}
	if depth == 0 {
		return false
	}
	// Кавычка должна и закрыться — иначе это одиночный знак от OCR.
	for i := rpos; i < len(runes); i++ {
		switch runes[i] {
		case '"', '»':
			return true
		}
	}
	return false
}

// xOverlaps — слова стоят в одной вертикальной полосе листа. Допуск нужен
// потому, что номер под названием редко выровнен по нему точно.
func xOverlaps(a, b token) bool {
	w := a.X1 - a.X0
	if w <= 0 {
		return false
	}
	lo, hi := a.X0-w*0.5, a.X1+w*0.5
	c := (b.X0 + b.X1) / 2
	return c >= lo && c <= hi
}

// numberBelongsToOther — стоит ли перед знаком «№» слово, которое делает номер
// чужим: номером договора, заказа, заявки. Смотрим три слова назад, дальше
// связь уже теряется.
// numberBelongsToOther — чей это номер. Идём от знака «№» к началу строки и
// смотрим, что встретится первым.
//
// Название САМОГО документа («Счет-акт», «Акт», «Договор») означает, что номер
// наш. Слово в косвенном падеже («по договору», «к счету», «согласно
// спецификации») — что номер чужой: так пишут про другой документ.
//
// Здесь сняты два прежних допущения. Первое: окно в три слова
// пробивалось собственным мотивирующим примером («по договору аренды от
// 01.03.2022г. №42» — «договору» стоит четвёртым). Второе: «название стоит
// первым словом строки» — не всегда, слева бывает город («г. Минск Счет-акт на
// оплату аренды № А0000000860»).
func numberBelongsToOther(r tokenRow, at int) bool {
	for k := at - 1; k >= 0; k-- {
		// латиница, неотличимая от кириллицы («cчету» с латинской c), возвращала
		// чужой номер
		w := latinLookalikes.Replace(strings.Trim(strings.TrimSpace(r.Toks[k].Text), "«»\"'"))
		if reTitleWord.MatchString(w) {
			// «по ТТН №», «к счет-фактуре №» — название с предлогом: это ссылка на
			// другой документ, номер чужой (акт без номера получал
			// номер накладной)
			if k > 0 && reTitlePreposition.MatchString(strings.TrimSpace(r.Toks[k-1].Text)) {
				return true
			}
			return false
		}
		if reOtherNumOwner.MatchString(w) {
			return true
		}
		// «Акт выполненных работ к № 45» — предлог сразу перед знаком, а слово-
		// владелец съедено переносом или распознаванием: номер всё равно чужой
		if k == at-1 && reOwnerPreposition.MatchString(w) {
			return true
		}
		if reFormOwner.MatchString(w) && (k == at-1 || (k == at-2 && strings.TrimSpace(r.Toks[at-1].Text) == "№")) {
			return true
		}
	}
	return false
}

var reOwnerPreposition = regexp.MustCompile(`(?i)^(к|согласно)$`)

// предлог перед словом-названием делает его ссылкой на чужой документ
var reTitlePreposition = regexp.MustCompile(`(?i)^(к|по|согласно)$`)

// plainNumberAfterSign — номер, стоящий сразу после знака «№». Здесь можно быть
// мягче: знак прямо говорит, что это номер, и точка в нём не делает его суммой
// («СЧЕТ-ФАКТУРА № 395.33»). Запятая по-прежнему запрещена — в белорусских
// документах дробная часть пишется через неё, и «0,35» это деньги. Больше одной
// точки — это код вида «10.07.01», а не номер.
func plainNumberAfterSign(s string) string {
	s = strings.TrimSpace(s)
	if n := plainNumber(s); n != "" {
		return n
	}
	// «27-09/23» после знака — номер, а не дата: в дате разделители одинаковые.
	// Отброшенный как дата, он уступал место номеру договора (IMG_0153).
	if strings.Contains(s, "-") && strings.Contains(s, "/") {
		if m := reNumToken.FindStringSubmatch(s); m != nil {
			if n := strings.Trim(m[1], "-/"); n != "" && !looksLikeAccountNumber(n) {
				return n
			}
		}
	}
	if strings.Contains(s, ",") || strings.Count(s, ".") != 1 {
		return ""
	}
	if reDateLike.MatchString(s) || reYearLike.MatchString(s) {
		return ""
	}
	head, tail, _ := strings.Cut(s, ".")
	if head == "" || tail == "" || !isAllDigits(tail) {
		return ""
	}
	// «09.2023» — это период, а не номер: месяц слева, год справа
	if len(head) <= 2 && reYearLike.MatchString(tail) {
		return ""
	}
	if n := plainNumber(head); n != "" {
		return n + "." + tail
	}
	return ""
}

// isDatePart — слово является частью даты: перед ним стоит предлог «от», либо
// сразу за ним идёт название месяца («от 11 Августа 2023 г.»).
var reMonthWord = regexp.MustCompile(`(?i)^(январ|феврал|март|апрел|мая|май|июн|июл|август|сентябр|октябр|ноябр|декабр)`)

func isDatePart(r tokenRow, at int) bool {
	if at > 0 {
		prev := strings.ToLower(strings.TrimSpace(r.Toks[at-1].Text))
		if prev == "от" || prev == "за" {
			return true
		}
	}
	if at+1 < len(r.Toks) && reMonthWord.MatchString(strings.TrimSpace(r.Toks[at+1].Text)) {
		return true
	}
	return false
}

// afterPlaceWord — перед словом стоит указатель места: дом, помещение, квартира,
// офис, кабинет, корпус. Их номера не имеют отношения к номеру документа.
var rePlaceWord = regexp.MustCompile(`(?i)^(дом|д\.|пом|помещени|кв|квартир|оф|офис|каб|кабинет|корп|корпус|стр|строени|этаж|бл|блок)`)

func afterPlaceWord(r tokenRow, at int) bool {
	if at == 0 {
		return false
	}
	return rePlaceWord.MatchString(strings.TrimSpace(r.Toks[at-1].Text))
}
