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
	reTitleWord = regexp.MustCompile(`(?i)^(счет|счёт|счет-фактура|счёт-фактура|эсчф|накладная|акт|тн|ттн|тн-2|ттн-1|упд|доверенность|договор)[-,.:]?$`)
	// Номер: цифры, возможно с буквами и разделителями. Не дата и не сумма.
	reNumToken = regexp.MustCompile(`^(?:№\s*)?([0-9][0-9A-Za-zА-Яа-я\-/]{0,19})$`)
	// Строки, рядом с которыми номер документа не стоит.
	reNotNumberRow = regexp.MustCompile(`(?i)(унп|унн|инн|окпо|р/с|расч|бик|bic|поручени|телефон|тел\.|индекс|январ|феврал|март|апрел|мая|май|июн|июл|август|сентябр|октябр|ноябр|декабр)`)
	reDateLike     = regexp.MustCompile(`^\d{1,2}[.\-/]\d{1,2}([.\-/]\d{2,4})?$`)
	reYearLike     = regexp.MustCompile(`^(19|20)\d{2}$`)
)

// HeaderNumberFromWords возвращает номер документа, найденный по расположению
// на странице. Пустая строка — найти не удалось.
func HeaderNumberFromWords(words []wordBox) string {
	toks := make([]token, 0, len(words))
	for _, w := range words {
		t := normalizeNoSign(StripMath(strings.TrimSpace(w.Text)))
		if t == "" || w.X1 <= w.X0 || w.Y1 <= w.Y0 {
			continue
		}
		toks = append(toks, token{Text: t, X0: w.X0, Y0: w.Y0, X1: w.X1, Y1: w.Y1})
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

	for i := 0; i < limit; i++ {
		ti := titleTokenIndex(rows[i])
		if ti < 0 {
			continue
		}
		title := rows[i].Toks[ti]

		// Сначала — справа от названия на той же строке.
		if n := numberRightOf(rows[i], ti); n != "" {
			return n
		}
		// Затем — ниже, в той же вертикальной полосе: так набран бланк, где
		// название стоит шапкой, а номер под ним.
		for j := i + 1; j < limit && j <= i+3; j++ {
			line := rowText(rows[j])
			if reNotNumberRow.MatchString(line) {
				continue
			}
			for _, t := range rows[j].Toks {
				if !xOverlaps(title, t) {
					continue
				}
				if n := plainNumber(t.Text); n != "" {
					return n
				}
			}
		}
	}
	return ""
}

func titleTokenIndex(r tokenRow) int {
	for i, t := range r.Toks {
		if reTitleWord.MatchString(strings.Trim(t.Text, "«»\"'")) {
			return i
		}
	}
	return -1
}

func numberRightOf(r tokenRow, from int) string {
	line := rowText(r)
	if reNotNumberRow.MatchString(line) {
		return ""
	}
	for i := from + 1; i < len(r.Toks) && i <= from+3; i++ {
		if n := plainNumber(r.Toks[i].Text); n != "" {
			return n
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
