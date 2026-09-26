package recognize

import (
	"strings"

	"docflow/internal/domain"
)

// Склейка позиции, разорванной переносом наименования.
//
// Длинное наименование в бланке печатается в две-три строки, и OCR отдаёт
// каждую строку отдельно. Числа при этом стоят только в одной из них — там,
// где в документе напечатана сумма. В карточке это выглядело как две позиции:
// у одной есть название и нет чисел, у другой числа есть, а название —
// огрызок.
//
// Так и вышло со счётом-фактурой на пеню: «Пеня по коммунальным услугам с
// 21.08. по» осталось одной строкой, а «25.08.2023 | руб. | 5 | 0,35 | 0,35» —
// другой, и модель разбирала эту рванину.
//
// Прежняя проверка (isNameContinuation) ловила только хвост БЕЗ цифр и только
// СНИЗУ. Здесь обе стороны и цифры внутри наименования: признак обрывка не
// «нет цифр», а «не заполнена ни одна числовая графа».
func MergeNameFragments(ft *domain.FreeTable) {
	if ft == nil || len(ft.Rows) < 2 {
		return
	}
	roles := ft.Roles
	if len(roles) != len(ft.Columns) {
		roles = rolesFor(ft.Columns)
	}

	out := make([][]string, 0, len(ft.Rows))
	var pending []string // обрывки, ждущие строку с числами ниже
	lastFull := -1

	for _, row := range ft.Rows {
		if rowIsNameFragment(row, roles) {
			if lastFull >= 0 {
				appendNameCells(out[lastFull], row, roles)
			} else {
				pending = append(pending, nameText(row, roles))
			}
			continue
		}
		cp := append([]string(nil), row...)
		if len(pending) > 0 {
			prependName(cp, strings.Join(pending, " "), roles)
			pending = nil
		}
		out = append(out, cp)
		lastFull = len(out) - 1
	}

	// Обрывки, под которыми так и не нашлось строки с числами, не выбрасываем:
	// пусть лучше позиция уедет в карточку без сумм, чем текст исчезнет молча.
	if len(pending) > 0 {
		if lastFull >= 0 {
			appendNameText(out[lastFull], strings.Join(pending, " "), roles)
		} else {
			return // трогать нечего: все строки — обрывки, оставляем как было
		}
	}
	if len(out) > 0 {
		ft.Rows = out
	}
}

// rowIsNameFragment — в строке заполнена только текстовая графа, а все
// числовые (количество, цена, суммы, НДС, единица) пусты.
func rowIsNameFragment(row, roles []string) bool {
	name, other := false, false
	for i, v := range row {
		v = strings.TrimSpace(v)
		if v == "" || i >= len(roles) {
			continue
		}
		switch roles[i] {
		case "name", "", "other":
			// Безымянная графа слева от наименования — тоже текст: в неё
			// уезжает хвост длинного названия, когда OCR сдвигает перенос
			// левее колонки.
			name = true
		case "qty", "unit", "price", "amount", "vat", "vat_rate":
			other = true
		}
	}
	return name && !other
}

func nameText(row, roles []string) string {
	parts := make([]string, 0, 2)
	for i, v := range row {
		if i < len(roles) && (roles[i] == "name" || roles[i] == "" || roles[i] == "other") {
			if v = strings.TrimSpace(v); v != "" {
				parts = append(parts, v)
			}
		}
	}
	return strings.Join(parts, " ")
}

func appendNameCells(dst, src, roles []string) {
	appendNameText(dst, nameText(src, roles), roles)
}

func appendNameText(dst []string, tail string, roles []string) {
	if strings.TrimSpace(tail) == "" {
		return
	}
	for i := range dst {
		if i < len(roles) && roles[i] == "name" {
			dst[i] = strings.TrimSpace(strings.TrimSpace(dst[i]) + " " + tail)
			return
		}
	}
}

func prependName(dst []string, head string, roles []string) {
	if strings.TrimSpace(head) == "" {
		return
	}
	for i := range dst {
		if i < len(roles) && roles[i] == "name" {
			dst[i] = strings.TrimSpace(head + " " + strings.TrimSpace(dst[i]))
			return
		}
	}
}
