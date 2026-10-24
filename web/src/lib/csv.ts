/**
 * CSV export (RFC 4180). Cells that start with =, +, -, @, tab or CR are prefixed with a quote so spreadsheet apps do not
 * evaluate them as formulas (CSV injection).
 */
export function csvCell(v: unknown): string {
  let s =
    typeof v === 'string'
      ? v
      : typeof v === 'number' || typeof v === 'boolean' || typeof v === 'bigint'
        ? String(v)
        : v && typeof v === 'object'
          ? JSON.stringify(v)
          : ''
  if (/^[=+\-@\t\r]/.test(s)) s = `'${s}`
  return /[",\n\r]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s
}

export function toCsv(header: string[], rows: unknown[][]): string {
  return [header, ...rows].map((r) => r.map(csvCell).join(',')).join('\r\n') + '\r\n'
}

export function downloadText(name: string, text: string, type = 'text/csv') {
  const blob = new Blob([text], { type: `${type};charset=utf-8` })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = name
  a.click()
  URL.revokeObjectURL(url)
}
