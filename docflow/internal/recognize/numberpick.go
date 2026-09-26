package recognize

// Номер документа: бланк против модели.
//
// Номер по месту на листе (HeaderNumberFromWords) стоит справа от названия
// документа или под ним, и на документах заказчика он надёжнее модели. Модель
// при расхождении приносит то, что номером не является: номер договора
// («23/23-КФ от 12.07.2023» вместо 666), слово с бланка («экземпляр»,
// «NHHILL» вместо 322), название формы («ТТН»). Поэтому при расхождении
// берётся бланк. NUMBER_PREFER_BLANK=false возвращает прежнее поведение.

import (
	"strings"

	"docflow/internal/domain"
)

// NumberPreferBlank — брать ли номер с бланка, когда модель или правила
// прочитали другой.
func NumberPreferBlank() bool { return envBool("NUMBER_PREFER_BLANK", true) }

func normDocNumber(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimLeft(s, "№#Nn ")
	return normForSearch(s)
}

// PreferBlankNumber ставит в поле number номер с бланка, если он найден и
// отличается от текущего. Возвращает прежнее значение, если оно заменено.
func PreferBlankNumber(rec *domain.Recognition, blank, fullText string) (replaced string, ok bool) {
	if rec == nil || !NumberPreferBlank() {
		return "", false
	}
	blank = strings.TrimSpace(blank)
	nb := normDocNumber(blank)
	if nb == "" {
		return "", false
	}
	if rec.Fields == nil {
		rec.Fields = map[string]domain.Field{}
	}
	cur := rec.Fields["number"]
	if cur.Source == "manual" {
		return "", false
	}
	nc := normDocNumber(cur.Value)
	if nc == nb {
		return "", false
	}
	// Бланк прочитан не целиком: «322» против «322/1», и длинный вариант есть
	// в тексте документа. Тогда прав длинный.
	if nc != "" && strings.HasPrefix(nc, nb) && strings.Contains(normForSearch(fullText), nc) {
		return "", false
	}
	rec.Fields["number"] = domain.Field{Value: blank, Confidence: ruleConfidence, Source: "rule"}
	if strings.TrimSpace(cur.Value) != "" {
		setNote(rec, "number", "номер взят с бланка; прочитано было «"+strings.TrimSpace(cur.Value)+"»")
	}
	return cur.Value, true
}
