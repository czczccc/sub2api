<template>
  <BaseDialog :show="show" :title="t('admin.workbuddyCredits.title')" width="wide" @close="emit('close')">
    <div v-if="group" class="space-y-4" data-test="workbuddy-credits-summary">
      <div class="flex flex-wrap items-center justify-between gap-2 rounded-lg bg-gray-50 px-4 py-2.5 text-sm dark:bg-dark-700">
        <span class="font-medium text-gray-900 dark:text-white">{{ group.name }}</span>
        <button type="button" class="btn btn-secondary btn-sm" :disabled="loading" @click="load">
          {{ t('admin.workbuddyCredits.reload') }}
        </button>
      </div>

      <div v-if="loading && !summary" class="py-8 text-center text-sm text-gray-500">{{ t('common.loading') }}</div>

      <template v-else-if="summary">
        <div class="grid grid-cols-1 gap-3 sm:grid-cols-3">
          <div class="rounded-lg border border-gray-200 p-3 dark:border-dark-600">
            <div class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.workbuddyCredits.remain') }}</div>
            <div class="mt-1 text-xl font-semibold text-gray-900 dark:text-white" data-test="credits-remain">
              {{ formatCredits(summary.remain) }}
            </div>
            <div class="text-xs text-gray-500 dark:text-gray-400">
              {{ t('admin.workbuddyCredits.ofTotal', { total: formatCredits(summary.total) }) }}
            </div>
          </div>
          <div class="rounded-lg border border-gray-200 p-3 dark:border-dark-600">
            <div class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.workbuddyCredits.tokens') }}</div>
            <div class="mt-1 text-xl font-semibold text-gray-900 dark:text-white" data-test="credits-tokens">
              {{ estimate ? '≈ ' + formatTokenCount(estimate.tokens, locale) : '—' }}
            </div>
            <div class="text-xs text-gray-500 dark:text-gray-400">
              <template v-if="estimate">
                {{ t('admin.workbuddyCredits.tokenSplit', {
                  input: formatTokenCount(estimate.inputTokens, locale),
                  output: formatTokenCount(estimate.outputTokens, locale)
                }) }}
              </template>
              <template v-else>{{ t('admin.workbuddyCredits.noMultiplier') }}</template>
            </div>
          </div>
          <div class="rounded-lg border border-gray-200 p-3 dark:border-dark-600">
            <div class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.workbuddyCredits.officialValue') }}</div>
            <div class="mt-1 text-xl font-semibold text-gray-900 dark:text-white" data-test="credits-cost">
              {{ estimate ? '¥' + formatMoney(estimate.costIdle) : '—' }}
            </div>
            <div class="text-xs text-gray-500 dark:text-gray-400">
              <template v-if="estimate">{{ t('admin.workbuddyCredits.peakValue', { cost: formatMoney(estimate.costPeak) }) }}</template>
            </div>
          </div>
        </div>

        <div class="grid grid-cols-1 gap-3 rounded-lg border border-gray-200 p-3 text-sm dark:border-dark-600 sm:grid-cols-3">
          <label class="space-y-1">
            <span class="block text-xs text-gray-600 dark:text-gray-300">{{ t('admin.workbuddyCredits.multiplier') }}</span>
            <input v-model.number="multiplier" type="number" min="0" step="0.01" class="input w-full py-1 text-sm" />
            <span class="block text-xs text-gray-500 dark:text-gray-400">{{ multiplierSource }}</span>
          </label>
          <label class="space-y-1">
            <span class="block text-xs text-gray-600 dark:text-gray-300">{{ t('admin.workbuddyCredits.tokensPerCredit') }}</span>
            <input v-model.number="tokensPerCredit" type="number" min="1" step="1" class="input w-full py-1 text-sm" />
            <span class="block text-xs text-gray-500 dark:text-gray-400">{{ t('admin.workbuddyCredits.tokensPerCreditHint') }}</span>
          </label>
          <label class="space-y-1">
            <span class="block text-xs text-gray-600 dark:text-gray-300">{{ t('admin.workbuddyCredits.outputPercent') }}</span>
            <input v-model.number="outputPercent" type="number" min="0" max="100" step="1" class="input w-full py-1 text-sm" />
            <span class="block text-xs text-gray-500 dark:text-gray-400">{{ t('admin.workbuddyCredits.outputPercentHint') }}</span>
          </label>
        </div>

        <p class="text-xs text-gray-500 dark:text-gray-400">
          {{ t('admin.workbuddyCredits.formula', {
            idleInput: DEEPSEEK_FLASH_PRICE.idle.input,
            idleOutput: DEEPSEEK_FLASH_PRICE.idle.output,
            peakInput: DEEPSEEK_FLASH_PRICE.peak.input,
            peakOutput: DEEPSEEK_FLASH_PRICE.peak.output
          }) }}
          <a :href="DEEPSEEK_PRICING_URL" target="_blank" rel="noopener noreferrer" class="text-primary-600 hover:underline dark:text-primary-400">
            {{ t('admin.workbuddyCredits.pricingLink') }}
          </a>
        </p>

        <p class="text-xs text-gray-500 dark:text-gray-400">
          {{ t('admin.workbuddyCredits.coverage', {
            reported: summary.reported_count,
            count: summary.account_count
          }) }}
          <template v-if="summary.unsupported_count > 0">
            {{ t('admin.workbuddyCredits.unsupported', { count: summary.unsupported_count }) }}
          </template>
          <template v-if="summary.error_count > 0">
            {{ t('admin.workbuddyCredits.errors', { count: summary.error_count }) }}
          </template>
          <template v-if="summary.oldest_checked_at">
            {{ t('admin.workbuddyCredits.oldestChecked', { time: formatDateTimeToMinute(summary.oldest_checked_at) }) }}
          </template>
          <template v-if="summary.earliest_expire_at">
            {{ t('admin.workbuddyCredits.earliestExpire', { time: formatDateTimeToMinute(summary.earliest_expire_at) }) }}
          </template>
        </p>

        <div class="max-h-72 overflow-y-auto rounded-lg border border-gray-200 dark:border-dark-600">
          <table class="w-full text-left text-sm">
            <thead class="sticky top-0 bg-gray-50 text-xs text-gray-500 dark:bg-dark-700 dark:text-gray-400">
              <tr>
                <th class="px-3 py-2">{{ t('admin.workbuddyCredits.columns.account') }}</th>
                <th class="px-3 py-2 text-right">{{ t('admin.workbuddyCredits.columns.remain') }}</th>
                <th class="px-3 py-2">{{ t('admin.workbuddyCredits.columns.expire') }}</th>
                <th class="px-3 py-2">{{ t('admin.workbuddyCredits.columns.checked') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-if="summary.accounts.length === 0">
                <td colspan="4" class="px-3 py-6 text-center text-gray-500">{{ t('admin.workbuddyCredits.empty') }}</td>
              </tr>
              <tr
                v-for="row in summary.accounts"
                :key="row.id"
                class="border-t border-gray-100 dark:border-dark-700"
                data-test="credits-account-row"
              >
                <td class="px-3 py-1.5 text-gray-900 dark:text-white">
                  {{ row.name }}
                  <span v-if="row.error" class="ml-1 text-xs text-red-500" :title="row.error">{{ t('admin.workbuddyCredits.queryFailed') }}</span>
                </td>
                <td class="px-3 py-1.5 text-right tabular-nums text-gray-700 dark:text-gray-200">
                  <template v-if="!row.supported">{{ t('admin.workbuddyCredits.notSupported') }}</template>
                  <template v-else-if="row.remain === undefined">—</template>
                  <template v-else>{{ formatCredits(row.remain) }} / {{ formatCredits(row.total ?? row.remain) }}</template>
                </td>
                <td class="px-3 py-1.5 text-gray-500 dark:text-gray-400">{{ row.expire_at ? formatDateTimeToMinute(row.expire_at) : '—' }}</td>
                <td class="px-3 py-1.5 text-gray-500 dark:text-gray-400">{{ row.checked_at ? formatDateTimeToMinute(row.checked_at) : '—' }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </template>
    </div>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { AdminGroup } from '@/types'
import { useAppStore } from '@/stores/app'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { getWorkBuddyConfig, getWorkBuddyCreditsSummary, type WorkBuddyCreditsSummary } from '@/api/admin/workbuddy'
import { formatDateTimeToMinute } from '@/utils/format'
import {
  DEEPSEEK_FLASH_PRICE,
  DEEPSEEK_PRICING_URL,
  estimateCreditTokens,
  formatTokenCount
} from '@/utils/workbuddyCredits'

const props = defineProps<{
  show: boolean
  group: AdminGroup | null
}>()

const emit = defineEmits<{
  close: []
}>()

const { t, locale } = useI18n()
const appStore = useAppStore()

const loading = ref(false)
const summary = ref<WorkBuddyCreditsSummary | null>(null)
// 折算参数以 WorkBuddy 设置为初值，弹窗里可临时修改试算（不落盘）。
const multiplier = ref(0)
const tokensPerCredit = ref(0)
const outputPercent = ref(20)
const settingsMultiplier = ref(0)

const estimate = computed(() =>
  summary.value
    ? estimateCreditTokens(summary.value.remain, multiplier.value, tokensPerCredit.value, outputPercent.value)
    : null
)

const multiplierSource = computed(() => {
  const upstream = summary.value?.deepseek_flash
  if (settingsMultiplier.value > 0 && multiplier.value === settingsMultiplier.value) {
    return t('admin.workbuddyCredits.multiplierFromSettings')
  }
  if (upstream && upstream.multiplier === multiplier.value) {
    return t('admin.workbuddyCredits.multiplierFromUpstream', { model: upstream.model_id })
  }
  if (!upstream) return t('admin.workbuddyCredits.multiplierMissing')
  return t('admin.workbuddyCredits.multiplierManual')
})

const formatCredits = (value: number) =>
  new Intl.NumberFormat(locale.value, { maximumFractionDigits: 2 }).format(value)
const formatMoney = (value: number) =>
  new Intl.NumberFormat(locale.value, { minimumFractionDigits: 2, maximumFractionDigits: 2 }).format(value)

const load = async () => {
  if (!props.group) return
  loading.value = true
  try {
    const [result, settings] = await Promise.all([
      getWorkBuddyCreditsSummary(props.group.id),
      getWorkBuddyConfig()
    ])
    summary.value = result
    const config = settings.config
    tokensPerCredit.value = config.credit_tokens_per_credit
    outputPercent.value = config.credit_output_percent
    settingsMultiplier.value = config.deepseek_flash_multiplier
    multiplier.value = config.deepseek_flash_multiplier > 0
      ? config.deepseek_flash_multiplier
      : result.deepseek_flash?.multiplier ?? 0
  } catch (error) {
    appStore.showError(t('admin.workbuddyCredits.loadFailed', { message: error instanceof Error ? error.message : '' }))
  } finally {
    loading.value = false
  }
}

watch(
  () => props.show,
  (visible) => {
    if (visible) {
      summary.value = null
      void load()
    }
  }
)
</script>
