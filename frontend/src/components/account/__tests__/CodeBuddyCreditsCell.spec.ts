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
})
