#!/usr/bin/env bash
# ---------------------------------------------------------------------------
# DocFlow: дата прописью + номер документа мимо банковского счёта.
#
# Три изменения, каждое срабатывает только там, где сейчас пусто или мусор:
#   1) дата «31 января 2023 г.» — ТОЛЬКО если reDateDotted ничего не нашёл;
#   2) широкий якорь номера     — ТОЛЬКО если строгий якорь не сработал;
#   3) фильтр «р/с»/IBAN        — отбрасывает номер, похожий на счёт в банке,
#                                 в том числе присланный моделью с conf 0.85.
# На документах, где эти поля уже определяются, поведение не меняется.
# ---------------------------------------------------------------------------
set -euo pipefail

ROOT="${1:-/opt/docflow-deploy}"
SRC="$ROOT/docflow/internal/recognize"
PIPE="$ROOT/docflow/internal/recognize/pipeline.go"
EXTR="$SRC/extract.go"
NEW="$SRC/extract_dates.go"
TS="$(date +%Y%m%d-%H%M%S)"

[ -f "$EXTR" ] || { echo "не найден $EXTR — укажи путь: $0 /путь/к/docflow-deploy"; exit 1; }
[ -f "$PIPE" ] || { echo "не найден $PIPE"; exit 1; }

if [ -f "$NEW" ]; then
  echo "ОТМЕНА: $NEW уже существует — патч, похоже, применён. Удали файл, если хочешь наложить заново."
  exit 1
fi

cp -a "$EXTR" "$EXTR.bak-datenum-$TS"
cp -a "$PIPE" "$PIPE.bak-datenum-$TS"
echo "бэкапы: *.bak-datenum-$TS"

# --- 1. Новый файл с регулярками и хелперами -------------------------------
cat > "$NEW" <<'GOFILE'
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
GOFILE
echo "создан $NEW"

# --- 2, 3, 4. Точечные правки существующих файлов --------------------------
python3 - "$EXTR" "$PIPE" <<'PY'
import sys

extr_path, pipe_path = sys.argv[1], sys.argv[2]

def patch(path, old, new, label):
    src = open(path, encoding='utf-8').read()
    if src.count(old) != 1:
        sys.exit("ОТМЕНА (%s): якорь найден %d раз, ожидалась 1. Файл не изменён." % (label, src.count(old)))
    open(path, 'w', encoding='utf-8').write(src.replace(old, new, 1))
    print("применено:", label)

# 2. Дата прописью — строго в ветку else, точечная дата приоритетнее.
patch(extr_path,
"""	if m := reDateDotted.FindStringSubmatch(ocrText); m != nil {
		fields["date"] = ruleField(m[1])
	}""",
"""	if m := reDateDotted.FindStringSubmatch(ocrText); m != nil {
		fields["date"] = ruleField(m[1])
	} else if d := dateFromWords(ocrText); d != "" {
		// Точечной даты в бланке нет — пробуем дату прописью. Порядок веток
		// важен: где ДД.ММ.ГГГГ есть, поведение остаётся прежним.
		fields["date"] = ruleField(d)
	}""",
"дата прописью (extract.go)")

# 3. Широкий якорь + фильтр счёта в запасном пути.
patch(extr_path,
"""	// Единственный найденный номер — договорный: значит это и есть договор.
	if anchoredContract != "" {
		return anchoredContract, ruleConfidence
	}

	for _, m := range reDocNumber.FindAllStringSubmatch(text, -1) {
		num := strings.Trim(m[1], "-/")
		if num == "" || (contract != "" && num == contract) {
			continue
		}
		return num, guessConfidence
	}""",
"""	// Строгий якорь не достал до номера: в бланках заголовок, подзаголовок и
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
	}""",
"широкий якорь номера (extract.go)")

# 4. Модель не должна затирать номер банковским счётом.
patch(pipe_path,
"""		if strings.HasPrefix(k, "account_") && AccountsEnabled() && !KnownAccount(sv) {
			p.log.Warn("модель вернула счёт вне плана счетов", "field", k, "value", sv)
			continue
		}""",
"""		if strings.HasPrefix(k, "account_") && AccountsEnabled() && !KnownAccount(sv) {
			p.log.Warn("модель вернула счёт вне плана счетов", "field", k, "value", sv)
			continue
		}
		// Поля модели идут с уверенностью 0.85 и молча перекрывают правила.
		// В номере документа модель иногда отдаёт банковский счёт из шапки
		// («р/с № BY29ALFA…»): выглядит достоверно, а в 1С уходит мусор.
		// Отбрасываем — останется номер, найденный правилами.
		if k == "number" && looksLikeAccountNumber(sv) {
			p.log.Warn("модель вернула в номере банковский счёт, значение отброшено", "value", sv)
			continue
		}""",
"фильтр банковского счёта (pipeline.go)")
PY

echo
echo "Готово. Пересобрать бэкенд:"
echo "  cd $ROOT && docker compose up -d --build docflow-backend"
echo
echo "Откатить:"
echo "  rm $NEW"
echo "  mv $EXTR.bak-datenum-$TS $EXTR"
echo "  mv $PIPE.bak-datenum-$TS $PIPE"
