import { statusLabel } from '../lib/labels.js'

export default function StatusBadge({ status }) {
  return <span className={'status status-' + status}>{statusLabel(status)}</span>
}
