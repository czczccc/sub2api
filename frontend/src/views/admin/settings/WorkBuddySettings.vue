<template>
  <div class="space-y-5">
    <div>
      <h3 class="text-lg font-semibold text-gray-900 dark:text-white">
        {{ t('admin.workbuddySettings.title') }}
      </h3>
      <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
        {{ t('admin.workbuddySettings.description') }}
      </p>
    </div>

    <div v-if="loading" class="py-8 text-center text-sm text-gray-500">
      {{ t('common.loading') }}
    </div>

    <template v-else>
      <section class="space-y-2 rounded-lg border border-gray-200 p-4 dark:border-dark-600">
        <label class="flex items-center gap-2 text-sm font-medium text-gray-900 dark:text-white">
          <input v-model="sanitizeEnabled" type="checkbox" data-testid="workbuddy-sanitize" />
          {{ t('admin.workbuddySettings.sanitize.title') }}
        </label>
        <p class="text-xs text-gray-500 dark:text-gray-400">
          {{ t('admin.workbuddySettings.sanitize.description') }}
        </p>
      </section>

      <section class="space-y-3 rounded-lg border border-gray-200 p-4 dark:border-dark-600">
        <h4 class="text-sm font-medium text-gray-900 dark:text-white">
          {{ t('admin.workbuddySettings.prompt.title') }}
        </h4>
        <div>
          <label class="input-label">{{ t('admin.workbuddySettings.prompt.mode') }}</label>
          <select v-model="config.prompt_mode" class="input" data-testid="workbuddy-prompt-mode">
            <option v-for="mode in WORKBUDDY_PROMPT_MODES" :key="mode" :value="mode">
              {{ t(`admin.workbuddySettings.prompt.modes.${mode}`) }}
            </option>
          </select>
          <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
            {{ t(`admin.workbuddySettings.prompt.modeHints.${config.prompt_mode}`) }}
          </p>
        </div>
        <div v-if="usesPromptText">
          <div class="flex items-center justify-between">
            <label class="input-label">{{ t('admin.workbuddySettings.prompt.text') }}</label>
            <button type="button" class="text-xs text-primary-600 hover:underline" @click="config.prompt_text = defaultPrompt">
              {{ t('admin.workbuddySettings.prompt.useDefault') }}
            </button>
          </div>
          <textarea
            v-model="config.prompt_text"
            rows="10"
            class="input font-mono text-xs"
            :placeholder="defaultPrompt"
          />
          <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
            {{ t('admin.workbuddySettings.prompt.textHint') }}
          </p>
        </div>
      </section>

      <div class="flex justify-end">
        <button type="button" class="btn btn-primary" :disabled="saving" @click="save">
          {{ saving ? t('common.saving') : t('common.save') }}
        </button>
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import {
  getWorkBuddyConfig,
  updateWorkBuddyConfig,
  WORKBUDDY_PROMPT_MODES,
  type WorkBuddyConfig
} from '@/api/admin/workbuddy'

const { t } = useI18n()
const appStore = useAppStore()

const loading = ref(true)
const saving = ref(false)
const defaultPrompt = ref('')
const config = ref<WorkBuddyConfig>({
  sanitize_disabled: false,
  prompt_mode: 'degrade',
  prompt_text: ''
})

const sanitizeEnabled = computed({
  get: () => !config.value.sanitize_disabled,
  set: (value: boolean) => {
    config.value.sanitize_disabled = !value
  }
})

const usesPromptText = computed(
  () => config.value.prompt_mode === 'append' || config.value.prompt_mode === 'custom'
)

const errorMessage = (error: unknown) => (error instanceof Error ? error.message : '')

const apply = (response: { config: WorkBuddyConfig; default_prompt: string }) => {
  config.value = { ...config.value, ...response.config }
  defaultPrompt.value = response.default_prompt
}

const load = async () => {
  loading.value = true
  try {
    apply(await getWorkBuddyConfig())
  } catch (error) {
    appStore.showError(t('admin.workbuddySettings.loadFailed', { message: errorMessage(error) }))
  } finally {
    loading.value = false
  }
}

const save = async () => {
  saving.value = true
  try {
    apply(await updateWorkBuddyConfig(config.value))
    appStore.showSuccess(t('admin.workbuddySettings.saveSuccess'))
  } catch (error) {
    appStore.showError(t('admin.workbuddySettings.saveFailed', { message: errorMessage(error) }))
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>
