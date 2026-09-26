// Package parties — приведение наименований юрлиц к сравнимому виду.
//
// Одна и та же компания приезжает в документах пятью строками: «Общество с
// ограниченной ответственностью «КофеВенд»», «ООО КофеВенд», «ООО Кофевенд»,
// «ОАО КофеВенд». В 1С из этого получалось пять контрагентов вместо одного.
// Здесь наименование раскладывается на форму собственности и собственно имя,
// и сравнение идёт по имени. Ключом справочника при этом остаётся УНП, а имя —
// только способ найти запись, когда номера на листе нет (акты аренды, где УНП
// стоит лишь на оттиске печати).
//
// Пакет без зависимостей: им пользуются и распознавание, и хранилище.
package parties

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// longForms — полные названия форм собственности и их сокращения. Порядок
// важен: длинные формулировки проверяются раньше вложенных в них коротких
// («частное торговое унитарное предприятие» раньше «унитарное предприятие»).
var longForms = []struct{ long, short string }{
	{"общество с дополнительной ответственностью", "ОДО"},
	{"общество с ограниченной ответственностью", "ООО"},
	{"совместное общество с ограниченной ответственностью", "СООО"},
	{"иностранное общество с ограниченной ответственностью", "ИООО"},
	{"открытое акционерное общество", "ОАО"},
	{"закрытое акционерное общество", "ЗАО"},
	{"публичное акционерное общество", "ПАО"},
	{"акционерное общество", "АО"},
	{"частное производственное унитарное предприятие", "ЧПУП"},
	{"частное торговое унитарное предприятие", "ЧТУП"},
	{"частное унитарное предприятие", "ЧУП"},
	{"коммунальное унитарное предприятие", "КУП"},
	{"коммунальное производственное унитарное предприятие", "КПУП"},
	{"республиканское унитарное предприятие", "РУП"},
	{"иностранное унитарное предприятие", "ИУП"},
	{"производственное унитарное предприятие", "ПУП"},
	{"унитарное предприятие", "УП"},
	{"учреждение здравоохранения", "УЗ"},
	{"государственное учреждение образования", "ГУО"},
	{"государственное учреждение", "ГУ"},
	{"индивидуальный предприниматель", "ИП"},
}

func init() {
	// Длинные формулировки — раньше вложенных в них коротких: «совместное
	// общество с ограниченной ответственностью» не должно распознаться как ООО
	// с лишним словом «совместное» в имени.
	sort.SliceStable(longForms, func(i, j int) bool {
		return len(longForms[i].long) > len(longForms[j].long)
	})
}

var shortForms = map[string]bool{
	"ооо": true, "одо": true, "сооо": true, "иооо": true, "оао": true, "зао": true,
	"пао": true, "ао": true, "чуп": true, "чтуп": true, "чпуп": true, "куп": true,
	"кпуп": true, "руп": true, "иуп": true, "пуп": true, "уп": true, "уз": true,
	"гу": true, "гуо": true, "ип": true,
}

var reSpaces = regexp.MustCompile(`\s+`)

// Split раскладывает наименование на форму собственности (сокращением, в
// верхнем регистре) и имя без кавычек. Формы нет — form пустая.
func Split(name string) (form, core string) {
	s := strings.TrimSpace(reSpaces.ReplaceAllString(name, " "))
	low := strings.ToLower(strings.ReplaceAll(s, "ё", "е"))
	for _, lf := range longForms {
		if i := strings.Index(low, lf.long); i >= 0 {
			form = lf.short
			s = strings.TrimSpace(s[:i] + " " + s[i+len(lf.long):])
			low = strings.ToLower(strings.ReplaceAll(s, "ё", "е"))
			break
		}
	}
	fields := strings.Fields(s)
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		bare := strings.Trim(strings.ToLower(f), "«»\"'„“”.,")
		if form == "" && shortForms[bare] {
			form = strings.ToUpper(bare)
			continue
		}
		if shortForms[bare] && strings.EqualFold(bare, form) {
			continue
		}
		out = append(out, f)
	}
	core = strings.TrimSpace(strings.Join(out, " "))
	core = strings.Trim(core, " «»\"'„“”,.")
	core = strings.NewReplacer("«", "", "»", "", "\"", "", "„", "", "“", "", "”", "").Replace(core)
	return form, strings.TrimSpace(reSpaces.ReplaceAllString(core, " "))
}

// Key — ключ сравнения: имя без формы собственности, кавычек, регистра и
// знаков препинания. «ООО «КофеВенд»» и «ОАО Кофевенд» дают один ключ — а то,
// что форма у второго неверная, решает справочник по УНП, а не сравнение.
func Key(name string) string {
	_, core := Split(name)
	var b strings.Builder
	for _, r := range strings.ToLower(strings.ReplaceAll(core, "ё", "е")) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Same — две строки называют одно юрлицо (по имени, без учёта формы).
func Same(a, b string) bool {
	ka, kb := Key(a), Key(b)
	if ka == "" || kb == "" {
		return false
	}
	if ka == kb {
		return true
	}
	// «Минская центральная районная клиническая больница» против того же с
	// хвостом адреса: короткий ключ целиком внутри длинного. Не меньше шести
	// букв, иначе «Мир» совпадёт со всем подряд.
	short, long := ka, kb
	if len([]rune(short)) > len([]rune(long)) {
		short, long = long, short
	}
	return len([]rune(short)) >= 6 && strings.Contains(long, short)
}

// CountForms — сколько форм собственности названо в строке. Больше одной —
// в поле склеены два юрлица («ООО «КофеВенд» ЗАО «Суперпрод»»).
func CountForms(name string) int {
	low := " " + strings.ToLower(strings.ReplaceAll(name, "ё", "е")) + " "
	n := 0
	for _, lf := range longForms {
		for {
			i := strings.Index(low, lf.long)
			if i < 0 {
				break
			}
			n++
			low = low[:i] + " " + low[i+len(lf.long):]
		}
	}
	for _, f := range strings.FieldsFunc(low, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r))
	}) {
		if shortForms[f] {
			n++
		}
	}
	return n
}

// SplitEntities режет строку со склеенными юрлицами по началу каждой формы
// собственности. Одно юрлицо — возвращается как есть.
func SplitEntities(name string) []string {
	if CountForms(name) < 2 {
		return []string{strings.TrimSpace(name)}
	}
	low := strings.ToLower(strings.ReplaceAll(name, "ё", "е"))
	var cuts []int
	for _, lf := range longForms {
		from := 0
		for {
			i := strings.Index(low[from:], lf.long)
			if i < 0 {
				break
			}
			cuts = append(cuts, from+i)
			from += i + len(lf.long)
		}
	}
	// Короткие формы — только отдельным словом.
	for i := 0; i < len(low); {
		j := i
		for j < len(low) && (low[j] == ' ' || low[j] == ',' || low[j] == '"') {
			j++
		}
		k := j
		for k < len(low) && low[k] != ' ' && low[k] != '"' && low[k] != ',' {
			k++
		}
		if k > j {
			w := strings.Trim(low[j:k], "«»\"'.")
			if shortForms[w] {
				cuts = append(cuts, j)
			}
		}
		if k == i {
			k++
		}
		i = k
	}
	sort.Ints(cuts)
	var out []string
	prev := -1
	for _, c := range cuts {
		if prev >= 0 && c-prev < 3 {
			continue
		}
		if prev >= 0 {
			if part := strings.TrimSpace(name[prev:c]); part != "" {
				out = append(out, strings.Trim(part, " ,;"))
			}
		} else if head := strings.TrimSpace(name[:c]); head != "" {
			out = append(out, strings.Trim(head, " ,;"))
		}
		prev = c
	}
	if prev >= 0 {
		if part := strings.TrimSpace(name[prev:]); part != "" {
			out = append(out, strings.Trim(part, " ,;"))
		}
	}
	if len(out) == 0 {
		return []string{strings.TrimSpace(name)}
	}
	return out
}

// ValidUNP — белорусский УНП: ровно девять цифр.
func ValidUNP(s string) bool {
	if len(s) != 9 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
