export function protocolName(row) {
  return `${row.last_name ?? ''} ${row.first_name ?? ''}`.trim()
}

function categoryKey(row) {
  return row.category_id || row.category_name || ''
}

export function protocolCategoryOptions(rows) {
  const byKey = new Map()
  for (const row of rows) {
    const key = categoryKey(row)
    if (key && !byKey.has(key)) byKey.set(key, row.category_name || key)
  }
  return [...byKey.entries()]
    .map(([value, label]) => ({value, label}))
    .sort((a, b) => a.label.localeCompare(b.label, 'ru'))
}

// Explicit filter arguments keep Svelte reactivity tied to each input.
export function filterProtocolRows(rows, query, gender, category) {
  const q = query.trim().toLowerCase()
  return rows.filter(row => {
    if (gender !== 'all' && row.gender !== gender) return false
    if (category !== 'all' && categoryKey(row) !== category) return false
    return !q || protocolName(row).toLowerCase().includes(q) ||
      (row.number != null && String(row.number).includes(q))
  })
}
