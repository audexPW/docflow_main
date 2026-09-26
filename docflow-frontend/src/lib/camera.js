// Съёмка документа камерой. В мобильном приложении используем нативный плагин
// @capacitor/camera (доступ к нему — через глобал window.Capacitor.Plugins,
// поэтому веб-сборка не тянет мобильные зависимости). В браузере плагина нет —
// возвращаем null, и вызывающий код откатывается на обычный <input capture>.

function nativeCamera() {
  return (window.Capacitor && window.Capacitor.Plugins && window.Capacitor.Plugins.Camera) || null
}

export function hasNativeCamera() {
  return !!nativeCamera()
}

// takePhoto открывает камеру и возвращает снятый кадр как File (JPEG) либо null.
export async function takePhoto() {
  const cam = nativeCamera()
  if (!cam) return null

  const photo = await cam.getPhoto({
    quality: 92,
    allowEditing: false,
    resultType: 'uri', // получаем webPath, затем читаем в Blob
    source: 'CAMERA',
    saveToGallery: false,
  })

  const src = photo.webPath || (photo.dataUrl ?? '')
  if (!src) return null

  const res = await fetch(src)
  const blob = await res.blob()
  const name = `scan_${Date.now()}.jpg`
  return new File([blob], name, { type: blob.type || 'image/jpeg' })
}
