package recognize

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Разрезание страницы на зоны.
//
// Реквизиты шапки — номер, дата, стороны, договор — стоят в документе НАД
// таблицей. Правила же искали их по всему распознанному тексту, вместе с
// табличной частью. Отсюда номер документа, взятый из графы «Кол-во», и дата
// из строки позиции, спорящая с датой документа.
//
// Zones отделяет то, что напечатано выше таблицы, от самой таблицы и от
// подвала. Дальше каждый реквизит ищется там, где он по форме стоит.
type Zones struct {
	// Head — всё выше табличной полосы: название документа, номер, дата,
	// реквизиты сторон.
	Head string
	// Totals — строка «Итого» табличной полосы. Отделена от позиций: суммы
	// документа брать из неё можно, наименования и количества — нет.
	Totals string
	// Foot — всё ниже таблицы: суммы прописью, «в том числе НДС», подписи.
	Foot string
	// Body — строки позиций таблицы. В реквизиты не идут никогда; нужны,
	// чтобы понять, что значение взято именно оттуда.
	Body string
	// OK — полосу таблицы удалось найти и шапка не потеряла стороны сделки.
	// false — работаем по всему тексту, как раньше.
	OK bool
}

// BuildZones режет страницу по координатам слов. flat — весь распознанный
// текст, он же результат при неудаче.
func BuildZones(words []wordBox, flat string) Zones {
	z := Zones{Head: flat}
	toks := make([]token, 0, len(words))
	for _, w := range words {
		t := normalizeNoSign(StripMath(strings.TrimSpace(w.Text)))
		if t == "" || w.X1 <= w.X0 || w.Y1 <= w.Y0 {
			continue
		}
		toks = append(toks, token{Text: t, X0: w.X0, Y0: w.Y0, X1: w.X1, Y1: w.Y1})
	}
	if len(toks) < 8 {
		return z
	}
	rows := groupTokenRows(toks)
	if len(rows) < 3 {
		return z
	}
	head, first, last := findTableBand(rows)
	if head < 0 {
		return z
	}

	var hb, bb, tb, fb strings.Builder
	for i := 0; i < head && i < len(rows); i++ {
		hb.WriteString(rowText(rows[i]))
		hb.WriteByte('\n')
	}
	for i := first; i <= last && i < len(rows); i++ {
		line := rowText(rows[i])
		if reTotalsRow.MatchString(line) {
			tb.WriteString(line)
			tb.WriteByte('\n')
			continue
		}
		bb.WriteString(line)
		bb.WriteByte('\n')
	}
	for i := last + 1; i < len(rows); i++ {
		fb.WriteString(rowText(rows[i]))
		fb.WriteByte('\n')
	}

	headText := strings.TrimSpace(hb.String())
	if headLostParties(headText) {
		// Полоса таблицы захватила блок реквизитов сторон: на бланках он тоже
		// напечатан в две колонки. Такой шапке доверять нельзя — возвращаемся
		// к разбору по всему тексту.
		return z
	}

	z.Head = headText
	z.Body = strings.TrimSpace(bb.String())
	z.Totals = strings.TrimSpace(tb.String())
	z.Foot = strings.TrimSpace(fb.String())
	z.OK = true
	return z
}

// HeaderText — текст для поиска реквизитов шапки.
func (z Zones) HeaderText(flat string) string {
	if !z.OK || strings.TrimSpace(z.Head) == "" {
		return flat
	}
	return z.Head
}

// AmountsText — текст для поиска сумм документа: шапка, строка «Итого» и
// подвал. Позиции таблицы исключены: именно из них раньше в «Сумму» и «НДС»
// приезжало одно и то же число.
func (z Zones) AmountsText(flat string) string {
	if !z.OK {
		return flat
	}
	parts := make([]string, 0, 3)
	for _, p := range []string{z.Head, z.Totals, z.Foot} {
		if strings.TrimSpace(p) != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return flat
	}
	return strings.Join(parts, "\n")
}

// ---------------------------------------------------------------------------
// Чистка подвала.
//
// В подвал попадает всё, что распозналось на печатях, подписях и на просвете
// листа: обрывки латиницы, точки, случайные числа. Модель видит это наравне с
// реквизитами и берёт числа оттуда — так «Итого» становилось 0.00, взятым с
// оттиска печати.
//
// Полезного в подвале ровно две вещи: суммы прописью и «в том числе НДС».
// Их и оставляем, остальное до модели не доходит.
// ---------------------------------------------------------------------------

var (
	// Строка, ради которой подвал вообще показывается модели.
	reFootKeep = regexp.MustCompile(`(?i)(прописью|в\s*т\.?\s*ч|в\s+том\s+числе|ндс|итого|всего|к\s+оплате|сумма)`)
	// Начало блока подписей: ниже читать нечего.
	reFootStop = regexp.MustCompile(`(?i)(м\.?\s*п\.?$|подпис|отпустил|получил|принял|сдал|главный\s+бухгалтер|начальник|директор|руководитель|товар\s+получ)`)
)

// CleanFoot оставляет в подвале только строки с суммами и обрывает его на
// блоке подписей.
func CleanFoot(foot string) string {
	if strings.TrimSpace(foot) == "" {
		return ""
	}
	out := make([]string, 0, 8)
	for _, line := range strings.Split(foot, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if reFootStop.MatchString(line) {
			break
		}
		if !reFootKeep.MatchString(line) {
			continue
		}
		if isOCRNoise(line) {
			continue
		}
		out = append(out, line)
		if len(out) >= 8 {
			break
		}
	}
	return strings.Join(out, "\n")
}

// isOCRNoise — строка похожа на мусор с печати: мало букв, много знаков
// препинания и латиницы вперемешку.
func isOCRNoise(line string) bool {
	var letters, cyr, punct, total int
	for _, r := range line {
		if unicode.IsSpace(r) {
			continue
		}
		total++
		switch {
		case unicode.IsLetter(r):
			letters++
			if r >= 'А' && r <= 'я' || r == 'ё' || r == 'Ё' {
				cyr++
			}
		case unicode.IsDigit(r):
		default:
			punct++
		}
	}
	if total == 0 {
		return true
	}
	if utf8.RuneCountInString(line) < 4 {
		return true
	}
	// Больше трети знаков препинания — это не текст документа.
	if punct*3 > total {
		return true
	}
	// Буквы есть, но кириллицы среди них почти нет: обрывки латиницы с
	// оттиска печати («3) services of the», «With the second»).
	if letters > 0 && cyr*2 < letters {
		return true
	}
	return false
}
