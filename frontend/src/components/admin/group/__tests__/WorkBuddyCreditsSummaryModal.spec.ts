import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { ref } from 'vue'
import type { AdminGroup } from '@/types'
import WorkBuddyCreditsSummaryModal from '../WorkBuddyCreditsSummaryModal.vue'

const mocks = vi.hoisted(() => ({ getWorkBuddyCreditsSummary: vi.fn(), getWorkBuddyConfig: vi.fn() }))
vi.mock('@/api/admin/workbuddy', () => mocks)
vi.mock('@/utils/format', () => ({ formatDateTimeToMinute: (value: string) => value }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: vi.fn() }) }))
vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key} ${JSON.stringify(params)}` : key), locale: ref('en') })
}))
enableAutoUnmount(afterEach)

const summary = {
  account_count: 2,
  reported_count: 2,
  unsupported_count: 0,
  error_count: 0,
  remain: 1000,
  total: 2000,
  used: 1000,
  deepseek_flash: { model_id: 'deepseek-v4.1-flash', multiplier: 0.5 },
  accounts: [{ id: 1, name: 'a', status: 'active', supported: true, remain: 1000, total: 2000 }]
}

function config(multiplier = 0) {
  return { config: { credit_tokens_per_credit: 10000, deepseek_flash_multiplier: multiplier, credit_output_percent: 20 }, default_prompt: '' }
}

async function open() {
  const wrapper = mount(WorkBuddyCreditsSummaryModal, {
    props: { show: false, group: { id: 9, name: 'WB', platform: 'codebuddy' } as AdminGroup },
    global: { stubs: { BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /></div>' } } }
  })
  await wrapper.setProps({ show: true })
  await flushPromises()
  return wrapper
}

beforeEach(() => {
  vi.clearAllMocks()
  mocks.getWorkBuddyCreditsSummary.mockResolvedValue(summary)
})

describe('WorkBuddyCreditsSummaryModal', () => {
  it('用上游倍率折算 tokens 和 DeepSeek 官方价', async () => {
    mocks.getWorkBuddyConfig.mockResolvedValue(config())
    const wrapper = await open()
    expect(mocks.getWorkBuddyCreditsSummary).toHaveBeenCalledWith(9)
    // 1000 ÷ 0.5 × 10000 = 2000 万 tokens；闲时 16M×1 + 4M×4 = ¥32。
    expect(wrapper.get('[data-test="credits-tokens"]').text()).toContain('20M')
    expect(wrapper.get('[data-test="credits-cost"]').text()).toContain('¥32.00')
    expect(wrapper.text()).toContain('multiplierFromUpstream')
  })

  it('设置里的倍率优先于上游', async () => {
    mocks.getWorkBuddyConfig.mockResolvedValue(config(0.25))
    const wrapper = await open()
    expect(wrapper.get('[data-test="credits-tokens"]').text()).toContain('40M')
    expect(wrapper.text()).toContain('multiplierFromSettings')
  })

  it('没有倍率时不给估算', async () => {
    mocks.getWorkBuddyConfig.mockResolvedValue(config())
    mocks.getWorkBuddyCreditsSummary.mockResolvedValue({ ...summary, deepseek_flash: undefined })
    const wrapper = await open()
    expect(wrapper.get('[data-test="credits-tokens"]').text()).toBe('—')
    expect(wrapper.text()).toContain('noMultiplier')
  })
})
