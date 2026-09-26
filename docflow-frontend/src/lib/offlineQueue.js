// Оффлайн-очередь загрузок. Пока нет связи, файлы складываются в IndexedDB
// (он умеет хранить сами Blob'ы и переживает перезапуск приложения), а при
// восстановлении сети досылаются на сервер.
import { api, ApiError } from '../api.js'

const DB_NAME = 'docflow'
const STORE = 'outbox'
const EVENT = 'docflow:outbox'

function openDB() {
  return new Promise((resolve, reject) => {
    const req = indexedDB.open(DB_NAME, 1)
    req.onupgradeneeded = () => {
      const db = req.result
      if (!db.objectStoreNames.contains(STORE)) {
        db.createObjectStore(STORE, { keyPath: 'id', autoIncrement: true })
      }
    }
    req.onsuccess = () => resolve(req.result)
    req.onerror = () => reject(req.error)
  })
}

function tx(db, mode) {
  return db.transaction(STORE, mode).objectStore(STORE)
}

function notifyChanged() {
  window.dispatchEvent(new Event(EVENT))
}

export function onOutboxChange(handler) {
  window.addEventListener(EVENT, handler)
  return () => window.removeEventListener(EVENT, handler)
}

export async function enqueue(file) {
  const db = await openDB()
  await new Promise((resolve, reject) => {
    const req = tx(db, 'readwrite').add({
      name: file.name || 'document',
      type: file.type || 'application/octet-stream',
      size: file.size,
      blob: file,
      addedAt: new Date().toISOString(),
    })
    req.onsuccess = () => resolve()
    req.onerror = () => reject(req.error)
  })
  notifyChanged()
}

export async function listOutbox() {
  const db = await openDB()
  return new Promise((resolve, reject) => {
    const req = tx(db, 'readonly').getAll()
    req.onsuccess = () => resolve(req.result || [])
    req.onerror = () => reject(req.error)
  })
}

export async function countOutbox() {
  const db = await openDB()
  return new Promise((resolve, reject) => {
    const req = tx(db, 'readonly').count()
    req.onsuccess = () => resolve(req.result || 0)
    req.onerror = () => reject(req.error)
  })
}

async function remove(id) {
  const db = await openDB()
  await new Promise((resolve, reject) => {
    const req = tx(db, 'readwrite').delete(id)
    req.onsuccess = () => resolve()
    req.onerror = () => reject(req.error)
  })
}

let flushing = false

// Досылает всё, что накопилось. Возвращает число успешно отправленных.
export async function flushOutbox() {
  if (flushing || !navigator.onLine) return 0
  flushing = true
  let sent = 0
  try {
    const items = await listOutbox()
    for (const it of items) {
      try {
        const file = new File([it.blob], it.name, { type: it.type })
        await api.uploadDocument(file)
        await remove(it.id)
        sent++
      } catch (e) {
        if (e instanceof ApiError && e.status === 0) {
          break // связь снова пропала — оставляем очередь на потом
        }
        // Сервер отверг файл (например, неподдерживаемый тип) — убираем,
        // чтобы очередь не застряла навсегда.
        await remove(it.id)
      }
    }
  } finally {
    flushing = false
    if (sent > 0) notifyChanged()
  }
  return sent
}
