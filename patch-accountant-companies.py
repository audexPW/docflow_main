import os, sys

if not os.path.isdir("docflow/internal/httpapi") or not os.path.isdir("docflow-frontend/src"):
    sys.exit("Запусти из /opt/docflow-deploy")

# ---------- 1. storage: набор компаний одного главбуха ----------
p = "docflow/internal/storage/companies.go"
s = open(p, encoding="utf-8").read()
if "SetAccountantCompanies" in s:
    print("storage: уже есть")
else:
    s += '''
// SetAccountantCompanies заменяет весь набор компаний одного главбуха. Обратная
// сторона SetCompanyAccountants: там правится состав одной компании, здесь —
// список компаний одного человека. Одной транзакцией, чтобы админ не увидел
// половину сохранённого при обрыве.
func (db *DB) SetAccountantCompanies(ctx context.Context, userID uuid.UUID, companyIDs []uuid.UUID) error {
\ttx, err := db.BeginTx(ctx, nil)
\tif err != nil {
\t\treturn err
\t}
\tdefer tx.Rollback()

\tif _, err := tx.ExecContext(ctx, `DELETE FROM company_accountants WHERE user_id = $1`, userID); err != nil {
\t\treturn err
\t}
\tfor _, cid := range companyIDs {
\t\tif _, err := tx.ExecContext(ctx, `
\t\t\tINSERT INTO company_accountants (company_id, user_id)
\t\t\tVALUES ($1, $2) ON CONFLICT DO NOTHING`, cid, userID); err != nil {
\t\t\treturn err
\t\t}
\t}
\treturn tx.Commit()
}
'''
    open(p, "w", encoding="utf-8").write(s)
    print("storage: добавлен SetAccountantCompanies")

# ---------- 2. httpapi: два хендлера ----------
p = "docflow/internal/httpapi/handlers_companies.go"
s = open(p, encoding="utf-8").read()
if "handleSetUserCompanies" in s:
    print("handlers: уже есть")
else:
    s += '''
type setUserCompaniesRequest struct {
\tCompanyIDs []string `json:"company_ids"`
}

// handleListUserCompanies — компании, закреплённые за главбухом. Нужен странице
// «Пользователи»: без него она не знает, какие галочки уже стоят.
func (s *Server) handleListUserCompanies(w http.ResponseWriter, r *http.Request) {
\tid, ok := pathUUID(w, r)
\tif !ok {
\t\treturn
\t}
\tids, err := s.db.AccountantCompanyIDs(r.Context(), id)
\tif err != nil {
\t\ts.log.Error("list user companies", "error", err)
\t\twriteError(w, http.StatusInternalServerError, "internal error")
\t\treturn
\t}
\tout := make([]string, 0, len(ids))
\tfor _, cid := range ids {
\t\tout = append(out, cid.String())
\t}
\twriteJSON(w, http.StatusOK, map[string]any{"company_ids": out})
}

// handleSetUserCompanies закрепляет главбуха сразу за набором компаний. То же
// закрепление, что и на странице «Компании», но со стороны сотрудника: посадить
// одного человека на пять юрлиц — один запрос, а не пять карточек.
func (s *Server) handleSetUserCompanies(w http.ResponseWriter, r *http.Request) {
\tid, ok := pathUUID(w, r)
\tif !ok {
\t\treturn
\t}
\tvar req setUserCompaniesRequest
\tif err := decodeJSON(r, &req); err != nil {
\t\twriteError(w, http.StatusBadRequest, "invalid request body")
\t\treturn
\t}

\tuser, err := s.db.UserByID(r.Context(), id)
\tif err != nil {
\t\twriteError(w, http.StatusNotFound, "пользователь не найден")
\t\treturn
\t}
\t// Та же проверка, что и в handleSetCompanyAccountants: закрепление имеет
\t// смысл только для главбуха, у остальных ролей область видимости считается
\t// по users.company_id и эти строки просто повисли бы мусором.
\tif user.Role != domain.RoleAccountant {
\t\twriteError(w, http.StatusBadRequest,
\t\t\t"закреплять компании можно только пользователю с ролью «Главбух»")
\t\treturn
\t}

\tids := make([]uuid.UUID, 0, len(req.CompanyIDs))
\tfor _, raw := range req.CompanyIDs {
\t\tcid, err := uuid.Parse(strings.TrimSpace(raw))
\t\tif err != nil {
\t\t\twriteError(w, http.StatusBadRequest, "некорректный идентификатор компании")
\t\t\treturn
\t\t}
\t\tif _, err := s.db.Company(r.Context(), cid); err != nil {
\t\t\twriteError(w, http.StatusBadRequest, "компания не найдена")
\t\t\treturn
\t\t}
\t\tids = append(ids, cid)
\t}

\tif err := s.db.SetAccountantCompanies(r.Context(), id, ids); err != nil {
\t\ts.log.Error("set user companies", "error", err)
\t\twriteError(w, http.StatusInternalServerError, "не удалось сохранить закрепление")
\t\treturn
\t}

\tactor, _ := identityFrom(r.Context())
\ts.db.Audit(r.Context(), &actor.UserID, "set_user_companies", "user", id.String(),
\t\tmap[string]any{"company_ids": req.CompanyIDs})
\twriteJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}
'''
    open(p, "w", encoding="utf-8").write(s)
    print("handlers: добавлены")

# ---------- 3. маршруты ----------
p = "docflow/internal/httpapi/server.go"
s = open(p, encoding="utf-8").read()
a = '\tmux.HandleFunc("POST /api/users/{id}/company", s.auth(s.handleSetUserCompany, domain.RoleAdmin))\n'
if "handleSetUserCompanies" in s:
    print("маршруты: уже есть")
elif s.count(a) != 1:
    sys.exit("не нашёл маршрут /api/users/{id}/company в server.go")
else:
    s = s.replace(a, a
        + '\tmux.HandleFunc("GET /api/users/{id}/companies", s.auth(s.handleListUserCompanies, domain.RoleAdmin))\n'
        + '\tmux.HandleFunc("PUT /api/users/{id}/companies", s.auth(s.handleSetUserCompanies, domain.RoleAdmin))\n')
    open(p, "w", encoding="utf-8").write(s)
    print("маршруты: добавлены")

# ---------- 4. api.js ----------
p = "docflow-frontend/src/api.js"
s = open(p, encoding="utf-8").read()
anchor = """  setUserCompany(id, company_id) {
    return request('/api/users/' + id + '/company', {
      method: 'POST',
      body: { company_id: company_id || null },
    })"""
if "setUserCompanies" in s:
    print("api.js: уже есть")
elif s.count(anchor) != 1:
    sys.exit("не нашёл setUserCompany в api.js")
else:
    idx = s.index(anchor)
    end = s.index("},", idx + len(anchor)) + 2
    add = """

  // Компании главбуха. То же закрепление, что и на странице «Компании», но со
  // стороны сотрудника: один запрос вместо обхода всех карточек компаний.
  listUserCompanies(id) {
    return request('/api/users/' + id + '/companies')
  },

  setUserCompanies(id, company_ids) {
    return request('/api/users/' + id + '/companies', { method: 'PUT', body: { company_ids } })
  },"""
    open(p, "w", encoding="utf-8").write(s[:end] + add + s[end:])
    print("api.js: добавлены методы")

# ---------- 5. Users.jsx ----------
p = "docflow-frontend/src/pages/Users.jsx"
s = open(p, encoding="utf-8").read()
if "toggleAccCompany" in s:
    print("Users.jsx: уже есть")
else:
    reps = [
("""  const [busy, setBusy] = useState(false)
  const [actingOn, setActingOn] = useState(null)""",
"""  const [busy, setBusy] = useState(false)
  const [actingOn, setActingOn] = useState(null)
  // Компании главбухов: id учётки -> массив id компаний. Держим отдельно от
  // items, потому что это не поле пользователя, а строки company_accountants.
  const [accCompanies, setAccCompanies] = useState({})"""),

("""      const [res, comp] = await Promise.all([api.listUsers(), api.listCompanies()])
      setItems(res.items || [])
      setCompanies(comp.items || [])
      setError('')""",
"""      const [res, comp] = await Promise.all([api.listUsers(), api.listCompanies()])
      const users = res.items || []
      setItems(users)
      setCompanies(comp.items || [])

      // Закрепления тянем только для главбухов — у остальных ролей их не бывает.
      const pairs = await Promise.all(
        users
          .filter((u) => u.role === 'accountant')
          .map((u) =>
            api
              .listUserCompanies(u.id)
              .then((r) => [u.id, r.company_ids || []])
              .catch(() => [u.id, []])
          )
      )
      setAccCompanies(Object.fromEntries(pairs))
      setError('')"""),

("""  function changeCompany(u, nextCompany) {
    if ((u.company_id || '') === nextCompany) return
    act(u.id, () => api.setUserCompany(u.id, nextCompany), 'Компания изменена')
  }""",
"""  function changeCompany(u, nextCompany) {
    if ((u.company_id || '') === nextCompany) return
    act(u.id, () => api.setUserCompany(u.id, nextCompany), 'Компания изменена')
  }

  // Главбух ведёт сколько угодно юрлиц, поэтому у него не выбор одной компании,
  // а набор галочек. Сохраняем весь набор целиком — сервер заменяет закрепление
  // одной транзакцией, частично применённого состояния не бывает.
  function toggleAccCompany(u, companyId, checked) {
    const current = accCompanies[u.id] || []
    const next = checked ? [...current, companyId] : current.filter((id) => id !== companyId)
    setAccCompanies((prev) => ({ ...prev, [u.id]: next }))
    act(
      u.id,
      () => api.setUserCompanies(u.id, next),
      checked ? 'Компания закреплена' : 'Компания снята'
    )
  }"""),

("""                  <td>
                    <select
                      value={u.company_id || ''}
                      disabled={actingOn === u.id}
                      onChange={(e) => changeCompany(u, e.target.value)}
                      aria-label={`Компания пользователя ${u.login}`}
                    >
                      <option value="">— без привязки —</option>
                      {companies.map((c) => (
                        <option key={c.id} value={c.id}>
                          {c.name}
                        </option>
                      ))}
                    </select>
                    {u.role === 'accountant' && (
                      <div className="small muted">компании назначаются на стр. «Компании»</div>
                    )}
                  </td>""",
"""                  <td>
                    {u.role === 'accountant' ? (
                      companies.length === 0 ? (
                        <span className="muted">компаний пока нет</span>
                      ) : (
                        <div>
                          {companies.map((c) => (
                            <label
                              key={c.id}
                              style={{ display: 'block', fontWeight: 'normal' }}
                            >
                              <input
                                type="checkbox"
                                checked={(accCompanies[u.id] || []).includes(c.id)}
                                disabled={actingOn === u.id}
                                onChange={(e) => toggleAccCompany(u, c.id, e.target.checked)}
                              />{' '}
                              {c.name}
                            </label>
                          ))}
                          <div className="small muted">главбух видит документы отмеченных юрлиц</div>
                        </div>
                      )
                    ) : (
                      <select
                        value={u.company_id || ''}
                        disabled={actingOn === u.id}
                        onChange={(e) => changeCompany(u, e.target.value)}
                        aria-label={`Компания пользователя ${u.login}`}
                      >
                        <option value="">— без привязки —</option>
                        {companies.map((c) => (
                          <option key={c.id} value={c.id}>
                            {c.name}
                          </option>
                        ))}
                      </select>
                    )}
                  </td>"""),
    ]
    for old, new in reps:
        if s.count(old) != 1:
            sys.exit("Users.jsx: не найден фрагмент:\n" + old[:80])
        s = s.replace(old, new)
    open(p, "w", encoding="utf-8").write(s)
    print("Users.jsx: страница обновлена")

print("готово")
