<template>
  <BaseDialog
    :show="show"
    :title="t('admin.workbuddySettings.taskCenter.title', { name: account.name })"
    width="wide"
    @close="emit('close')"
  >
    <div class="space-y-3" data-test="codebuddy-task-center">
      <div class="flex justify-end">
        <button type="button" class="btn btn-secondary btn-sm" :disabled="loading" @click="load">
          {{ t('admin.workbuddySettings.taskCenter.refresh') }}
        </button>
      </div>
      <div v-if="loading && !tasks.length" class="py-8 text-center text-sm text-gray-500">
        {{ t('common.loading') }}
      </div>
      <div v-else-if="!tasks.length" class="py-8 text-center text-sm text-gray-500">
        {{ t('admin.workbuddySettings.taskCenter.empty') }}
      </div>
      <div
        v-for="task in tasks"
        :key="task.task_code"
        class="flex flex-wrap items-start justify-between gap-3 rounded-lg border border-gray-200 p-3 dark:border-dark-600"
        :data-test="`codebuddy-task-${task.task_code}`"
      >
        <div class="min-w-0 flex-1 space-y-0.5">
          <div class="text-sm font-medium text-gray-900 dark:text-white">
            {{ task.title || task.task_code }}
          </div>
          <div v-if="task.task_desc || task.description" class="text-xs text-gray-500 dark:text-gray-400">
            {{ task.task_desc || task.description }}
          </div>
          <div class="flex flex-wrap gap-2 text-xs text-gray-600 dark:text-gray-300">
            <span v-if="task.target > 0">
              {{ t('admin.workbuddySettings.taskCenter.progress', { current: task.current, target: task.target }) }}
            </span>
            <span v-if="task.credit || task.energy">{{ rewardLabel(task) }}</span>
            <span v-if="task.auto && task.auto_hint" class="text-primary-600 dark:text-primary-400">{{ task.auto_hint }}</span>
          </div>
          <div v-if="messages[task.task_code]" class="text-xs text-gray-700 dark:text-gray-200">
            {{ messages[task.task_code] }}
          </div>
        </div>
        <div class="flex shrink-0 items-center gap-2">
          <span v-if="task.claimed" class="text-xs text-green-600 dark:text-green-400">
            {{ t('admin.workbuddySettings.taskCenter.claimed') }}
          </span>
          <span v-else-if="task.locked" class="text-xs text-gray-400">
            {{ t('admin.workbuddySettings.taskCenter.locked') }}
          </span>
          <template v-else>
            <button
              v-if="task.claimable"
              type="button"
              class="btn btn-primary btn-sm"
              :disabled="busy !== null"
              @click="act(task, 'claim')"
            >
              {{ busy === task.task_code ? t('admin.workbuddySettings.taskCenter.running') : t('admin.workbuddySettings.taskCenter.claim') }}
            </button>
            <button
              v-else-if="task.auto"
              type="button"
              class="btn btn-secondary btn-sm"
              :disabled="busy !== null"
              @click="act(task, 'run')"
            >
              {{ busy === task.task_code ? t('admin.workbuddySettings.taskCenter.running') : t('admin.workbuddySettings.taskCenter.run') }}
            </button>
            <span v-else class="text-xs text-gray-400">
              {{ t('admin.workbuddySettings.taskCenter.manual') }}
            </span>
          </template>
        </div>
      </div>
    </div>
  </BaseDialog>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { useAppStore } from '@/stores/app'
import type { Account } from '@/types'
import {
  claimWorkBuddyGrowthTask,
  listWorkBuddyGrowthTasks,
  runWorkBuddyGrowthTask,
  type WorkBuddyGrowthTask
} from '@/api/admin/workbuddy'

const props = defineProps<{
  show: boolean
  account: Account
}>()

const emit = defineEmits<{ close: [] }>()

const { t } = useI18n()
const appStore = useAppStore()

const tasks = ref<WorkBuddyGrowthTask[]>([])
const loading = ref(false)
const busy = ref<string | null>(null)
const messages = ref<Record<string, string>>({})

const errorMessage = (error: unknown): string => {
  const err = error as { message?: string; response?: { data?: { message?: string } } }
  return err?.response?.data?.message || err?.message || ''
}

const rewardLabel = (task: WorkBuddyGrowthTask) =>
  task.energy
    ? t('admin.workbuddySettings.taskCenter.rewardEnergy', { credit: task.credit, energy: task.energy })
    : t('admin.workbuddySettings.taskCenter.reward', { credit: task.credit })

const load = async () => {
  loading.value = true
  try {
    tasks.value = await listWorkBuddyGrowthTasks(props.account.id)
  } catch (error) {
    appStore.showError(t('admin.workbuddySettings.taskCenter.loadFailed', { message: errorMessage(error) }))
  } finally {
    loading.value = false
  }
}

const act = async (task: WorkBuddyGrowthTask, action: 'run' | 'claim') => {
  busy.value = task.task_code
  try {
    const message =
      action === 'run'
        ? await runWorkBuddyGrowthTask(props.account.id, task.task_code)
        : await claimWorkBuddyGrowthTask(props.account.id, task.task_code)
    messages.value = { ...messages.value, [task.task_code]: message }
    await load()
  } catch (error) {
    const message = errorMessage(error)
    messages.value = { ...messages.value, [task.task_code]: message }
    appStore.showError(t('admin.workbuddySettings.taskCenter.actionFailed', { message }))
  } finally {
    busy.value = null
  }
}

watch(
  () => props.show,
  (show) => {
    if (show) {
      messages.value = {}
      void load()
    }
  },
  { immediate: true }
)
</script>
