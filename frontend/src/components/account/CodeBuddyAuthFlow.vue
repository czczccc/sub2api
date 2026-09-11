<template>
  <div class="rounded-lg border border-sky-200 bg-sky-50 p-4 dark:border-sky-700 dark:bg-sky-900/30">
    <div class="flex items-start gap-4">
      <div class="flex h-10 w-10 flex-shrink-0 items-center justify-center rounded-lg bg-sky-500">
        <Icon name="link" size="md" class="text-white" />
      </div>
      <div class="min-w-0 flex-1">
        <h4 class="mb-3 font-semibold text-sky-900 dark:text-sky-200">
          {{ t('admin.accounts.codebuddy.auth.title') }}
        </h4>

        <!-- 获取方式 -->
        <div class="mb-4 flex flex-wrap gap-4">
          <label class="flex cursor-pointer items-center gap-2">
            <input v-model="mode" type="radio" value="oauth" class="text-sky-600 focus:ring-sky-500" />
            <span class="text-sm text-sky-900 dark:text-sky-200">
              {{ t('admin.accounts.codebuddy.auth.methodOAuth') }}
            </span>
          </label>
          <label class="flex cursor-pointer items-center gap-2">
            <input v-model="mode" type="radio" value="manual" class="text-sky-600 focus:ring-sky-500" />
            <span class="text-sm text-sky-900 dark:text-sky-200">
              {{ t('admin.accounts.codebuddy.auth.methodManual') }}
            </span>
          </label>
        </div>

        <template v-if="mode === 'oauth'">
          <p class="mb-3 text-sm text-sky-800 dark:text-sky-300">
            {{ t('admin.accounts.codebuddy.auth.followSteps') }}
          </p>

          <!-- 步骤 1：生成授权链接 -->
          <div class="mb-3 rounded-lg border border-sky-200 bg-white p-3 dark:border-sky-800 dark:bg-dark-700">
            <div class="flex items-start gap-3">
              <span
                class="flex h-6 w-6 flex-shrink-0 items-center justify-center rounded-full bg-sky-500 text-xs font-semibold text-white"
                >1</span
              >
              <div class="min-w-0 flex-1">
                <p class="text-sm font-medium text-gray-900 dark:text-white">
                  {{ t('admin.accounts.codebuddy.auth.step1Title') }}
                </p>
                <button
                  type="button"
                  class="btn btn-primary btn-sm mt-2"
                  :disabled="starting"
                  data-testid="codebuddy-generate-link"
                  @click="handleStart"
                >
                  <Icon name="link" size="sm" />
                  {{
                    authUrl
                      ? t('admin.accounts.codebuddy.auth.regenerateLink')
                      : t('admin.accounts.codebuddy.auth.generateLink')
                  }}
                </button>
              </div>
            </div>
          </div>

          <!-- 步骤 2：打开链接完成登录 -->
          <div
            v-if="authUrl"
            class="mb-3 rounded-lg border border-sky-200 bg-white p-3 dark:border-sky-800 dark:bg-dark-700"
          >
            <div class="flex items-start gap-3">
              <span
                class="flex h-6 w-6 flex-shrink-0 items-center justify-center rounded-full bg-sky-500 text-xs font-semibold text-white"
                >2</span
              >
              <div class="min-w-0 flex-1">
                <p class="text-sm font-medium text-gray-900 dark:text-white">
                  {{ t('admin.accounts.codebuddy.auth.step2Title') }}
                </p>
                <p class="mt-1 text-xs text-gray-600 dark:text-gray-300">
                  {{ t('admin.accounts.codebuddy.auth.openHint') }}
                </p>
                <input
                  :value="authUrl"
                  type="text"
                  readonly
                  class="input mt-2 font-mono text-xs"
                  data-testid="codebuddy-auth-url"
                />
                <div class="mt-2 flex flex-wrap gap-2">
                  <button type="button" class="btn btn-secondary btn-sm" @click="handleCopy">
                    {{ copied ? t('admin.accounts.codebuddy.auth.copied') : t('admin.accounts.codebuddy.auth.copy') }}
                  </button>
                  <a class="btn btn-secondary btn-sm" :href="authUrl" target="_blank" rel="noopener noreferrer">
                    <Icon name="externalLink" size="sm" />
                    {{ t('admin.accounts.codebuddy.auth.open') }}
                  </a>
                </div>
              </div>
            </div>
          </div>

          <!-- 步骤 3：取回凭据 -->
          <div v-if="authUrl" class="rounded-lg border border-sky-200 bg-white p-3 dark:border-sky-800 dark:bg-dark-700">
            <div class="flex items-start gap-3">
              <span
                class="flex h-6 w-6 flex-shrink-0 items-center justify-center rounded-full bg-sky-500 text-xs font-semibold text-white"
                >3</span
              >
              <div class="min-w-0 flex-1">
                <p class="text-sm font-medium text-gray-900 dark:text-white">
                  {{ t('admin.accounts.codebuddy.auth.step3Title') }}
                </p>
                <p v-if="pending" class="mt-1 text-xs text-gray-600 dark:text-gray-300">
                  {{ t('admin.accounts.codebuddy.auth.stillPending') }}
                </p>
                <button
                  type="button"
                  class="btn btn-primary btn-sm mt-2"
                  :disabled="polling"
                  data-testid="codebuddy-fetch-credentials"
                  @click="handlePollOnce"
                >
                  <Icon name="refresh" size="sm" />
                  {{
                    polling
                      ? t('admin.accounts.codebuddy.auth.fetching')
                      : t('admin.accounts.codebuddy.auth.fetchCredentials')
                  }}
                </button>
              </div>
            </div>
          </div>
        </template>

        <p v-else class="text-sm text-sky-800 dark:text-sky-300">
          {{ t('admin.accounts.codebuddy.auth.manualHint') }}
        </p>

        <p v-if="errorMessage" class="mt-3 text-sm text-red-600 dark:text-red-400" data-testid="codebuddy-auth-error">
          {{ errorMessage }}
        </p>
        <p
          v-if="successText"
          class="mt-3 flex items-center gap-1 text-sm text-green-700 dark:text-green-400"
          data-testid="codebuddy-auth-success"
        >
          <Icon name="checkCircle" size="sm" />
          {{ successText }}
        </p>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import {
  pollCodeBuddyAuth,
  startCodeBuddyAuth,
  type CodeBuddyAuthCredentials
} from '@/api/admin/accounts'

/** oauth = 向导式授权（推荐）；manual = 手工填写 token。 */
const mode = defineModel<'oauth' | 'manual'>('mode', { default: 'oauth' })

const emit = defineEmits<{
  /** 授权成功：把凭据交给父组件填入表单（父组件是表单状态的唯一拥有者）。 */
  authorized: [credentials: CodeBuddyAuthCredentials, nickname: string]
}>()

const { t } = useI18n()

const starting = ref(false)
const authUrl = ref('')
const state = ref('')
const polling = ref(false)
const pending = ref(false)
const errorMessage = ref('')
const successText = ref('')
const copied = ref(false)

let pollTimer: ReturnType<typeof setInterval> | null = null
let pollDeadline = 0

const POLL_INTERVAL_MS = 3000
const POLL_TIMEOUT_MS = 10 * 60 * 1000

function stopPolling() {
  if (pollTimer !== null) {
    clearInterval(pollTimer)
    pollTimer = null
  }
  polling.value = false
}

onUnmounted(stopPolling)

function handleStart() {
  stopPolling()
  starting.value = true
  errorMessage.value = ''
  successText.value = ''
  pending.value = false
  copied.value = false
  startCodeBuddyAuth()
    .then((session) => {
      authUrl.value = session.auth_url
      state.value = session.state
      // 生成链接后自动开始轮询，用户只需去浏览器完成登录。
      startPolling()
    })
    .catch((error: unknown) => {
      errorMessage.value = extractError(error) || t('admin.accounts.codebuddy.auth.errorStart')
    })
    .finally(() => {
      starting.value = false
    })
}

function startPolling() {
  if (!state.value) return
  stopPolling()
  polling.value = true
  pending.value = false
  pollDeadline = Date.now() + POLL_TIMEOUT_MS
  void pollOnce()
  pollTimer = setInterval(() => {
    if (Date.now() > pollDeadline) {
      stopPolling()
      errorMessage.value = t('admin.accounts.codebuddy.auth.errorTimeout')
      return
    }
    void pollOnce()
  }, POLL_INTERVAL_MS)
}

/** 手动触发一次拉取（按钮）。 */
function handlePollOnce() {
  if (!state.value) return
  if (!polling.value) {
    polling.value = true
    pending.value = false
    pollDeadline = Date.now() + POLL_TIMEOUT_MS
    pollTimer = setInterval(() => {
      if (Date.now() > pollDeadline) {
        stopPolling()
        errorMessage.value = t('admin.accounts.codebuddy.auth.errorTimeout')
        return
      }
      void pollOnce()
    }, POLL_INTERVAL_MS)
  }
  void pollOnce()
}

async function pollOnce() {
  try {
    const result = await pollCodeBuddyAuth(state.value)
    if (result.status !== 'ready' || !result.credentials) {
      pending.value = true
      return
    }
    stopPolling()
    pending.value = false
    errorMessage.value = ''
    const nickname = result.nickname?.trim() ?? ''
    successText.value = nickname
      ? t('admin.accounts.codebuddy.auth.successNamed', { name: nickname })
      : t('admin.accounts.codebuddy.auth.success')
    emit('authorized', { ...result.credentials }, nickname)
  } catch (error: unknown) {
    stopPolling()
    errorMessage.value = extractError(error) || t('admin.accounts.codebuddy.auth.errorPoll')
  }
}

function extractError(error: unknown): string {
  const candidate = error as { response?: { data?: { message?: string; error?: string } }; message?: string }
  return candidate?.response?.data?.message || candidate?.response?.data?.error || candidate?.message || ''
}

async function handleCopy() {
  try {
    await navigator.clipboard.writeText(authUrl.value)
    copied.value = true
    setTimeout(() => {
      copied.value = false
    }, 2000)
  } catch {
    errorMessage.value = t('admin.accounts.codebuddy.auth.copyFailed')
  }
}

// 切到手工方式时停止轮询，避免后台继续打上游。
watch(mode, (value) => {
  if (value === 'manual') stopPolling()
})
</script>
