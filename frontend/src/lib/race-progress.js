export function progressSummary(progress, legacyCounts) {
  const c = progress?.counts
  if (!c) {
    if (!legacyCounts) return ''
    return `Финишировали: ${legacyCounts.finished} · DNS: ${legacyCounts.dns} · DNF: ${legacyCounts.dnf} · DSQ: ${legacyCounts.dsq}`
  }
  const parts = [`Финишировали: ${c.finished}`, `На дистанции: ${c.on_course}`, `Сошли: ${c.dnf}`, `DNS: ${c.dns}`, `DSQ: ${c.dsq}`]
  if (c.not_started) parts.push(`Старт не наступил или не задан: ${c.not_started}`)
  if (c.needs_review) parts.push(`Требуют проверки: ${c.needs_review}`)
  if (c.reserve) parts.push(`Резерв: ${c.reserve}`)
  return parts.join(' · ')
}
