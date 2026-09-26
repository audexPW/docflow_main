package recognize

import (
	"strings"

	"docflow/internal/domain"
)

// taxIDToken — «плейсхолдер» налогового идентификатора в списках обязательных
// полей. Он резолвится в конкретный ключ (unp для Беларуси, inn для России) в
// зависимости от локали, чтобы один и тот же перечень требований работал в обеих
// странах. См. SetLocale.
const taxIDToken = "@taxid"

// primaryTaxKey — фактический ключ налогового идентификатора для текущей локали.
var primaryTaxKey = "inn"

// SetLocale настраивает страновую специфику распознавания. Для "by" налоговый
// идентификатор — УНП (9 цифр), для остальных — ИНН. Вызывается один раз на
// старте из main по значению LOCALE.
func SetLocale(locale string) {
	if strings.EqualFold(strings.TrimSpace(locale), "by") {
		primaryTaxKey = "unp"
	} else {
		primaryTaxKey = "inn"
	}
}

// PrimaryTaxKey возвращает текущий ключ налогового идентификатора (unp|inn).
func PrimaryTaxKey() string { return primaryTaxKey }

// Обязательные реквизиты берутся из реестра типов (doctypes.go). Если все они
// распознаны — документ уходит в 1С автоматически; если чего-то не хватает —
// распознанное уходит частичной выгрузкой, а недостающее сотрудник вводит
// вручную (см. worker/recognizer.go и ТЗ, разделы 5–6, 10).
//
// taxIDToken вместо жёсткого "inn" — чтобы для белорусской конфигурации
// требовался УНП, а для российской — ИНН, без правки реестра.

// RequiredFields возвращает список обязательных полей для типа документа,
// резолвя налоговый идентификатор под текущую локаль. Для типа, которого нет в
// реестре (новая форма, которую принесла модель), берётся минимум из
// fallback_required — требовать сумму от незнакомого документа нельзя, он может
// быть доверенностью или актом приёмки.
func RequiredFields(docType string) []string {
	base := current().fallbackRequired
	if spec, ok := LookupDocType(docType); ok && len(spec.Required) > 0 {
		base = spec.Required
	}
	out := make([]string, len(base))
	for i, k := range base {
		if k == taxIDToken {
			out[i] = primaryTaxKey
		} else {
			out[i] = k
		}
	}
	return out
}

// MinTrustedConfidence — порог, ниже которого значение считается догадкой,
// а не распознанным реквизитом.
//
// Смысл в том, что «пусто» и «возможно неверно» для бухгалтерии одинаково
// непригодны, а второе опаснее: пустое поле сотрудник заполнит, а
// правдоподобно неверную сумму проведёт как есть. Поэтому такие поля
// приравниваются к отсутствующим: документ уходит в 1С частичной выгрузкой
// (как и любой недораспознанный), а значение сотрудник подтверждает руками.
const MinTrustedConfidence = 0.5

// MissingRequired возвращает обязательные поля, которые не заполнены либо
// заполнены значением, которому нельзя доверять без проверки человеком.
func MissingRequired(rec domain.Recognition) []string {
	var missing []string
	for _, k := range RequiredFields(rec.DocType) {
		f, ok := rec.Fields[k]
		switch {
		case !ok || strings.TrimSpace(f.Value) == "":
			missing = append(missing, k)
		case f.Source == "manual":
			// Введено человеком — доверяем безусловно.
		case f.Confidence < MinTrustedConfidence:
			missing = append(missing, k)
		}
	}
	return missing
}
