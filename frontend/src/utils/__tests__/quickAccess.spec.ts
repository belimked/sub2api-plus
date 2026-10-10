import { describe, expect, it } from 'vitest'
import { buildQuickAccessUrl, pickRandom } from '../quickAccess'

describe('buildQuickAccessUrl', () => {
  it('appends the api key as the apikey query parameter', () => {
    expect(buildQuickAccessUrl('https://gw.example.com/entry', 'sk-abc')).toBe(
      'https://gw.example.com/entry?apikey=sk-abc'
    )
  })

  it('keeps existing query parameters and replaces a stale apikey', () => {
    expect(buildQuickAccessUrl('https://gw.example.com/entry?from=panel&apikey=old', 'sk-new')).toBe(
      'https://gw.example.com/entry?from=panel&apikey=sk-new'
    )
  })

  it('encodes special characters in the key', () => {
    expect(buildQuickAccessUrl('https://gw.example.com/entry', 'sk-a+b&c')).toBe(
      'https://gw.example.com/entry?apikey=sk-a%2Bb%26c'
    )
  })
})

describe('pickRandom', () => {
  it('returns undefined for an empty list', () => {
    expect(pickRandom([])).toBeUndefined()
  })

  it('maps the random value onto the list bounds', () => {
    const items = ['a', 'b', 'c']
    expect(pickRandom(items, () => 0)).toBe('a')
    expect(pickRandom(items, () => 0.5)).toBe('b')
    expect(pickRandom(items, () => 0.999)).toBe('c')
  })
})
