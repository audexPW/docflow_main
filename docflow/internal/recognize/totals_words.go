package recognize

import (
	"math"
	"strings"
	"unicode"
)

// Слова строки итога: что считать изуродованным «ИТОГО», что — хвостом итоговой
// строки, а что — настоящим наименованием позиции.

// хвосты итоговой строки: «ИТОГО по счёту», «Всего с НДС», «Итого к оплате»
var totalsTailWords = map[string]bool{
	"счету": true, "счет": true, "документу": true, "накладной": true, "акту": true,
	"оплате": true, "оплату": true, "сумма": true, "сумму": true, "ндс": true,
	"руб": true, "byn": true, "бел": true, "числе": true, "том": true, "без": true,
	"итого": true, "всего": true, "итог": true, "листу": true, "странице": true,
}

// isNameWord — слово годится в наименование позиции.
func isNameWord(w string) bool {
	w = strings.TrimSpace(w)
	// «ИТ0ГО:36,78» — сумма приклеилась к слову итога; «|ИТ0ГО» — рамка графы
	if i := strings.IndexAny(w, ":;"); i > 0 {
		w = w[:i]
	}
	w = strings.TrimFunc(w, func(r rune) bool { return !unicode.IsLetter(r) && r != '0' })
	letters := 0
	for _, r := range w {
		if unicode.IsLetter(r) {
			letters++
		}
	}
	if letters < 3 {
		return false
	}
	norm := strings.ReplaceAll(strings.ToLower(latinLookalikes.Replace(w)), "ё", "е")
	if totalsTailWords[norm] || isGarbledTotalWord(norm) {
		return false
	}
	return true
}

// isGarbledTotalWord — «ИТОГО», изуродованное распознаванием: «ИТ0ГО» (ноль
// вместо О), «ОТОТИ» (буквы переставлены, одна заменена). Сравниваем по набору
// букв, а не по порядку: переставляет распознавание постоянно.
//
// Огрызок бывает и не из пяти букв («ИТГО», «ИТОГ0О», «ИТ0Г»), и от
// «ВСЕГО» латиницей («Bcero»: латинская r вместо г — одна заменённая буква, её
// покрывает допуск). Длина 4-6, сравнение с обоими словами.
var totalWordLookalikes = strings.NewReplacer("0", "о")

func isGarbledTotalWord(norm string) bool {
	norm = totalWordLookalikes.Replace(norm)
	if norm == "итого" || norm == "всего" || norm == "итог" {
		return true
	}
	n := len([]rune(norm))
	if n < 4 || n > 6 {
		return false
	}
	// одна заменённая буква даёт расхождение в 2 (одной не хватает, одна лишняя),
	// пропущенная или лишняя — в 1
	return letterDiff(norm, "итого") <= 2 || letterDiff(norm, "всего") <= 2
}

func letterDiff(a, b string) int {
	count := map[rune]int{}
	for _, r := range b {
		count[r]++
	}
	for _, r := range a {
		count[r]--
	}
	diff := 0
	for _, v := range count {
		if v < 0 {
			v = -v
		}
		diff += v
	}
	return diff
}

// Сумма прописью: числительное словом и «рубл»/«копе» в одной строке.
var spelledNumeralPrefixes = []string{
	"один", "одна", "одно", "два", "две", "три", "четыр", "пят", "шест", "сем",
	"восем", "девят", "десят", "одиннадц", "двенадц", "двадцат", "тридцат",
	"сорок", "пятьдес", "шестьдес", "семьдес", "восемьдес", "девяност", "двест",
	"трист", "четырест", "пятьсот", "шестьсот", "семьсот", "восемьсот",
	"девятьсот", "тысяч", "миллион",
}

func isSpelledAmount(s string) bool {
	numeral, currency := false, false
	for _, f := range strings.Fields(strings.ToLower(s)) {
		f = strings.Trim(f, ".,:;«»\"'()")
		if strings.HasPrefix(f, "рубл") || strings.HasPrefix(f, "копе") {
			currency = true
			continue
		}
		if f == "сто" {
			numeral = true
			continue
		}
		for _, p := range spelledNumeralPrefixes {
			if strings.HasPrefix(f, p) {
				numeral = true
				break
			}
		}
	}
	return numeral && currency
}

// sharesMoney — в двух строках есть одна и та же денежная сумма.
func sharesMoney(a, b []string) bool {
	var av []float64
	for _, c := range a {
		for _, f := range strings.Fields(c) {
			f = strings.Trim(f, ":;")
			if reMoneyWritten.MatchString(f) {
				if v, ok := cellNumber(f); ok && v > 0 {
					av = append(av, v)
				}
			}
		}
	}
	for _, c := range b {
		if !reMoneyWritten.MatchString(strings.TrimSpace(c)) {
			continue
		}
		v, ok := cellNumber(c)
		if !ok || v <= 0 {
			continue
		}
		for _, x := range av {
			if math.Abs(x-v) < 0.005 {
				return true
			}
		}
	}
	return false
}
