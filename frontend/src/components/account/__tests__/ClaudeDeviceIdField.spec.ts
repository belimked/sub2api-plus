import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import ClaudeDeviceIdField from '../ClaudeDeviceIdField.vue'
import { isClaudeDeviceId, normalizeClaudeDeviceId, supportsClaudeDeviceId } from '../claudeDeviceId'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

const DEVICE = 'abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789'

function mountField(props: { platform?: string; type?: string; modelValue?: string }) {
  return mount(ClaudeDeviceIdField, { props })
}

describe('ClaudeDeviceIdField', () => {
  it.each([
    ['anthropic', 'oauth', true],
    ['anthropic', 'setup-token', true],
    ['anthropic', 'apikey', false],
    ['anthropic', 'bedrock', false],
    ['openai', 'oauth', false],
  ])('renders for %s %s: %s', (platform, type, visible) => {
    const wrapper = mountField({ platform, type })
    expect(wrapper.find('[data-testid="claude-device-id"]').exists()).toBe(visible)
  })

  it('shows the hint for empty and valid values', () => {
    for (const modelValue of ['', DEVICE, DEVICE.toUpperCase()]) {
      const wrapper = mountField({ platform: 'anthropic', type: 'oauth', modelValue })
      expect(wrapper.text()).toContain('admin.accounts.claudeDeviceId.hint')
      expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    }
  })

  it('flags malformed values', () => {
    const wrapper = mountField({ platform: 'anthropic', type: 'oauth', modelValue: 'clientid123' })
    expect(wrapper.get('[role="alert"]').text()).toBe('admin.accounts.claudeDeviceId.invalid')
  })

  it('binds the device ID through v-model', async () => {
    const wrapper = mountField({ platform: 'anthropic', type: 'oauth', modelValue: '' })
    await wrapper.get('[data-testid="claude-device-id"]').setValue(DEVICE)
    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual([DEVICE])
  })
})

describe('claudeDeviceId helpers', () => {
  it('normalizes to lowercase without surrounding whitespace', () => {
    expect(normalizeClaudeDeviceId(`  ${DEVICE.toUpperCase()} `)).toBe(DEVICE)
  })

  it('accepts blank and 64 hexadecimal characters only', () => {
    expect(isClaudeDeviceId('')).toBe(true)
    expect(isClaudeDeviceId('   ')).toBe(true)
    expect(isClaudeDeviceId(DEVICE)).toBe(true)
    expect(isClaudeDeviceId(DEVICE.slice(1))).toBe(false)
    expect(isClaudeDeviceId('g'.repeat(64))).toBe(false)
  })

  it('limits the setting to Anthropic OAuth and setup-token accounts', () => {
    expect(supportsClaudeDeviceId('anthropic', 'oauth')).toBe(true)
    expect(supportsClaudeDeviceId('anthropic', 'setup-token')).toBe(true)
    expect(supportsClaudeDeviceId('anthropic', 'apikey')).toBe(false)
    expect(supportsClaudeDeviceId(undefined, undefined)).toBe(false)
  })
})
