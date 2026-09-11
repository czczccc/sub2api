<template>
  <div class="space-y-5">
    <div>
      <h3 class="text-lg font-semibold text-gray-900 dark:text-white">
        {{ t('admin.modelCapabilities.title') }}
      </h3>
      <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
        {{ t('admin.modelCapabilities.description') }}
      </p>
    </div>

    <div
      class="rounded-lg border border-amber-200 bg-amber-50 p-3 text-xs text-amber-700 dark:border-amber-900 dark:bg-amber-900/20 dark:text-amber-400"
    >
      {{ t('admin.modelCapabilities.advertiseOnlyHint') }}
    </div>

    <div v-if="loading" class="py-8 text-center text-sm text-gray-500">
      {{ t('common.loading') }}
    </div>

    <template v-else>
      <div v-if="entries.length === 0" class="rounded-lg border border-dashed border-gray-300 p-6 text-center text-sm text-gray-500 dark:border-dark-600">
        {{ t('admin.modelCapabilities.empty') }}
      </div>

      <div v-else class="space-y-3">
        <div
          v-for="(entry, index) in entries"
          :key="index"
          class="rounded-lg border border-gray-200 p-3 dark:border-dark-600"
        >
          <div class="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
            <div>
              <label class="input-label">{{ t('admin.modelCapabilities.platform') }}</label>
              <select v-model="entry.platform" class="input">
                <option value="">{{ t('admin.modelCapabilities.allPlatforms') }}</option>
                <option v-for="option in platformOptions" :key="option.value" :value="option.value">
                  {{ option.label }}
                </option>
              </select>
            </div>
            <div>
              <label class="input-label">{{ t('admin.modelCapabilities.modelId') }}</label>
              <input
                v-model="entry.model_id"
                type="text"
                class="input font-mono"
                :placeholder="t('admin.modelCapabilities.modelIdPlaceholder')"
              />
            </div>
            <div>
              <label class="input-label">{{ t('admin.modelCapabilities.contextWindow') }}</label>
              <input
                :value="entry.context_window ?? ''"
                type="number"
                min="0"
                class="input"
                :placeholder="t('admin.modelCapabilities.unset')"
                @input="entry.context_window = readNumber($event)"
              />
            </div>
            <div>
              <label class="input-label">{{ t('admin.modelCapabilities.maxOutputTokens') }}</label>
              <input
                :value="entry.max_output_tokens ?? ''"
                type="number"
                min="0"
                class="input"
                :placeholder="t('admin.modelCapabilities.unset')"
                @input="entry.max_output_tokens = readNumber($event)"
              />
            </div>
          </div>

          <div class="mt-3 flex flex-wrap items-center justify-between gap-3">
            <div class="flex flex-wrap items-center gap-3">
              <span class="text-xs font-medium text-gray-600 dark:text-gray-400">
                {{ t('admin.modelCapabilities.inputModalities') }}
              </span>
              <label
                v-for="modality in MODEL_CAPABILITY_MODALITIES"
                :key="modality"
                class="inline-flex items-center gap-1.5 text-xs text-gray-700 dark:text-gray-300"
              >
                <input
                  type="checkbox"
                  :checked="entry.input_modalities?.includes(modality) ?? false"
                  @change="toggleModality(entry, modality)"
                />
                {{ t(`admin.modelCapabilities.modalities.${modality}`) }}
              </label>
            </div>
            <button
              type="button"
              class="text-sm text-red-600 hover:text-red-700 dark:text-red-400"
              @click="removeEntry(index)"
            >
              {{ t('common.delete') }}
            </button>
          </div>
        </div>
      </div>

      <div class="flex flex-wrap gap-2">
        <button type="button" class="btn btn-secondary" @click="addEntry">
          + {{ t('admin.modelCapabilities.addEntry') }}
        </button>
        <button type="button" class="btn btn-secondary" @click="addCodeBuddyPreset">
          {{ t('admin.modelCapabilities.addCodeBuddyPreset') }}
        </button>
        <button type="button" class="btn btn-primary" :disabled="saving" @click="save">
          {{ saving ? t('common.saving') : t('common.save') }}
        </button>
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { CONCRETE_PLATFORM_OPTIONS } from '@/constants/platforms'
import {
  getModelCapabilities,
  updateModelCapabilities,
  MODEL_CAPABILITY_MODALITIES,
  type ModelCapabilityEntry
} from '@/api/admin/modelCapabilities'

const { t } = useI18n()
const appStore = useAppStore()

const platformOptions = CONCRETE_PLATFORM_OPTIONS

const entries = ref<ModelCapabilityEntry[]>([])
const loading = ref(true)
const saving = ref(false)

const readNumber = (event: Event): number | undefined => {
  const raw = (event.target as HTMLInputElement).value.trim()
  if (raw === '') return undefined
  const parsed = Number(raw)
  return Number.isFinite(parsed) && parsed > 0 ? Math.floor(parsed) : undefined
}

const addEntry = () => {
  entries.value.push({ platform: '', model_id: '', input_modalities: ['text'] })
}

/**
 * CodeBuddy 的 15 个模型实测全部支持图片输入，但上游目录不返回任何能力字段。
 * 这个预置把平台默认模态显式化，管理员只需补齐上下文窗口。
 */
const addCodeBuddyPreset = () => {
  for (const modelID of codeBuddyModelIDs) {
    if (entries.value.some(entry => entry.platform === 'codebuddy' && entry.model_id === modelID)) {
      continue
    }
    entries.value.push({
      platform: 'codebuddy',
      model_id: modelID,
      input_modalities: ['text', 'image']
    })
  }
}

const codeBuddyModelIDs = [
  'auto',
  'hy4-preview',
  'hy3',
  'hy3-x',
  'deepseek-v4.1-flash',
  'deepseek-v4-pro',
  'glm-5.3',
  'glm-5.3-flash',
  'glm-5.2',
  'glm-5.1',
  'glm-5v-turbo',
  'kimi-k3-1',
  'kimi-k2.7',
  'kimi-k2.6',
  'minimax-m3'
]

const removeEntry = (index: number) => {
  entries.value.splice(index, 1)
}

const toggleModality = (entry: ModelCapabilityEntry, modality: string) => {
  const current = entry.input_modalities ?? []
  entry.input_modalities = current.includes(modality)
    ? current.filter(value => value !== modality)
    : [...current, modality]
}

const load = async () => {
  loading.value = true
  try {
    const config = await getModelCapabilities()
    entries.value = config.models ?? []
  } catch (error) {
    appStore.showError(
      t('admin.modelCapabilities.loadFailed', {
        message: error instanceof Error ? error.message : ''
      })
    )
  } finally {
    loading.value = false
  }
}

const save = async () => {
  saving.value = true
  try {
    const saved = await updateModelCapabilities({ models: entries.value })
    entries.value = saved.models ?? []
    appStore.showSuccess(t('admin.modelCapabilities.saveSuccess'))
  } catch (error) {
    appStore.showError(
      t('admin.modelCapabilities.saveFailed', {
        message: error instanceof Error ? error.message : ''
      })
    )
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>
