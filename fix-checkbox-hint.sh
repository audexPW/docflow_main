#!/usr/bin/env bash
# DocFlow: «не кликабельно окошко (не во всех док-х)».
# Флажок в списке документов намеренно неактивен, когда документ нельзя
# выгрузить в 1С (уже выгружен / ещё не распознан / у типа нет объекта-приёмника).
# Проблема в том, что у disabled-инпута браузер не показывает ни title, ни
# курсор, ни клик — для пользователя это просто мёртвая галочка.
# Патч оставляет логику как есть, но делает причину видимой: серый флажок,
# курсор «нельзя», подсказка при наведении и всплывающая строка по клику
# (важно для планшета, где наведения нет вовсе).
set -euo pipefail

ROOT="${1:-$(pwd)}"
JSX="$ROOT/docflow-frontend/src/pages/Documents.jsx"
CSS="$ROOT/docflow-frontend/src/styles.css"
[ -f "$JSX" ] || { echo "Не найден $JSX — запусти скрипт из каталога docflow-deploy"; exit 1; }

STAMP=$(date +%Y%m%d-%H%M%S)
cp "$JSX" "$JSX.bak-$STAMP"
cp "$CSS" "$CSS.bak-$STAMP"
echo "Бэкапы: $JSX.bak-$STAMP"

python3 - "$JSX" "$CSS" <<'PY'
import sys, io

jsx_path, css_path = sys.argv[1], sys.argv[2]
src = io.open(jsx_path, encoding='utf-8').read()

if 'cb-blocked' in src:
    print('Documents.jsx уже пропатчен — пропускаю')
else:
    # 1. Состояние подсказки + автоскрытие.
    anchor = "  const [report, setReport] = useState(null) // { ok, failed: [{ name, message }] }\n"
    assert anchor in src, 'не найден якорь состояния'
    src = src.replace(anchor, anchor + """
  // Причина, по которой галочка не ставится. disabled-инпут не отдаёт браузеру
  // ни клика, ни подсказки title, поэтому неактивный флажок рисуется обычным
  // (только для чтения) внутри span, а причина показывается всплывающей строкой.
  const [hint, setHint] = useState(null) // { text }
  useEffect(() => {
    if (!hint) return
    const t = setTimeout(() => setHint(null), 5000)
    return () => clearTimeout(t)
  }, [hint])
""", 1)

    # 2. Флажок «выбрать все».
    old_head = """                  <input
                    type="checkbox"
                    checked={allSelected}
                    onChange={toggleAll}
                    disabled={selectable.length === 0 || Boolean(sending)}
                    title={
                      selectable.length === 0
                        ? 'Нет документов, готовых к выгрузке'
                        : 'Выбрать все, готовые к выгрузке'
                    }
                  />
"""
    new_head = """                  {selectable.length === 0 ? (
                    <span
                      className="cb-blocked"
                      title="Нет документов, готовых к выгрузке"
                      onClick={() =>
                        setHint({ text: 'В списке нет документов, готовых к выгрузке в 1С' })
                      }
                    >
                      <input type="checkbox" checked={false} readOnly tabIndex={-1} />
                    </span>
                  ) : (
                    <input
                      type="checkbox"
                      checked={allSelected}
                      onChange={toggleAll}
                      disabled={Boolean(sending)}
                      title="Выбрать все, готовые к выгрузке"
                    />
                  )}
"""
    assert old_head in src, 'не найден флажок в шапке таблицы'
    src = src.replace(old_head, new_head, 1)

    # 3. Флажок в строке.
    old_row = """                      <input
                        type="checkbox"
                        checked={selected.has(d.id)}
                        onChange={() => toggleOne(d.id)}
                        disabled={Boolean(reason) || Boolean(sending)}
                        title={reason || 'Отправить в 1С'}
                      />
"""
    new_row = """                      {reason ? (
                        <span
                          className="cb-blocked"
                          title={reason}
                          onClick={() => setHint({ text: d.original_name + ': ' + reason })}
                        >
                          <input type="checkbox" checked={false} readOnly tabIndex={-1} />
                        </span>
                      ) : (
                        <input
                          type="checkbox"
                          checked={selected.has(d.id)}
                          onChange={() => toggleOne(d.id)}
                          disabled={Boolean(sending)}
                          title="Отправить в 1С"
                        />
                      )}
"""
    assert old_row in src, 'не найден флажок в строке таблицы'
    src = src.replace(old_row, new_row, 1)

    # 4. Подсказка внизу списка: наведение работает не везде, добавляем про клик.
    src = src.replace(
        "          Наведите курсор на серый флажок, чтобы увидеть причину.",
        "          Нажмите на серый флажок (или наведите курсор), чтобы увидеть причину.",
        1,
    )

    # 5. Сама всплывающая строка.
    tail = """      {!loading && blocked > 0 && (
"""
    assert tail in src
    src = src.replace(tail, """      {hint && (
        <div className="row-hint" onClick={() => setHint(null)} role="status">
          {hint.text}
        </div>
      )}

""" + tail, 1)

    io.open(jsx_path, 'w', encoding='utf-8').write(src)
    print('Documents.jsx пропатчен')

css = io.open(css_path, encoding='utf-8').read()
if 'cb-blocked' in css:
    print('styles.css уже пропатчен — пропускаю')
else:
    css += """
/* --- Неактивный флажок выгрузки в 1С ---------------------------------------
   Раньше здесь стоял disabled-инпут: браузер не показывал ни title, ни курсор,
   и окошко выглядело просто сломанным. Теперь клик ловит обёртка. */
.cb-blocked {
  display: inline-flex;
  align-items: center;
  cursor: not-allowed;
}
.cb-blocked input {
  pointer-events: none;
  opacity: 0.4;
}

/* Всплывающая строка с причиной. */
.row-hint {
  position: fixed;
  left: 50%;
  bottom: 24px;
  transform: translateX(-50%);
  z-index: 60;
  max-width: min(90vw, 560px);
  padding: 10px 16px;
  border-radius: 4px;
  background: #2b2b2b;
  color: #fff;
  font-size: 14px;
  line-height: 1.35;
  box-shadow: 0 4px 14px rgba(0, 0, 0, 0.28);
  cursor: pointer;
}
"""
    io.open(css_path, 'w', encoding='utf-8').write(css)
    print('styles.css пропатчен')
PY

echo
echo "Пересобираю фронтенд…"
cd "$ROOT"
docker compose build frontend
docker compose up -d frontend
echo "Готово. Обнови страницу с Ctrl+Shift+R."
