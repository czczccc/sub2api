import { describe, expect, it, vi, beforeEach } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import CodeBuddyCreditsCell from '../CodeBuddyCreditsCell.vue'
import type { Account } from '@/types'

const { refreshCodeBuddyCredits } = vi.hoisted(() => ({
  refreshCodeBuddyCredits: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      refreshCodeBuddyCredits
    }
  }
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) =>
        params ? `${key}:${JSON.stringify(params)}` : key
    })
  }
})

function makeAccount(overrides: Partial<Account> = {}): Account {
  return {
    id: 7,
    platform: 'codebuddy',
    type: 'apikey',
    credentials: { product: 'workbuddy', region: 'china' },
    extra: {},
    ...overrides
  } as Account
}

describe('CodeBuddyCreditsCell', () => {
  beforeEach(() => {
    refreshCodeBuddyCredits.mockReset()
  })

  it('shows stored remaining credits', () => {
    const wrapper = mount(CodeBuddyCreditsCell, {
      props: {
        account: makeAccount({
          extra: { codebuddy_credits_remain: 120.5, codebuddy_credits_total: 500 }
        })
      }
    })
    expect(wrapper.get('[data-test="codebuddy-credits-value"]').text()).toContain('"remain":"120.50"')
    expect(wrapper.get('[data-test="codebuddy-credits-value"]').text()).toContain('"total":"500"')
  })

  it('hides for international accounts', () => {
    const wrapper = mount(CodeBuddyCreditsCell, {
      props: { account: makeAccount({ credentials: { product: 'workbuddy', region: 'global' } }) }
    })
    expect(wrapper.find('[data-test="codebuddy-credits"]').exists()).toBe(false)
  })

  it('refreshes and emits the merged account', async () => {
    refreshCodeBuddyCredits.mockResolvedValue({
      extra: { codebuddy_credits_remain: 80, codebuddy_credits_total: 100, codebuddy_credits_error: '' }
    })
    const wrapper = mount(CodeBuddyCreditsCell, {
      props: { account: makeAccount({ extra: { codebuddy_auto_checkin: true } }) }
    })
    await wrapper.get('[data-test="codebuddy-credits-refresh"]').trigger('click')
    await flushPromises()

    expect(refreshCodeBuddyCredits).toHaveBeenCalledWith(7)
    const emitted = wrapper.emitted('account-updated')?.[0]?.[0] as Account
    expect(emitted.extra).toMatchObject({
      codebuddy_auto_checkin: true,
      codebuddy_credits_remain: 80,
      codebuddy_credits_total: 100
    })
  })

  it('shows the stored upstream error', () => {
    const wrapper = mount(CodeBuddyCreditsCell, {
      props: { account: makeAccount({ extra: { codebuddy_credits_error: '积分接口返回异常' } }) }
    })
    expect(wrapper.get('[data-test="codebuddy-credits-error"]').text()).toBe('积分接口返回异常')
  })

  it('shows task results and the task center entry for personal WorkBuddy accounts', () => {
    const wrapper = mount(CodeBuddyCreditsCell, {
      props: {
        account: makeAccount({
          extra: {
            codebuddy_nickname: '小明',
            codebuddy_task_streak: { at: '2026-10-09T01:00:00Z', ok: true, message: '连登 8 天' },
            codebuddy_task_travel: { at: '2026-10-09T01:00:00Z', ok: false, message: '派出失败' }
          }
        })
      },
      global: { stubs: { CodeBuddyTaskCenterModal: true } }
    })
    expect(wrapper.get('[data-test="codebuddy-nickname"]').text()).toBe('小明')
    const results = wrapper.get('[data-test="codebuddy-task-results"]').text()
    expect(results).toContain('admin.workbuddySettings.tasks.names.streak ✓')
    expect(results).toContain('admin.workbuddySettings.tasks.names.travel ✗')
    expect(wrapper.find('[data-test="codebuddy-task-center-open"]').exists()).toBe(true)
  })

  it('hides the task center for enterprise accounts', () => {
    const wrapper = mount(CodeBuddyCreditsCell, {
      props: {
        account: makeAccount({ credentials: { product: 'workbuddy', region: 'china', enterprise_id: 'e-1' } })
      }
    })
    expect(wrapper.find('[data-test="codebuddy-task-center-open"]').exists()).toBe(false)
  })
})
