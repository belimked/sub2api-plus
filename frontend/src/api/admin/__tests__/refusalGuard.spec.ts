import { describe, expect, it } from 'vitest'
import { normalizeRefusalConfig, type RefusalConfig } from '../refusalGuard'

describe('normalizeRefusalConfig', () => {
  it('turns null rule fields from Go nil slices into arrays', () => {
    const raw = {
      enabled: false, user_threshold: 3, count_window_seconds: 300, user_cooldown_seconds: 300,
      session_block_ttl_seconds: 2592000,
      rules: [{ id: 'anthropic_usage_policy', name: 'p', enabled: true, status_codes: [400],
        error_codes: null, error_types: null, message_keywords: ['violate our Usage Policy'] }],
    } as unknown as RefusalConfig
    const cfg = normalizeRefusalConfig(raw)
    expect(cfg.rules[0].error_codes).toEqual([])
    expect(cfg.rules[0].error_types).toEqual([])
    expect(cfg.rules[0].message_keywords).toEqual(['violate our Usage Policy'])
  })
  it('handles null rules', () => {
    expect(normalizeRefusalConfig({ rules: null } as unknown as RefusalConfig).rules).toEqual([])
  })
})
