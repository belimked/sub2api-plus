<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { isClaudeDeviceId, supportsClaudeDeviceId } from './claudeDeviceId'

const props = defineProps<{
  platform?: string
  type?: string
}>()
const model = defineModel<string>({ default: '' })
const { t } = useI18n()

const visible = computed(() => supportsClaudeDeviceId(props.platform, props.type))
const invalid = computed(() => !isClaudeDeviceId(model.value))
</script>

<template>
  <div v-if="visible">
    <label class="input-label">{{ t('admin.accounts.claudeDeviceId.label') }}</label>
    <input
      v-model="model"
      data-testid="claude-device-id"
      type="text"
      class="input font-mono"
      spellcheck="false"
      autocomplete="off"
      :placeholder="t('admin.accounts.claudeDeviceId.placeholder')"
    />
    <p v-if="invalid" role="alert" class="mt-1 text-xs text-red-600">
      {{ t('admin.accounts.claudeDeviceId.invalid') }}
    </p>
    <p v-else class="input-hint">{{ t('admin.accounts.claudeDeviceId.hint') }}</p>
  </div>
</template>
