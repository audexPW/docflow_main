#!/usr/bin/env bash
# ============================================================================
#  DocFlow — патч по итогам аудита 08.09.2026
#
#  Запускать из /opt/docflow-deploy. Идемпотентно: повторный запуск сообщает
#  «уже применено» и ничего не портит. Каждый изменённый файл бэкапится рядом
#  с меткой времени. Откат — в конце вывода.
#
#  Шаги независимы, можно выполнить только часть:
#      ./patch-docflow.sh llm      — только контекст модели (самое важное)
#      ./patch-docflow.sh prompt   — только обрезка промпта по рунам
#      ./patch-docflow.sh xml      — только теги XML
#      ./patch-docflow.sh          — всё
# ============================================================================
set -euo pipefail

TS="$(date +%Y%m%d-%H%M%S)"
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

C_OK=$'\033[1;32m'; C_SKIP=$'\033[1;33m'; C_HDR=$'\033[1;36m'; C_ERR=$'\033[1;31m'; C_0=$'\033[0m'
hdr()  { printf '%s==>%s %s\n' "$C_HDR"  "$C_0" "$*"; }
ok()   { printf '%s  +%s %s\n' "$C_OK"   "$C_0" "$*"; }
skip() { printf '%s  .%s %s\n' "$C_SKIP" "$C_0" "$*"; }
die()  { printf '%s  x%s %s\n' "$C_ERR"  "$C_0" "$*" >&2; exit 1; }

backup() { cp -a "$1" "$1.bak-audit-$TS"; }

for f in .env docker-compose.yml docflow/internal/onec/file.go \
         docflow/internal/onec/onec.go docflow/internal/recognize/pipeline.go; do
  [[ -f "$f" ]] || die "нет $f — запускайте из /opt/docflow-deploy"
done

WHAT="${1:-all}"

# ---------------------------------------------------------------------------
# ШАГ 1. Контекст модели.
#
# LLM_MAX_TOKENS=5000 при LLM_CTX=4096 означает, что лимит генерации больше
# всего окна. Промпт (6000 симв. OCR + 3000 симв. сетки + схема) занимает
# ~4000-4500 токенов сам по себе. llama.cpp начинает сдвигать контекст прямо
# во время ответа: JSON обрывается на середине, ключи теряются, а
# decodeJSONObject («первая { … последняя }») склеивает огрызок.
#
# Самая вероятная причина и «кривых полей», и «пропадающих тегов».
# ---------------------------------------------------------------------------
if [[ "$WHAT" == all || "$WHAT" == llm ]]; then
hdr "1/4  контекст LLM"

if grep -qxF 'LLM_CTX=8192' .env && grep -qxF 'LLM_MAX_TOKENS=2048' .env; then
  skip ".env уже поправлен"
else
  backup .env
  sed -i -E 's/^LLM_CTX=.*/LLM_CTX=8192/'               .env
  sed -i -E 's/^LLM_MAX_TOKENS=.*/LLM_MAX_TOKENS=2048/' .env
  grep -q '^LLM_CTX='        .env || echo 'LLM_CTX=8192'        >> .env
  grep -q '^LLM_MAX_TOKENS=' .env || echo 'LLM_MAX_TOKENS=2048' >> .env
  ok "LLM_CTX 4096 -> 8192, LLM_MAX_TOKENS 5000 -> 2048"
fi

if grep -q 'LLM_CTX:-8192' docker-compose.yml; then
  skip "compose уже поправлен"
else
  backup docker-compose.yml
  sed -i 's/-c \${LLM_CTX:-4096}/-c ${LLM_CTX:-8192}/' docker-compose.yml
  ok "дефолт -c поднят до 8192"
fi

# KV-кэш 7B Q4_K_M при 8k контекста ~1 ГБ сверх весов (4.4 ГБ).
# При LLM_MEM_LIMIT=8g запас есть. Выше 8k — только вместе с лимитом.
echo "      текущий лимит: $(grep -E '^LLM_MEM_LIMIT=' .env || echo 'LLM_MEM_LIMIT не задан (дефолт 6g)')"
fi

# ---------------------------------------------------------------------------
# ШАГ 2. Обрезка промпта по рунам.
#
# ocrText[:6000] режет по БАЙТАМ. Кириллица в UTF-8 — два байта на букву,
# поэтому обрезка почти всегда рвёт руну пополам и отдаёт модели битый хвост.
# ---------------------------------------------------------------------------
if [[ "$WHAT" == all || "$WHAT" == prompt ]]; then
hdr "2/4  обрезка промпта по рунам"

PIPE=docflow/internal/recognize/pipeline.go
if grep -q 'func truncRunes' "$PIPE"; then
  skip "уже применено"
else
  backup "$PIPE"
  python3 - "$PIPE" <<'PY'
import io, sys
p = sys.argv[1]
s = io.open(p, encoding='utf-8').read()

old_ocr = "\tconst maxChars = 6000\n\tif len(ocrText) > maxChars {\n\t\tocrText = ocrText[:maxChars]\n\t}\n"
new_ocr = "\tconst maxChars = 6000\n\tocrText = truncRunes(ocrText, maxChars)\n"
if s.count(old_ocr) != 1:
    sys.exit("якорь обрезки ocrText не найден или неоднозначен")
s = s.replace(old_ocr, new_ocr)

old_grid = "\t\tconst maxGrid = 3000\n\t\tif len(tableGrid) > maxGrid {\n\t\t\ttableGrid = tableGrid[:maxGrid]\n\t\t}\n"
new_grid = "\t\tconst maxGrid = 3000\n\t\ttableGrid = truncRunes(tableGrid, maxGrid)\n"
if s.count(old_grid) == 1:
    s = s.replace(old_grid, new_grid)

s += '''
// truncRunes режет строку по рунам, а не по байтам. Прежняя обрезка
// ocrText[:6000] рвала кириллическую руну пополам: модель получала битый
// UTF-8 в хвосте промпта.
func truncRunes(s string, max int) string {
\tif max <= 0 {
\t\treturn s
\t}
\tn := 0
\tfor i := range s {
\t\tif n == max {
\t\t\treturn s[:i]
\t\t}
\t\tn++
\t}
\treturn s
}
'''
io.open(p, 'w', encoding='utf-8').write(s)
PY
  ok "truncRunes добавлен, обрезка ocrText и tableGrid переведена на руны"
fi
fi

# ---------------------------------------------------------------------------
# ШАГ 3. XML: фиксированная схема + недостающие теги.
#
# 3a. omitempty на реквизитах шапки: нераспознанное поле ИСЧЕЗАЕТ из XML.
#     Для приёмника это неотличимо от смены схемы. Должно быть наоборот:
#     тег есть всегда, пусто — значит пусто.
#
# 3b. amount_no_vat, contract_number, contract_date объявлены обязательными
#     в doctypes.json, но своих тегов не имеют — проваливаются в
#     <AdditionalFields>. Дмитрий получает номер договора безымянной строкой,
#     хотя это второе субконто счетов 60.1.x.
#
# ВНИМАНИЕ: схема XML меняется. Предупредите Дмитрия ДО выкатки.
# ---------------------------------------------------------------------------
if [[ "$WHAT" == all || "$WHAT" == xml ]]; then
hdr "3/4  XML: фиксированная схема, договор явными тегами"

FILEGO=docflow/internal/onec/file.go
ONECGO=docflow/internal/onec/onec.go

if grep -q 'ContractNumber string' "$FILEGO"; then
  skip "уже применено"
else
  backup "$FILEGO"; backup "$ONECGO"
  python3 - "$FILEGO" "$ONECGO" <<'PY'
import io, sys
fp, op = sys.argv[1], sys.argv[2]

s = io.open(fp, encoding='utf-8').read()

fixed = {
 '\tNumber       string    `xml:"Number,omitempty"`':          '\tNumber       string    `xml:"Number"`',
 '\tDate         string    `xml:"Date,omitempty"`':            '\tDate         string    `xml:"Date"`',
 '\tTotal        string    `xml:"Total,omitempty"`':           '\tTotal        string    `xml:"Total"`',
 '\tVATAmount    string    `xml:"VATAmount,omitempty"`':       '\tVATAmount    string    `xml:"VATAmount"`',
 '\tCurrency     string    `xml:"Currency,omitempty"`':        '\tCurrency     string    `xml:"Currency"`',
 '\tOrganization string    `xml:"Organization,omitempty"`':    '\tOrganization string    `xml:"Organization"`',
 '\tOrgTaxID     string    `xml:"OrganizationUNP,omitempty"`': '\tOrgTaxID     string    `xml:"OrganizationUNP"`',
}
for a, b in fixed.items():
    if s.count(a) != 1:
        sys.exit("якорь не найден в file.go: " + a.strip())
    s = s.replace(a, b)

anchor = '\tCurrency     string    `xml:"Currency"`\n'
s = s.replace(anchor, anchor +
 '\t// AmountNoVAT и Contract* объявлены обязательными в doctypes.json, значит\n'
 '\t// должны быть явными тегами, а не теряться в AdditionalFields. Договор —\n'
 '\t// второе субконто счетов 60.1.x в конфигурации заказчика.\n'
 '\tAmountNoVAT    string `xml:"AmountNoVAT"`\n'
 '\tContractNumber string `xml:"ContractNumber"`\n'
 '\tContractDate   string `xml:"ContractDate"`\n', 1)

a2 = '\t\t\t\tCurrency:     p.Currency,\n'
if s.count(a2) != 1:
    sys.exit("якорь Currency в toEnterpriseData не найден")
s = s.replace(a2, a2 +
 '\t\t\t\tAmountNoVAT:    p.AmountNoVAT,\n'
 '\t\t\t\tContractNumber: p.ContractNumber,\n'
 '\t\t\t\tContractDate:   p.ContractDate,\n', 1)

a3 = '\t\t"counterparty": true, "organization": true, "organization_unp": true,\n'
if s.count(a3) != 1:
    sys.exit("якорь known-карты не найден")
s = s.replace(a3, a3 +
 '\t\t"amount_no_vat": true, "contract_number": true, "contract_date": true,\n', 1)

io.open(fp, 'w', encoding='utf-8').write(s)

s = io.open(op, encoding='utf-8').read()
a4 = '\tCurrency     string   `json:"currency,omitempty"`\n'
if s.count(a4) != 1:
    sys.exit("якорь Currency в Payload не найден")
s = s.replace(a4, a4 +
 '\tAmountNoVAT    string `json:"amount_no_vat,omitempty"`\n'
 '\tContractNumber string `json:"contract_number,omitempty"`\n'
 '\tContractDate   string `json:"contract_date,omitempty"`\n', 1)

a5 = '\tp.Currency = p.Fields["currency"]\n'
if s.count(a5) != 1:
    sys.exit("якорь присвоения Currency не найден")
s = s.replace(a5, a5 +
 '\tp.AmountNoVAT = p.Fields["amount_no_vat"]\n'
 '\tp.ContractNumber = p.Fields["contract_number"]\n'
 '\tp.ContractDate = p.Fields["contract_date"]\n', 1)

io.open(op, 'w', encoding='utf-8').write(s)
PY
  ok "схема шапки зафиксирована, договор и сумма без НДС — явными тегами"
fi
fi

# ---------------------------------------------------------------------------
# ШАГ 4. Сборка.
# ---------------------------------------------------------------------------
hdr "4/4  сборка"
if command -v go >/dev/null 2>&1; then
  if ( cd docflow && go build ./... ); then ok "go build прошёл"; else die "go build упал — откатитесь, см. ниже"; fi
else
  skip "go на хосте нет, проверка будет при docker build"
fi

cat <<EOF

${C_HDR}Дальше руками:${C_0}

  docker compose up -d --build docflow-backend llm
  docker compose logs -f docflow-backend | grep -iE 'unparseable|model refine|no json'

  Загрузите тот же ЭСЧФ и посмотрите, пропали ли строки про unparseable json.

${C_HDR}Откат:${C_0}

  for f in .env docker-compose.yml docflow/internal/onec/file.go \\
           docflow/internal/onec/onec.go docflow/internal/recognize/pipeline.go; do
    [ -f "\$f.bak-audit-$TS" ] && mv "\$f.bak-audit-$TS" "\$f"
  done
  docker compose up -d --build docflow-backend llm

EOF
