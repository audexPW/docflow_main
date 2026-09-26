import { API_BASE } from './config.js'

const TOKEN_KEY = 'docflow_token'
const ROLE_KEY = 'docflow_role'
const EXP_KEY = 'docflow_expires'

export function getToken() {
  return localStorage.getItem(TOKEN_KEY) || ''
}

export function getSession() {
  const token = getToken()
  if (!token) return null
  const expires = localStorage.getItem(EXP_KEY)
  if (expires && new Date(expires).getTime() < Date.now()) {
    clearSession()
    return null
  }
  return { token, role: localStorage.getItem(ROLE_KEY) || 'client' }
}

export function saveSession({ token, role, expires_at }) {
  localStorage.setItem(TOKEN_KEY, token)
  localStorage.setItem(ROLE_KEY, role)
  if (expires_at) localStorage.setItem(EXP_KEY, expires_at)
}

export function clearSession() {
  localStorage.removeItem(TOKEN_KEY)
  localStorage.removeItem(ROLE_KEY)
  localStorage.removeItem(EXP_KEY)
}

// Ошибка с текстом от сервера и HTTP-статусом.
export class ApiError extends Error {
  constructor(message, status) {
    super(message)
    this.status = status
  }
}

async function request(path, { method = 'GET', body, headers = {}, raw = false } = {}) {
  const opts = { method, headers: { ...headers } }
  const token = getToken()
  if (token) opts.headers['Authorization'] = 'Bearer ' + token

  if (body instanceof FormData) {
    opts.body = body // Content-Type выставит браузер (с boundary)
  } else if (body !== undefined) {
    opts.headers['Content-Type'] = 'application/json'
    opts.body = JSON.stringify(body)
  }

  let res
  try {
    res = await fetch(API_BASE + path, opts)
  } catch (e) {
    throw new ApiError('Нет связи с сервером', 0)
  }

  if (res.status === 401) {
    clearSession()
    throw new ApiError('Сессия истекла, войдите заново', 401)
  }

  if (raw) {
    if (!res.ok) throw new ApiError('Не удалось загрузить файл', res.status)
    return res
  }

  let data = null
  const text = await res.text()
  if (text) {
    try {
      data = JSON.parse(text)
    } catch {
      data = null
    }
  }

  if (!res.ok) {
    const msg = (data && data.error) || 'Ошибка ' + res.status
    throw new ApiError(msg, res.status)
  }
  return data
}

export const api = {
  login(login, password) {
    return request('/api/auth/login', { method: 'POST', body: { login, password } })
  },

  // Смена собственного пароля. Сервер аннулирует прежние токены и возвращает
  // новый, поэтому сессию нужно перезаписать ответом.
  changePassword(current_password, new_password) {
    return request('/api/auth/password', {
      method: 'POST',
      body: { current_password, new_password },
    })
  },

  // --- администрирование учётных записей ---
  setUserPassword(id, new_password) {
    return request('/api/users/' + id + '/password', { method: 'POST', body: { new_password } })
  },

  setUserActive(id, is_active) {
    return request('/api/users/' + id + '/active', { method: 'POST', body: { is_active } })
  },

  setUserRole(id, role) {
    return request('/api/users/' + id + '/role', { method: 'POST', body: { role } })
  },

  // Привязка учётки к юрлицу. null снимает привязку (сотрудник офиса,
  // работающий сразу за нескольких клиентов).
  setUserCompany(id, company_id) {
    return request('/api/users/' + id + '/company', {
      method: 'POST',
      body: { company_id: company_id || null },
    })
  },

  // Компании главбуха. То же закрепление, что и на странице «Компании», но со
  // стороны сотрудника: один запрос вместо обхода всех карточек компаний.
  listUserCompanies(id) {
    return request('/api/users/' + id + '/companies')
  },

  setUserCompanies(id, company_ids) {
    return request('/api/users/' + id + '/companies', { method: 'PUT', body: { company_ids } })
  },

  // --- компании (юрлица) ---
  listCompanies() {
    return request('/api/companies')
  },

  createCompany({ name, unp, folder, approval_required }) {
    return request('/api/companies', {
      method: 'POST',
      body: { name, unp, folder, approval_required: !!approval_required },
    })
  },

  updateCompany(id, patch) {
    return request('/api/companies/' + id, { method: 'PATCH', body: patch })
  },

  listCompanyAccountants(id) {
    return request('/api/companies/' + id + '/accountants')
  },

  // Переназначение главбухов: отправляем итоговый состав целиком.
  setCompanyAccountants(id, user_ids) {
    return request('/api/companies/' + id + '/accountants', { method: 'PUT', body: { user_ids } })
  },

  // Реестр типов документов: подписи, обязательные реквизиты и объект 1С.
  listDocTypes() {
    return request('/api/doctypes')
  },

  // archive: '' — рабочий список (проведённые в 1С скрыты), 'only' — архив,
  // 'all' — всё вместе. Ответ содержит total, поэтому список умеет догружать
  // следующие порции, а не обрываться на первой сотне.
  listDocuments({ status, companyId, archive, limit = 100, offset = 0 } = {}) {
    const q = new URLSearchParams()
    if (status) q.set('status', status)
    // Фильтр по юрлицу: главбух ведёт несколько компаний и разбирает их по
    // очереди — выгружает в 1С сначала одну, потом другую.
    if (companyId) q.set('company_id', companyId)
    if (archive) q.set('archive', archive)
    q.set('limit', limit)
    q.set('offset', offset)
    return request('/api/documents?' + q.toString())
  },

  // Вернуть документ из архива в работу (главбух, администратор).
  unarchiveDocument(id) {
    return request('/api/documents/' + id + '/unarchive', { method: 'POST' })
  },

  getDocument(id) {
    return request('/api/documents/' + id)
  },

  fileResponse(id) {
    return request('/api/documents/' + id + '/file', { raw: true })
  },

  // companyId нужен оператору: он снимает документы за клиента, приехавшего в
  // офис, и указывает, к какому юрлицу их отнести. Клиент его не передаёт —
  // компания берётся из его учётки.
  uploadDocument(file, companyId) {
    const fd = new FormData()
    fd.append('file', file, file.name || 'document')
    if (companyId) fd.append('company_id', companyId)
    return request('/api/documents', { method: 'POST', body: fd })
  },

  updateRecognition(id, { doc_type, fields }) {
    const body = {}
    if (doc_type !== undefined) body.doc_type = doc_type
    if (fields !== undefined) body.fields = fields
    return request('/api/documents/' + id, { method: 'PATCH', body })
  },

  confirmDocument(id) {
    return request('/api/documents/' + id + '/confirm', { method: 'POST' })
  },

  // Дозаполнение вручную введённых полей и досыл в 1С (для клиента — свои документы).
  completeDocument(id, fields) {
    return request('/api/documents/' + id + '/complete', { method: 'POST', body: { fields } })
  },

  // Проставить документу юрлицо (только администратор).
  setDocumentCompany(id, company_id) {
    return request('/api/documents/' + id + '/company', {
      method: 'POST',
      body: { company_id: company_id || null },
    })
  },

  // Решение главбуха по документу.
  approveDocument(id, note) {
    return request('/api/documents/' + id + '/approve', { method: 'POST', body: { note: note || '' } })
  },

  rejectDocument(id, note) {
    return request('/api/documents/' + id + '/reject', { method: 'POST', body: { note } })
  },

  reprocessDocument(id) {
    return request('/api/documents/' + id + '/reprocess', { method: 'POST' })
  },

  registerDevice({ platform, token }) {
    return request('/api/devices', { method: 'POST', body: { platform, token } })
  },

  unregisterDevice(token) {
    return request('/api/devices', { method: 'DELETE', body: { token } })
  },

  listUsers() {
    return request('/api/users')
  },

  createUser({ login, password, role, company_id }) {
    return request('/api/users', {
      method: 'POST',
      body: { login, password, role, company_id: company_id || '' },
    })
  },

  listAudit(limit = 100) {
    return request('/api/audit?limit=' + limit)
  },
}
