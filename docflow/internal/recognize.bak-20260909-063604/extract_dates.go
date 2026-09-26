package recognize

import (
	"regexp"
	"strings"
)

// Дата, написанная словами: «31 января 2023 г.», «"31" октября 2023г.»,
// «05» мая 2025 г. Точечную дату разбирает reDateDotted; сюда управление
// доходит только тогда, когда точечной даты в документе нет вовсе.
var reDateWord = regexp.MustCompile(`(?i)[«"]?\s*(\d{1,2})\s*[»"]?\s*(январ|феврал|март|апрел|ма[йя]|июн|июл|август|сентябр|октябр|ноябр|декабр)[а-яё]*\s+(\d{4})`)

var monthByPrefix = map[string]string{
	"январ": "01", "феврал": "02", "март": "03", "апрел": "04",
	"май": "05", "мая": "05", "июн": "06", "июл": "07",
	"август": "08", "сентябр": "09", "октябр": "10",
	"ноябр": "11", "декабр": "12",
}

// dateFromWords приводит дату прописью к тому же виду ДД.ММ.ГГГГ, в котором
// поле date отдаётся во всех остальных ветках. Пустая строка — не нашли.
func dateFromWords(text string) string {
	m := reDateWord.FindStringSubmatch(text)
	if m == nil {
		return ""
	}
	mon, ok := monthByPrefix[strings.ToLower(m[2])]
	if !ok {
		return ""
	}
	day := m[1]
	if len(day) == 1 {
		day = "0" + day
	}
	return day + "." + mon + "." + m[3]
}

// Широкий якорь номера: то же, что reAnchoredNumber, но окно 80 символов и
// переносы строк разрешены. В бланках название документа, подзаголовок и сам
// номер стоят тремя отдельными строками («СЧЕТ-ПРОТОКОЛ / согласования
// свободных договорных цен на услуги / № У-000032»), и строгий якорь до
// номера не достаёт. Запускается только после провала строгого.
var reWideAnchoredNumber = regexp.MustCompile(`(?i)(счёт|счет|счёт-фактура|счет-фактура|эсчф|накладная|тн|ттн|акт|упд|товарный чек|договор|заказ)[^№N]{0,80}(?:№|N|No)[\s:]*([0-9A-Za-zА-Яа-я\-/]+)`)

var (
	reIBANLike     = regexp.MustCompile(`(?i)^[A-Za-z]{2}\d{2}[A-Za-z]{4}\d{10,}$`)
	reAccountLabel = regexp.MustCompile(`(?i)(р\s*/\s*с|расч[а-яё]*\s+сч[её]т|сч[её]т\s+получателя)`)
)

// looksLikeAccountNumber — значение является банковским счётом, а не номером
// документа. На странице «р/с № BY29ALFA…» стоит выше заголовка, и его берут
// и запасная регулярка, и модель. Номер документа под этот формат не подходит
// никогда: он короткий и не начинается с кода страны и банка.
func looksLikeAccountNumber(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if reAccountLabel.MatchString(s) {
		return true
	}
	for _, tok := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ' ' || r == ',' || r == ';' || r == '\t' || r == '\n'
	}) {
		if reIBANLike.MatchString(strings.Trim(tok, "№:")) {
			return true
		}
	}
	return false
}
