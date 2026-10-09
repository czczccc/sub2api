<template>
  <div v-if="visible" class="space-y-0.5" data-test="codebuddy-credits">
    <div class="flex flex-wrap items-center gap-1.5">
      <span
        data-test="codebuddy-credits-value"
        class="text-[10px] font-medium leading-4 text-gray-700 dark:text-gray-200"
        :title="checkedAtTitle"
      >
        {{ t('admin.accounts.codebuddy.credits.label') }}
        <template v-if="remain !== null">
          {{ t('admin.accounts.codebuddy.credits.remain', { remain: formatCredits(remain), total: formatCredits(total ?? remain) }) }}
        </template>
        <template v-else>{{ t('admin.accounts.codebuddy.credits.empty') }}</template>
      </span>
      <button
        type="button"
        data-test="codebuddy-credits-refresh"
        class="inline-flex items-center gap-0.5 whitespace-nowrap rounded px-1.5 py-0.5 text-[10px] font-medium leading-4 text-blue-600 transition-colors hover:bg-blue-50 disabled:cursor-not-allowed disabled:opacity-50 dark:text-blue-400 dark:hover:bg-blue-900/30"
        :disabled="loading"
        :title="t('admin.accounts.codebuddy.credits.refreshTooltip')"
        @click="handleRefresh"
      >
        <svg
          class="h-2.5 w-2.5"
          :class="{ 'animate-spin': loading }"
          fill="none"
          stroke="currentColor"
          viewBox="0 0 24 24"
        >
          <path
            stroke-linecap="round"
            stroke-linejoin="round"
            stroke-width="2"
            d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"
          />
        </svg>
        {{ t('admin.accounts.codebuddy.credits.refresh') }}
      </button>
    </div>
    <div v-if="expireLabel" class="text-[10px] leading-4 text-amber-600 dark:text-amber-400">
      {{ expireLabel }}
    </div>
    <div
      v-if="errorText"
      data-test="codebuddy-credits-error"
      class="max-w-[220px] truncate text-[10px] text-red-600 dark:text-red-400"
      :title="errorText"
    >
      {{ errorText }}
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { Account } from '@/types'
import { formatDateTime } from '@/utils/format'
import { codeBuddyCreditsSupported } from './credentialsBuilder'

const props = defineProps<{
  account: Account
}>()

const emit = defineEmits<{
  'account-updated': [account: Account]
}>()

const { t } = useI18n()

const visible = computed(() => codeBuddyCreditsSupported(props.account))
const loading = ref(false)
const requestError = ref<string | null>(null)

const extra = computed(() => (props.account.extra ?? {}) as Record<string, unknown>)
const numberField = (key: string): number | null => {
  const v = extra.value[key]
  return typeof v === 'number' && Number.isFinite(v) ? v : null
}
const stringField = (key: string): string => {
  const v = extra.value[key]
  return typeof v === 'string' ? v : ''
}

const remain = computed(() => numberField('codebuddy_credits_remain'))
const total = computed(() => numberField('codebuddy_credits_total'))

const expireLabel = computed(() => {
  const value = stringField('codebuddy_credits_expire_at')
  if (!value || remain.value === null || remain.value <= 0) return ''
  return t('admin.accounts.codebuddy.credits.expireAt', { date: formatDateTime(new Date(value)) })
})

const checkedAtTitle = computed(() => {
  const value = stringField('codebuddy_credits_checked_at')
  return value ? `${t('admin.accounts.codebuddy.credits.checkedAt')} ${formatDateTime(new Date(value))}` : ''
})

const errorText = computed(() => requestError.value || stringField('codebuddy_credits_error'))

const formatCredits = (value: number): string =>
  Number.isInteger(value) ? String(value) : value.toFixed(2)

const extractErrorMessage = (e: unknown): string => {
  const err = e as { message?: string; response?: { data?: { message?: string } } }
  return err?.response?.data?.message || err?.message || t('common.error')
}

const handleRefresh = async () => {
  if (loading.value) return
  loading.value = true
  requestError.value = null
  try {
    const result = await adminAPI.accounts.refreshCodeBuddyCredits(props.account.id)
    emit('account-updated', { ...props.account, extra: { ...extra.value, ...result.extra } })
  } catch (e) {
    requestError.value = extractErrorMessage(e)
  } finally {
    loading.value = false
  }
}
</script>
