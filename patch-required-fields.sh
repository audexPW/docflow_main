#!/usr/bin/env bash
# ============================================================================
#  DocFlow: правки по требованиям заказчика от 06.09.2026
#
#  1) Единый список обязательных реквизитов. Сейчас в doctypes.json
#     fallback_required = ["number","date"], а у типов своих списков нет —
#     отсюда и вопрос заказчика «почему количество обязательных полей меняется
#     от документа к документу». Ставим один перечень из 14 реквизитов.
#
#  2) Обязательные поля показываются ВСЕГДА, даже если распознавание их не
#     нашло, и подсвечиваются красным с пометкой «не распознано». Раньше в
#     карточку попадали только распознанные, и бухгалтеру приходилось сверять
#     её с оригиналом построчно, чтобы понять, чего не хватает.
#
#  3) Подписи полей только по-русски. В карточке светились ключи
#     contract_date и contract_number — их не было в словаре подписей.
#     Заодно разведены три суммы: «Сумма без НДС», «Сумма НДС», «Сумма с НДС»
#     вместо одной безымянной «Сумма».
#
#  4) Сумма НДС прописью. Заказчик показал счёт, где в таблице графы с НДС
#     нет вовсе, а под ней напечатано «в т.ч. НДС: Семь рублей 26 копеек».
#     Добавлен разбор сумм прописью: числительные, копейки цифрами и словами,
#     прилагательные валюты («Тридцать шесть белорусских рублей 78 копеек»).
#     Незнакомое слово внутри суммы — отказ, а не частичный разбор: «сорок»
#     вместо «сорок четыре» уехало бы в проводку молча.
#
#  5) Новый реквизит «Сумма без НДС» (amount_no_vat) с извлечением из текста.
#     Вычитанием из итога НЕ считаем: «Итого» в разных формах бывает и с
#     налогом, и без, и ошибка тут тоже уйдёт в проводку незаметно. Нет в
#     документе — поле пустое и красное, бухгалтер вносит руками, ровно как
#     просил заказчик.
#
#  ВАЖНО про автовыгрузку. Тот же список используется как условие
#  автоматической отправки в 1С: документ уходит сам, только когда заполнены
#  все обязательные поля. С 14 реквизитами автоматически будет уходить сильно
#  меньше документов, остальные встанут «На проверке» — это прямое следствие
#  требования заказчика, а не ошибка. Если нужно разделить «показывать» и
#  «блокировать выгрузку», перечень правится в doctypes.json без пересборки.
#
#  Запуск из /opt/docflow-deploy:  bash patch-required-fields.sh
# ============================================================================
set -euo pipefail
cd "$(dirname "$0")"

GO="docflow"
FE="docflow-frontend/src"
TS="$(date +%Y%m%d-%H%M%S)"

[ -f doctypes.json ] || { echo "Не вижу doctypes.json"; exit 1; }
[ -f "$FE/lib/labels.js" ] || { echo "Не вижу $FE/lib/labels.js"; exit 1; }

for f in doctypes.json "$FE/lib/labels.js" "$FE/pages/DocumentDetail.jsx" \
         "$FE/styles.css" "$GO/internal/recognize/extract.go"; do
  cp "$f" "$f.bak.req-$TS"
done

echo "→ 1/6 doctypes.json: единый список обязательных реквизитов"
python3 - <<'PY'
import io, json, collections
p = 'doctypes.json'
d = json.load(io.open(p, encoding='utf-8'), object_pairs_hook=collections.OrderedDict)
d['_comment_required'] = (
    "Обязательные реквизиты — единый список для всех типов (требование заказчика "
    "от 06.09.2026): перечень не должен меняться от документа к документу. "
    "@taxid разворачивается в unp для Беларуси. Поле, которого в документе нет, "
    "остаётся пустым и подсвечивается бухгалтеру — вычислять его арифметикой "
    "нельзя, ошибка уйдёт в проводку молча.")
d['fallback_required'] = ["account_credit", "account_debit", "counterparty", "@taxid",
                          "contract_number", "contract_date", "currency", "date", "number",
                          "organization", "organization_unp", "amount_no_vat",
                          "vat_amount", "total"]
io.open(p, 'w', encoding='utf-8').write(json.dumps(d, ensure_ascii=False, indent=2) + "\n")
print('   ok, реквизитов:', len(d['fallback_required']))
PY

echo "→ 2/6 words.go: разбор сумм прописью"
cat > "$GO/internal/recognize/words.go" <<'GOEOF'
package recognize

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Суммы прописью.
//
// В белорусских бланках итог и НДС часто печатаются под таблицей словами:
// «К оплате: Сорок четыре рубля 00 копеек, в т.ч. НДС: Семь рублей 26 копеек».
// Цифрами в самой таблице сумма НДС при этом может отсутствовать вовсе —
// заказчик показал именно такой счёт. Раньше поле оставалось пустым, хотя
// значение в документе есть, просто написано буквами.

var wordUnits = map[string]int{
	"ноль": 0, "нуль": 0,
	"один": 1, "одна": 1, "два": 2, "две": 2, "три": 3, "четыре": 4,
	"пять": 5, "шесть": 6, "семь": 7, "восемь": 8, "девять": 9,
	"десять": 10, "одиннадцать": 11, "двенадцать": 12, "тринадцать": 13,
	"четырнадцать": 14, "пятнадцать": 15, "шестнадцать": 16,
	"семнадцать": 17, "восемнадцать": 18, "девятнадцать": 19,
	"двадцать": 20, "тридцать": 30, "сорок": 40, "пятьдесят": 50,
	"шестьдесят": 60, "семьдесят": 70, "восемьдесят": 80, "девяносто": 90,
	"сто": 100, "двести": 200, "триста": 300, "четыреста": 400,
	"пятьсот": 500, "шестьсот": 600, "семьсот": 700,
	"восемьсот": 800, "девятьсот": 900,
}

// wordScales — множители разрядов. «Тысяча» и «миллион» во всех падежах и
// родах, как их печатают в бланках.
var wordScales = map[string]int{
	"тысяча": 1000, "тысячи": 1000, "тысяч": 1000, "тысячу": 1000,
	"миллион": 1000000, "миллиона": 1000000, "миллионов": 1000000,
}

// wordSkip — слова, которые встречаются внутри суммы прописью, но на значение
// не влияют: «Тридцать шесть белорусских рублей 78 копеек». Список закрытый и
// короткий намеренно — пропускать всё незнакомое нельзя, иначе «сорок мяу
// четыре» разберётся как 44 и уйдёт в бухгалтерию.
var wordSkip = map[string]bool{
	"белорусских": true, "белорусский": true, "белорусские": true,
	"российских": true, "российский": true, "российские": true,
	"и": true,
}

// reMoneyInWords — сумма прописью: рублёвая часть словами, копейки цифрами или
// словами. Копейки в бланках чаще цифрами («Семь рублей 26 копеек»), но
// встречается и полностью словесная запись.
var reMoneyInWords = regexp.MustCompile(
	`(?i)([а-яё]+(?:[\s-]+[а-яё]+){0,9})\s+рубл\S*\s*(\d{1,2}|[а-яё]+(?:\s+[а-яё]+)?)\s*коп\S*`)

// reVATWords — «в том числе НДС» с суммой прописью. Отдельно от reVATAmount:
// та ищет цифры и на словесной записи не срабатывает.
var reVATWords = regexp.MustCompile(
	`(?i)(?:в\s*т\.?\s*ч\.?\s*ндс|в\s+том\s+числе\s+ндс|в\s+т\.?\s*ч\.?\s+ндс)\s*[:\-]?\s*` +
		`([а-яё]+(?:[\s-]+[а-яё]+){0,8}\s+рубл\S*\s*(?:\d{1,2}|[а-яё]+(?:\s+[а-яё]+)?)\s*коп\S*)`)

// parseRussianInt переводит числительное прописью в число.
// Возвращает -1, если хотя бы одно слово не опознано: частичный разбор здесь
// опаснее отказа — «сорок» вместо «сорок четыре» уйдёт в бухгалтерию молча.
func parseRussianInt(s string) int {
	words := strings.Fields(strings.ToLower(strings.ReplaceAll(s, "-", " ")))
	if len(words) == 0 {
		return -1
	}
	total, group, seen := 0, 0, false
	for _, w := range words {
		w = strings.Trim(w, ".,:;()")
		if w == "" {
			continue
		}
		if wordSkip[w] {
			continue
		}
		if v, ok := wordUnits[w]; ok {
			group += v
			seen = true
			continue
		}
		if mul, ok := wordScales[w]; ok {
			if group == 0 {
				group = 1 // «тысяча рублей» без числительного перед ней
			}
			total += group * mul
			group = 0
			seen = true
			continue
		}
		return -1
	}
	if !seen {
		return -1
	}
	return total + group
}

// ParseMoneyWords разбирает сумму прописью в строку вида «44.00».
// Пустая строка — разобрать не удалось.
func ParseMoneyWords(s string) string {
	m := reMoneyInWords.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	rub := parseRussianInt(m[1])
	if rub < 0 {
		return ""
	}

	kop := 0
	k := strings.TrimSpace(m[2])
	if v, err := strconv.Atoi(k); err == nil {
		kop = v
	} else {
		kop = parseRussianInt(k)
	}
	if kop < 0 || kop > 99 {
		return ""
	}
	return fmt.Sprintf("%d.%02d", rub, kop)
}

// VATFromWords достаёт сумму НДС, напечатанную под таблицей прописью.
func VATFromWords(text string) string {
	m := reVATWords.FindStringSubmatch(text)
	if m == nil {
		return ""
	}
	return ParseMoneyWords(m[1])
}
GOEOF
cat > "$GO/internal/recognize/words_test.go" <<'GOEOF'
package recognize

import "testing"

func TestParseMoneyWords(t *testing.T) {
	cases := map[string]string{
		"Сорок четыре рубля 00 копеек":                "44.00",
		"Семь рублей 26 копеек":                       "7.26",
		"Тридцать шесть белорусских рублей 78 копеек": "36.78",
		"Сто шестьдесят девять рублей 44 копейки":     "169.44",
		"Двадцать шесть рублей 57 копеек":             "26.57",
		"Одна тысяча двести рублей 05 копеек":         "1200.05",
		"Шесть рублей тринадцать копеек":              "6.13",
		"Триста восемнадцать рублей 31 копейка":       "318.31",
	}
	for in, want := range cases {
		if got := ParseMoneyWords(in); got != want {
			t.Errorf("%q → %q, ожидалось %q", in, got, want)
		}
	}
}

func TestVATFromWords(t *testing.T) {
	text := "К оплате : Сорок четыре рубля 00 копеек\nв т.ч. НДС: Семь рублей 26 копеек\n"
	if got := VATFromWords(text); got != "7.26" {
		t.Errorf("НДС прописью не разобран: %q", got)
	}
	// Без пометки «в том числе НДС» брать первую попавшуюся сумму нельзя.
	if got := VATFromWords("К оплате: Сорок четыре рубля 00 копеек"); got != "" {
		t.Errorf("взята сумма не того назначения: %q", got)
	}
}

// Частичный разбор опаснее отказа: «сорок» вместо «сорок четыре» уйдёт в
// бухгалтерию молча и никто не заметит.
func TestUnknownWordRejects(t *testing.T) {
	if got := parseRussianInt("сорок мяу"); got != -1 {
		t.Errorf("неизвестное слово не отвергнуто: %d", got)
	}
	if got := ParseMoneyWords("Сорок мяу рубля 00 копеек"); got != "" {
		t.Errorf("испорченная строка разобрана: %q", got)
	}
}

// Поля, которые заказчик требует показывать всегда, должны заполняться и
// когда сумма напечатана под таблицей словами.
func TestExtractVATFromWordsInDocument(t *testing.T) {
	text := "Предмет счета Количество Тариф Сумма б/НДС\n" +
		"Услуги по обращению с ТКО 0,04 1 0,04 20\n" +
		"ИТОГО к оплате: 44.00\n" +
		"К оплате : Сорок четыре рубля 00 копеек\n" +
		"в т.ч. НДС: Семь рублей 26 копеек\n"
	rec := RecognizeDocument(text, text)
	f, ok := rec.Fields["vat_amount"]
	if !ok || f.Value != "7.26" {
		t.Errorf("сумма НДС прописью не попала в поля: %+v", rec.Fields["vat_amount"])
	}
}

func TestExtractAmountNoVAT(t *testing.T) {
	text := "Стоимость - всего без НДС, руб.  134,51\nСумма НДС, руб. 26,90\n"
	rec := RecognizeDocument(text, text)
	if f := rec.Fields["amount_no_vat"]; f.Value != "134.51" {
		t.Errorf("сумма без НДС: %q", f.Value)
	}
}
GOEOF
echo "   ok"

echo "→ 3/6 extract.go: НДС прописью и сумма без НДС"
python3 - "$GO" <<'PY'
import sys, io, os
p = os.path.join(sys.argv[1], 'internal/recognize/extract.go')
s = io.open(p, encoding='utf-8').read()
if 'VATFromWords' in s:
    print('   уже применено'); raise SystemExit

old = '''	if m := reVATAmount.FindStringSubmatch(ocrText); m != nil {
		fields["vat_amount"] = ruleField(normalizeMoney(m[1]))
	}'''
new = '''	if m := reVATAmount.FindStringSubmatch(ocrText); m != nil {
		fields["vat_amount"] = ruleField(normalizeMoney(m[1]))
	} else if v := VATFromWords(ocrText); v != "" {
		// Сумма НДС прописью под таблицей: «в т.ч. НДС: Семь рублей 26
		// копеек». В самой таблице отдельной графы с НДС может не быть вовсе —
		// заказчик показал именно такой счёт, и поле оставалось пустым, хотя
		// значение в документе есть, просто написано буквами.
		fields["vat_amount"] = ruleField(v)
	}
	if m := reAmountNoVAT.FindStringSubmatch(ocrText); m != nil {
		fields["amount_no_vat"] = ruleField(normalizeMoney(m[1]))
	}'''
if old not in s:
    raise SystemExit('   не найден якорь reVATAmount в extract.go')
s = s.replace(old, new, 1)

old = '\treVATAmount = regexp.MustCompile('
new = '''\t// Сумма без НДС отдельным реквизитом: заказчик требует все три суммы —
\t// без НДС, НДС и с НДС — независимо от того, есть ли они в бланке.
\t// Не вычисляем вычитанием: «Итого» в разных формах бывает и с налогом, и
\t// без, и ошибка тут молча уедет в проводку. Нет в документе — поле
\t// остаётся пустым и подсвечивается бухгалтеру.
\treAmountNoVAT = regexp.MustCompile(`(?i)(?:сумма|стоимость|итого|всего)\\s+без\\s+ндс[^\\d\\n]{0,20}([\\d\\s]+[.,]\\d{2})`)

\treVATAmount = regexp.MustCompile('''
if old not in s:
    raise SystemExit('   не найдено объявление reVATAmount')
s = s.replace(old, new, 1)
io.open(p, 'w', encoding='utf-8').write(s)
print('   ok')
PY

echo "→ 4/6 labels.js: подписи только по-русски"
python3 - "$FE" <<'PY'
import sys, io, os
p = os.path.join(sys.argv[1], 'lib/labels.js')
s = io.open(p, encoding='utf-8').read()
if 'amount_no_vat' in s:
    print('   уже применено'); raise SystemExit
old = "  vat_amount: 'Сумма НДС',"
new = """  vat_amount: 'Сумма НДС',
  amount_no_vat: 'Сумма без НДС',
  contract_number: 'Номер договора',
  contract_date: 'Дата договора',"""
if old not in s:
    raise SystemExit('   не найден якорь vat_amount в labels.js')
s = s.replace(old, new, 1)
# Безымянная «Сумма» путала: заказчик требует различать три суммы.
s = s.replace("  total: 'Сумма',", "  total: 'Сумма с НДС',", 1)
s = s.replace("  date: 'Дата',", "  date: 'Дата документа',", 1)
s = s.replace("  number: 'Номер',", "  number: 'Номер документа',", 1)
s = s.replace("  unp: 'УНП',", "  unp: 'УНП контрагента',", 1)
s = s.replace("  inn: 'ИНН',", "  inn: 'ИНН контрагента',", 1)
io.open(p, 'w', encoding='utf-8').write(s)
print('   ok')
PY

echo "→ 5/6 DocumentDetail.jsx: показывать все реквизиты, недостающие — красным"
python3 - "$FE" <<'PY'
import sys, io, os
p = os.path.join(sys.argv[1], 'pages/DocumentDetail.jsx')
s = io.open(p, encoding='utf-8').read()
if 'field-missing' in s:
    print('   уже применено'); raise SystemExit

old = """    const list = rec?.fields
      ? Object.entries(rec.fields).map(([key, f]) => ({
          key,
          value: f.value ?? '',
          confidence: f.confidence,
          source: f.source,
        }))
      : []
    // Стабильный порядок полей
    list.sort((a, b) => a.key.localeCompare(b.key))
    setFields(list)"""
new = """    const list = rec?.fields
      ? Object.entries(rec.fields).map(([key, f]) => ({
          key,
          value: f.value ?? '',
          confidence: f.confidence,
          source: f.source,
        }))
      : []

    // Обязательные реквизиты показываем всегда, даже если распознавание их не
    // нашло: бухгалтеру нужен один и тот же набор полей в любом документе, а
    // не тот, что получился. Недостающие добавляем пустыми — ниже они идут
    // красным, и их вносят руками.
    const seen = new Set(list.map((f) => f.key))
    for (const k of rec?.missing || []) {
      if (!seen.has(k)) {
        list.push({ key: k, value: '', confidence: undefined, source: undefined })
        seen.add(k)
      }
    }

    // Порядок как в бухгалтерской карточке, а не по алфавиту: реквизиты
    // документа, стороны, договор, суммы, счета учёта.
    const order = [
      'date', 'number', 'currency',
      'counterparty', 'unp', 'inn',
      'organization', 'organization_unp', 'organization_inn',
      'contract_number', 'contract_date',
      'amount_no_vat', 'vat_amount', 'total',
      'account_debit', 'account_credit', 'account_vat',
    ]
    const rank = (k) => {
      const i = order.indexOf(k)
      return i === -1 ? order.length : i
    }
    list.sort((a, b) => rank(a.key) - rank(b.key) || a.key.localeCompare(b.key))
    setFields(list)"""
if old not in s:
    raise SystemExit('   не найден якорь построения списка полей')
s = s.replace(old, new, 1)

old = """                {fields.map((f, idx) => (
                  <tr key={f.key}>
                    <td>
                      {fieldLabel(f.key)}"""
new = """                {fields.map((f, idx) => (
                  <tr key={f.key} className={missing.includes(f.key) ? 'field-missing' : undefined}>
                    <td>
                      {fieldLabel(f.key)}
                      {missing.includes(f.key) && (
                        <span className="conf field-missing-mark" title="Обязательный реквизит не распознан — заполните вручную">
                          не распознано
                        </span>
                      )}"""
if old not in s:
    raise SystemExit('   не найден якорь отрисовки строк полей')
s = s.replace(old, new, 1)
io.open(p, 'w', encoding='utf-8').write(s)
print('   ok')
PY

echo "→ 6/6 styles.css: подсветка"
if grep -q "field-missing" "$FE/styles.css"; then
  echo "   уже применено"
else
cat >> "$FE/styles.css" <<'CSSEOF'

/* Обязательный реквизит, который распознавание не нашло. Заказчик просил
   подсвечивать такие поля, чтобы бухгалтер сразу видел, что вносить руками,
   а не сверял карточку с оригиналом построчно. */
.field-missing td {
  background: #fdf3f3;
}
.field-missing td:first-child {
  border-left: 3px solid var(--danger);
}
.field-missing input {
  border-color: #d3a3a3;
}
.field-missing-mark {
  color: var(--danger);
}
CSSEOF
echo "   ok"
fi

python3 -c "import json,io; json.load(io.open('doctypes.json',encoding='utf-8')); print('doctypes.json — валидный JSON')"

if command -v go >/dev/null 2>&1; then
  (cd "$GO" && gofmt -l internal/recognize/ && go vet ./internal/recognize/ && go test ./internal/recognize/) || {
    echo; echo "Не собралось. Откат:"
    echo "  for f in doctypes.json $FE/lib/labels.js $FE/pages/DocumentDetail.jsx $FE/styles.css $GO/internal/recognize/extract.go; do cp \"\$f.bak.req-$TS\" \"\$f\"; done"
    exit 1; }
fi

docker compose up -d --build docflow-backend frontend

cat <<MSG

Готово. Бэкапы: *.bak.req-$TS

Проверить:
  1) Открыть любой документ — в карточке должны быть все 14 реквизитов,
     недостающие красные с пометкой «не распознано».
  2) Подписи только по-русски: «Номер договора», «Дата договора»,
     «Сумма без НДС», «Сумма НДС», «Сумма с НДС».
  3) Счёт с НДС прописью — поле «Сумма НДС» должно заполниться из строки
     «в т.ч. НДС: Семь рублей 26 копеек».

  Страницу обновить жёстко: Ctrl+Shift+R.

Учтите: с 14 обязательными реквизитами автоматически в 1С будет уходить
заметно меньше документов — остальные встанут «На проверке». Это следствие
самого требования. Перечень правится в doctypes.json без пересборки:
после правки  docker compose restart docflow-backend
MSG
