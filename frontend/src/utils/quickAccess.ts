export function buildQuickAccessUrl(base: string, apiKey: string): string {
  const url = new URL(base)
  url.searchParams.set('apikey', apiKey)
  return url.toString()
}

export function pickRandom<T>(items: readonly T[], random: () => number = Math.random): T | undefined {
  if (items.length === 0) return undefined
  return items[Math.floor(random() * items.length)]
}
