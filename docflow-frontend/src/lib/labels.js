// Человекочитаемые подписи. Если ключ незнаком (появился новый тип документа
// или новое поле) — показываем сам ключ, а не «падаем». Система не привязана
// к закрытому списку типов.

export const docTypeLabels = {
  schet_faktura: 'Счёт-фактура',
  upd: 'УПД',
  invoice: 'Счёт на оплату',
  waybill: 'Товарная накладная',
  act: 'Акт',
  receipt: 'Чек',
  contract: 'Договор',
  unknown: 'Не определён',
}

// Перечень типов открыт (ТЗ §5) и живёт на сервере в реестре DOCTYPES_PATH:
// там же задано, в какой объект 1С попадает каждый тип. Фронтенд подтягивает
// его при входе, а список ниже — только запасной вариант, если запрос не прошёл.
const fallbackOptions = [
  'schet_faktura',
  'upd',
  'invoice',
  'waybill',
  'act',
  'receipt',
  'contract',
  'unknown',
]

let catalog = null // [{ slug, title, required, onec_object, routable }]

export function setDocTypeCatalog(types) {
  if (!Array.isArray(types) || types.length === 0) return
  catalog = types
  for (const t of types) {
    if (t.slug && t.title) docTypeLabels[t.slug] = t.title
  }
}

export function docTypeOptions() {
  if (catalog) return catalog.map((t) => t.slug)
  return fallbackOptions
}

// docTypeInfo нужен, чтобы показать оператору, уйдёт ли документ такого типа в
// 1С сам: без согласованного объекта-приёмника он останется на проверке.
export function docTypeInfo(slug) {
  if (!catalog) return null
  return catalog.find((t) => t.slug === slug) || null
}

export const statusLabels = {
  received: 'Принят',
  processing: 'Распознаётся',
  needs_review: 'На проверке',
  needs_input: 'Нужно уточнение',
  needs_approval: 'На согласовании у главбуха',
  rejected: 'Возвращён главбухом',
  confirmed: 'Подтверждён',
  exported: 'Выгружен в 1С',
  failed: 'Ошибка',
}

export const fieldLabels = {
  date: 'Дата документа',
  number: 'Номер документа',
  total: 'Сумма с НДС',
  currency: 'Валюта',
  unp: 'УНП контрагента',
  inn: 'ИНН контрагента',
  kpp: 'КПП',
  counterparty: 'Контрагент',
  contragent: 'Контрагент',
  organization: 'Организация',
  organization_unp: 'УНП организации',
  organization_inn: 'ИНН организации',
  vat_amount: 'Сумма НДС',
  amount_no_vat: 'Сумма без НДС',
  contract_number: 'Номер договора',
  contract_date: 'Дата договора',
  supplier: 'Поставщик',
  buyer: 'Покупатель',
  account_debit: 'Счёт учёта (дебет)',
  account_credit: 'Счёт расчётов (кредит)',
  account_vat: 'Счёт НДС',
}

// Подписи ответа 1С. Раньше статус показывался кодом (posted/accepted), и
// понять его мог только тот, кто читал документацию обмена.
export const onecStatusLabels = {
  accepted: 'Принят в 1С',
  posted: 'Проведён в 1С',
  rejected: 'Отклонён 1С',
  error: 'Ошибка обработки в 1С',
}

export function onecStatusLabel(s) {
  if (!s) return 'Ответ 1С не получен'
  return onecStatusLabels[s] || s
}

export const roleLabels = {
  client: 'Клиент',
  operator: 'Оператор',
  accountant: 'Главбух',
  admin: 'Администратор',
}

export function docTypeLabel(t) {
  if (!t) return '—'
  return docTypeLabels[t] || t
}

export function statusLabel(s) {
  return statusLabels[s] || s || '—'
}

export function fieldLabel(k) {
  return fieldLabels[k] || k
}

export function roleLabel(r) {
  return roleLabels[r] || r
}
