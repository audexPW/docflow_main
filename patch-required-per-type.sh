#!/usr/bin/env bash
# ============================================================================
#  DocFlow: единый перечень обязательных реквизитов реально применяется
#
#  Что было не так. Прошлый патч прописал список из 14 реквизитов в
#  fallback_required, но в логе распознанного счёта по-прежнему missing:null.
#
#  Причина в порядке разрешения. RequiredFields берёт fallback_required ТОЛЬКО
#  для типа, у которого нет своего списка:
#
#      base := current().fallbackRequired
#      if spec, ok := LookupDocType(docType); ok && len(spec.Required) > 0 {
#          base = spec.Required
#      }
#
#  А во встроенном реестре (doctypes.go) у каждого типа список свой, зашитый в
#  код: у invoice это {number, date, total, @taxid}. Все четыре реквизита
#  распознались — отсюда и пустой missing. То есть fallback_required не
#  применялся ни к одному известному типу, он работает только для форм,
#  которых в реестре нет вовсе.
#
#  Патч прописывает перечень явно каждому из 11 типов в doctypes.json.
#  LoadDocTypes накладывает файл поверх встроенного реестра, и непустые поля
#  файла побеждают — так список становится единым для всех типов, как и
#  требует заказчик.
#
#  Заодно убирается ключ _comment_required, если он ещё остался: парсер идёт с
#  DisallowUnknownFields, и любой лишний ключ верхнего уровня роняет загрузку
#  реестра вместе со всем бэкендом.
#
#  Пересборка не нужна: doctypes.json монтируется в контейнер, достаточно
#  перезапуска.
#
#  Запуск из /opt/docflow-deploy:  bash patch-required-per-type.sh
# ============================================================================
set -euo pipefail
cd "$(dirname "$0")"

TS="$(date +%Y%m%d-%H%M%S)"
[ -f doctypes.json ] || { echo "Не вижу doctypes.json"; exit 1; }
cp doctypes.json "doctypes.json.bak.pertype-$TS"

python3 - <<'PY'
import io, json, collections
p = 'doctypes.json'
d = json.load(io.open(p, encoding='utf-8'), object_pairs_hook=collections.OrderedDict)

# Лишний ключ верхнего уровня валит парсер: DisallowUnknownFields.
d.pop('_comment_required', None)

REQ = ["account_credit", "account_debit", "counterparty", "@taxid",
       "contract_number", "contract_date", "currency", "date", "number",
       "organization", "organization_unp", "amount_no_vat",
       "vat_amount", "total"]

for t in d.get('types', []):
    t['required'] = list(REQ)
d['fallback_required'] = list(REQ)

io.open(p, 'w', encoding='utf-8').write(json.dumps(d, ensure_ascii=False, indent=2) + "\n")
print('   типов с явным перечнем:', len(d.get('types', [])))
print('   реквизитов в перечне: ', len(REQ))
PY

python3 -c "import json,io; json.load(io.open('doctypes.json',encoding='utf-8')); print('   doctypes.json — валидный JSON')"

docker compose restart docflow-backend
sleep 6
docker compose logs --tail=20 docflow-backend | grep -iE "doctypes load failed" && {
  echo; echo "Реестр не загрузился. Откат:  cp doctypes.json.bak.pertype-$TS doctypes.json && docker compose restart docflow-backend"
  exit 1; } || echo "   реестр загрузился без ошибок"

cat <<MSG

Готово. Бэкап: doctypes.json.bak.pertype-$TS

Проверить перечень, который отдаёт сервер:

  curl -s localhost:8080/api/doctypes | python3 -m json.tool | grep -A 20 '"slug": "invoice"'

Затем «Распознать заново» на счёте №112 и:

  docker compose logs --tail=200 docflow-backend | grep "document recognized"

Теперь в missing должны быть перечислены недостающие реквизиты, а не null.
Для счёта №112 ожидаю: account_debit, contract_date, amount_no_vat.

Если понадобится вернуть прежнее поведение — перечень правится в
doctypes.json, пересборка не нужна, только restart docflow-backend.
MSG
