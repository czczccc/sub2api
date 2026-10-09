import { describe, expect, it, vi, beforeEach } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import CodeBuddyTaskCenterModal from '../CodeBuddyTaskCenterModal.vue'
import type { Account } from '@/types'

const { listWorkBuddyGrowthTasks, runWorkBuddyGrowthTask, claimWorkBuddyGrowthTask } = vi.hoisted(() => ({
  listWorkBuddyGrowthTasks: vi.fn(),
  runWorkBuddyGrowthTask: vi.fn(),
  claimWorkBuddyGrowthTask: vi.fn()
}))

vi.mock('@/api/admin/workbuddy', () => ({
  listWorkBuddyGrowthTasks,
  runWorkBuddyGrowthTask,
  claimWorkBuddyGrowthTask
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn() })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

const account = { id: 7, name: 'wb', platform: 'codebuddy', credentials: {}, extra: {} } as unknown as Account

const task = (overrides: Record<string, unknown>) => ({
  task_code: 'x',
  title: 'x',
  credit: 100,
  energy: 0,
  locked: false,
  target: 1,
  current: 0,
  accept_status: 'accepted',
  claimable: false,
  claimed: false,
  auto: false,
  ...overrides
})

describe('CodeBuddyTaskCenterModal', () => {
  beforeEach(() => {
    listWorkBuddyGrowthTasks.mockReset()
    runWorkBuddyGrowthTask.mockReset()
    claimWorkBuddyGrowthTask.mockReset()
  })

  it('offers run for auto tasks, claim for ready tasks, and nothing for manual tasks', async () => {
    listWorkBuddyGrowthTasks.mockResolvedValue([
      task({ task_code: 'Model_chat_GLM5.2', auto: true, auto_hint: 'glm' }),
      task({ task_code: 'chat_5', current: 5, target: 5, claimable: true }),
      task({ task_code: 'RichMeow_Chat' })
    ])
    runWorkBuddyGrowthTask.mockResolvedValue('完成')
    const wrapper = mount(CodeBuddyTaskCenterModal, {
      props: { show: true, account },
      global: { stubs: { BaseDialog: { template: '<div><slot /></div>' } } }
    })
    await flushPromises()

    const auto = wrapper.get('[data-test="codebuddy-task-Model_chat_GLM5.2"]')
    expect(auto.text()).toContain('admin.workbuddySettings.taskCenter.run')
    expect(wrapper.get('[data-test="codebuddy-task-chat_5"]').text()).toContain('admin.workbuddySettings.taskCenter.claim')
    expect(wrapper.get('[data-test="codebuddy-task-RichMeow_Chat"]').text()).toContain('admin.workbuddySettings.taskCenter.manual')

    await auto.get('button').trigger('click')
    await flushPromises()
    expect(runWorkBuddyGrowthTask).toHaveBeenCalledWith(7, 'Model_chat_GLM5.2')
    expect(listWorkBuddyGrowthTasks).toHaveBeenCalledTimes(2)
  })
})
