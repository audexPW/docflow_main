import { useEffect, useRef, useState, useCallback } from 'react'

// Редактор фото перед отправкой: поворот, обрезка, контраст, яркость.
// Чистый canvas + pointer events, без внешних библиотек. Работает и в браузере,
// и во встроенном WebView мобильного приложения.
//
// Props:
//   file     — исходный File (image/jpeg или image/png)
//   onCancel — закрыть без изменений
//   onApply(file) — вернуть отредактированный File (JPEG)

const MAX_PREVIEW = 560

export default function ImageEditor({ file, onCancel, onApply }) {
  const imgRef = useRef(null)
  const workRef = useRef(null) // полноразмерный холст с применёнными поворотом/фильтрами
  const previewRef = useRef(null) // видимый уменьшенный холст
  const wrapRef = useRef(null)

  const [loaded, setLoaded] = useState(false)
  const [err, setErr] = useState('')
  const [rot, setRot] = useState(0) // 0/90/180/270
  const [contrast, setContrast] = useState(100)
  const [brightness, setBrightness] = useState(100)
  const [scale, setScale] = useState(1) // work→preview
  const [crop, setCrop] = useState(null) // {x,y,w,h} в координатах preview
  const dragRef = useRef(null)
  const [busy, setBusy] = useState(false)

  // Загрузка изображения из File
  useEffect(() => {
    const url = URL.createObjectURL(file)
    const img = new Image()
    img.onload = () => {
      imgRef.current = img
      setLoaded(true)
      URL.revokeObjectURL(url)
    }
    img.onerror = () => {
      setErr('Не удалось открыть изображение для правки')
      URL.revokeObjectURL(url)
    }
    img.src = url
  }, [file])

  // Полноразмерный холст: поворот + фильтры
  const renderWork = useCallback(() => {
    const img = imgRef.current
    if (!img) return
    const rotated = rot === 90 || rot === 270
    const w = rotated ? img.naturalHeight : img.naturalWidth
    const h = rotated ? img.naturalWidth : img.naturalHeight

    const work = workRef.current
    work.width = w
    work.height = h
    const ctx = work.getContext('2d')
    ctx.clearRect(0, 0, w, h)
    ctx.filter = `contrast(${contrast}%) brightness(${brightness}%)`
    ctx.save()
    ctx.translate(w / 2, h / 2)
    ctx.rotate((rot * Math.PI) / 180)
    ctx.drawImage(img, -img.naturalWidth / 2, -img.naturalHeight / 2)
    ctx.restore()

    // Превью
    const s = Math.min(1, MAX_PREVIEW / w)
    setScale(s)
    const preview = previewRef.current
    preview.width = Math.round(w * s)
    preview.height = Math.round(h * s)
    const pctx = preview.getContext('2d')
    pctx.clearRect(0, 0, preview.width, preview.height)
    pctx.drawImage(work, 0, 0, preview.width, preview.height)
  }, [rot, contrast, brightness])

  useEffect(() => {
    if (!loaded) return
    renderWork()
  }, [loaded, renderWork])

  // Сброс выделения при повороте (координаты становятся невалидными)
  useEffect(() => {
    setCrop(null)
  }, [rot])

  // --- Выделение области обрезки (drag) ---
  function pointFromEvent(e) {
    const rect = previewRef.current.getBoundingClientRect()
    const clientX = e.clientX ?? (e.touches && e.touches[0]?.clientX)
    const clientY = e.clientY ?? (e.touches && e.touches[0]?.clientY)
    const x = Math.max(0, Math.min(previewRef.current.width, clientX - rect.left))
    const y = Math.max(0, Math.min(previewRef.current.height, clientY - rect.top))
    return { x, y }
  }

  function onDown(e) {
    e.preventDefault()
    const p = pointFromEvent(e)
    dragRef.current = { startX: p.x, startY: p.y }
    setCrop({ x: p.x, y: p.y, w: 0, h: 0 })
  }
  function onMove(e) {
    if (!dragRef.current) return
    const p = pointFromEvent(e)
    const { startX, startY } = dragRef.current
    setCrop({
      x: Math.min(startX, p.x),
      y: Math.min(startY, p.y),
      w: Math.abs(p.x - startX),
      h: Math.abs(p.y - startY),
    })
  }
  function onUp() {
    dragRef.current = null
    setCrop((c) => (c && (c.w < 8 || c.h < 8) ? null : c))
  }

  function rotateCW() {
    setRot((r) => (r + 90) % 360)
  }
  function resetAll() {
    setRot(0)
    setContrast(100)
    setBrightness(100)
    setCrop(null)
  }

  async function apply() {
    setBusy(true)
    try {
      const work = workRef.current
      const s = scale || 1
      let sx = 0,
        sy = 0,
        sw = work.width,
        sh = work.height
      if (crop && crop.w > 8 && crop.h > 8) {
        sx = Math.round(crop.x / s)
        sy = Math.round(crop.y / s)
        sw = Math.round(crop.w / s)
        sh = Math.round(crop.h / s)
      }
      const out = document.createElement('canvas')
      out.width = sw
      out.height = sh
      out.getContext('2d').drawImage(work, sx, sy, sw, sh, 0, 0, sw, sh)

      const blob = await new Promise((resolve) => out.toBlob(resolve, 'image/jpeg', 0.92))
      if (!blob) {
        setErr('Не удалось сохранить изображение')
        setBusy(false)
        return
      }
      const base = (file.name || 'document').replace(/\.[^.]+$/, '')
      const edited = new File([blob], `${base}_edited.jpg`, { type: 'image/jpeg' })
      onApply(edited)
    } catch {
      setErr('Ошибка при обработке изображения')
      setBusy(false)
    }
  }

  return (
    <div className="editor-overlay" onPointerUp={onUp}>
      <div className="editor" ref={wrapRef}>
        <div className="toolbar" style={{ marginBottom: 8 }}>
          <strong>Правка перед отправкой</strong>
          <div className="spacer" />
          <button onClick={onCancel} disabled={busy}>
            Отмена
          </button>
        </div>

        {err && <div className="error">{err}</div>}

        <div
          className="editor-canvas-wrap"
          style={{ position: 'relative', touchAction: 'none', userSelect: 'none' }}
          onPointerDown={onDown}
          onPointerMove={onMove}
        >
          <canvas ref={previewRef} style={{ display: 'block', maxWidth: '100%', borderRadius: 8 }} />
          {crop && (
            <div
              style={{
                position: 'absolute',
                left: crop.x,
                top: crop.y,
                width: crop.w,
                height: crop.h,
                border: '2px dashed #fff',
                boxShadow: '0 0 0 9999px rgba(0,0,0,0.35)',
                pointerEvents: 'none',
              }}
            />
          )}
        </div>
        {/* Рабочий холст скрыт */}
        <canvas ref={workRef} style={{ display: 'none' }} />

        <p className="muted small" style={{ marginBottom: 4 }}>
          Потяните по изображению, чтобы выделить область обрезки.
        </p>

        <div className="editor-controls">
          <div className="toolbar" style={{ marginBottom: 8 }}>
            <button onClick={rotateCW} disabled={busy}>
              Повернуть на 90°
            </button>
            <button onClick={() => setCrop(null)} disabled={busy || !crop}>
              Сбросить обрезку
            </button>
            <button onClick={resetAll} disabled={busy}>
              Сбросить всё
            </button>
          </div>

          <div className="field" style={{ marginBottom: 6 }}>
            <label>Контраст: {contrast}%</label>
            <input
              type="range"
              min="50"
              max="200"
              value={contrast}
              onChange={(e) => setContrast(Number(e.target.value))}
            />
          </div>
          <div className="field" style={{ marginBottom: 6 }}>
            <label>Яркость: {brightness}%</label>
            <input
              type="range"
              min="50"
              max="200"
              value={brightness}
              onChange={(e) => setBrightness(Number(e.target.value))}
            />
          </div>
        </div>

        <div className="toolbar" style={{ marginTop: 10, marginBottom: 0 }}>
          <button className="btn-primary" onClick={apply} disabled={busy || !loaded}>
            {busy ? 'Обработка…' : 'Применить'}
          </button>
        </div>
      </div>
    </div>
  )
}
