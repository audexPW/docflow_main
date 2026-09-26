export function formatDateTime(iso) {
  if (!iso) return '—'
  const d = new Date(iso)
  if (isNaN(d.getTime())) return iso
  const pad = (n) => String(n).padStart(2, '0')
  return `${pad(d.getDate())}.${pad(d.getMonth() + 1)}.${d.getFullYear()} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

export function formatSize(bytes) {
  if (!bytes && bytes !== 0) return '—'
  if (bytes < 1024) return bytes + ' Б'
  if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(0) + ' КБ'
  return (bytes / (1024 * 1024)).toFixed(1) + ' МБ'
}

export function formatConfidence(c) {
  if (c === undefined || c === null) return ''
  return Math.round(c * 100) + '%'
}

// Уровень доверия к распознанному значению. Теперь уверенность считается по
// свидетельствам (сходятся ли источники, есть ли значение в документе, бьётся
// ли арифметика), а не проставляется по источнику — поэтому её имеет смысл
// показывать цветом: главбух должен видеть, какие поля перепроверять.
export function confidenceLevel(c) {
  if (c === undefined || c === null) return ''
  if (c < 0.5) return 'conf-low'
  if (c < 0.75) return 'conf-mid'
  return 'conf-high'
}
