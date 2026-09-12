import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent, ref, h } from 'vue'

const { startMock, pollMock } = vi.hoisted(() => ({
  startMock: vi.fn(),
  pollMock: vi.fn()
}))

vi.mock('@/api/admin/accounts', () => ({
  startCodeBuddyAuth: startMock,
  pollCodeBuddyAuth: pollMock
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

import CodeBuddyAuthFlow from '../CodeBuddyAuthFlow.vue'

const READY_CREDENTIALS = {
  access_token: 'at-1',
  refresh_token: 'rt-1',
  uid: 'u-1',
  enterprise_id: 'e-1',
  domain: 'www.workbuddy.ai',
  product: 'workbuddy',
  region: 'global'
}

/** 默认站点：大陆 CodeBuddy。 */
const DEFAULT_SITE = { product: 'codebuddy', region: 'china' }
/** 国际版 WorkBuddy。 */
const WORKBUDDY_INTL_SITE = { product: 'workbuddy', region: 'global' }

/**
 * 用真实的 v-model 宿主挂载：site-key / mode 都是 defineModel，
 * 子组件只 emit，必须由父组件把新值同步回来（否则会回弹到旧值）。
 */
function mountFlow(initialSiteKey = 'codebuddy/china') {
  const Host = defineComponent({
    setup(_, { expose }) {
      const siteKey = ref(initialSiteKey)
      const mode = ref<'oauth' | 'manual'>('oauth')
      expose({ siteKey, mode })
      return () =>
        h(CodeBuddyAuthFlow, {
          mode: mode.value,
          'onUpdate:mode': (value: 'oauth' | 'manual') => (mode.value = value),
          siteKey: siteKey.value,
          'onUpdate:siteKey': (value: string) => (siteKey.value = value)
        })
    }
  })
  const wrapper = mount(Host)
  return { wrapper, flow: () => wrapper.findComponent(CodeBuddyAuthFlow) }
}

describe('CodeBuddyAuthFlow', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    startMock.mockReset()
    pollMock.mockReset()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('生成链接后展示授权地址，并在轮询到 ready 时把凭据交给父组件', async () => {
    startMock.mockResolvedValue({
      state: 'st-1',
      auth_url: 'https://www.workbuddy.ai/login?platform=CLI&state=st-1'
    })
    pollMock.mockResolvedValue({ status: 'ready', nickname: '测试号', credentials: READY_CREDENTIALS })

    const { wrapper, flow } = mountFlow('workbuddy/global')
    await wrapper.find('[data-testid="codebuddy-generate-link"]').trigger('click')
    await flushPromises()

    expect(startMock).toHaveBeenCalledTimes(1)
    // 站点维度必须随请求下发，否则授权链接会落到默认的大陆站。
    expect(startMock).toHaveBeenCalledWith(WORKBUDDY_INTL_SITE)
    const input = wrapper.find('[data-testid="codebuddy-auth-url"]').element as HTMLInputElement
    expect(input.value).toBe('https://www.workbuddy.ai/login?platform=CLI&state=st-1')

    // 生成链接后自动开始轮询，无需再点按钮；轮询必须带同一组站点取值。
    expect(pollMock).toHaveBeenCalledWith('st-1', WORKBUDDY_INTL_SITE)
    expect(flow().emitted('authorized')).toEqual([[READY_CREDENTIALS, '测试号']])
    expect(wrapper.find('[data-testid="codebuddy-auth-success"]').exists()).toBe(true)
    wrapper.unmount()
  })

  it('默认站点为大陆 CodeBuddy，并把它随授权请求下发', async () => {
    startMock.mockResolvedValue({ state: 'st-1', auth_url: 'https://copilot.tencent.com/login?state=st-1' })
    pollMock.mockResolvedValue({ status: 'pending' })

    const { wrapper } = mountFlow()
    await wrapper.find('[data-testid="codebuddy-generate-link"]').trigger('click')
    await flushPromises()

    expect(startMock).toHaveBeenCalledWith(DEFAULT_SITE)
    expect(pollMock).toHaveBeenCalledWith('st-1', DEFAULT_SITE)
    wrapper.unmount()
  })

  it('切换站点时作废已生成的授权链接，并让后续请求打到新站点', async () => {
    startMock.mockResolvedValue({ state: 'st-1', auth_url: 'https://copilot.tencent.com/login?state=st-1' })
    pollMock.mockResolvedValue({ status: 'pending' })

    const { wrapper, flow } = mountFlow()
    await wrapper.find('[data-testid="codebuddy-generate-link"]').trigger('click')
    await flushPromises()

    const input = wrapper.find('[data-testid="codebuddy-auth-url"]').element as HTMLInputElement
    expect(input.value).not.toBe('')

    // 切到国际版 WorkBuddy：旧的 state 属于另一个站点，必须清空。
    await wrapper.find('[data-testid="codebuddy-site-workbuddy/global"]').trigger('click')
    await flushPromises()

    expect(flow().emitted('update:siteKey')).toEqual([['workbuddy/global']])
    // 链接被清空后输入框整体消失（v-if），即不再展示上一个站点的链接。
    expect(wrapper.find('[data-testid="codebuddy-auth-url"]').exists()).toBe(false)

    // 重新生成必须打到新站点。
    startMock.mockResolvedValue({ state: 'st-2', auth_url: 'https://www.workbuddy.ai/login?state=st-2' })
    await wrapper.find('[data-testid="codebuddy-generate-link"]').trigger('click')
    await flushPromises()
    expect(startMock).toHaveBeenLastCalledWith(WORKBUDDY_INTL_SITE)
    wrapper.unmount()
  })

  it('轮询 pending 时不报错、不发射凭据，只展示等待提示', async () => {
    startMock.mockResolvedValue({ state: 'st-1', auth_url: 'https://copilot.tencent.com/login?state=st-1' })
    pollMock.mockResolvedValue({ status: 'pending' })

    const { wrapper, flow } = mountFlow()
    await wrapper.find('[data-testid="codebuddy-generate-link"]').trigger('click')
    await flushPromises()

    expect(flow().emitted('authorized')).toBeUndefined()
    expect(wrapper.find('[data-testid="codebuddy-auth-error"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('生成链接失败时展示错误且不进入轮询', async () => {
    startMock.mockRejectedValue(new Error('upstream down'))

    const { wrapper } = mountFlow()
    await wrapper.find('[data-testid="codebuddy-generate-link"]').trigger('click')
    await flushPromises()

    expect(pollMock).not.toHaveBeenCalled()
    expect(wrapper.find('[data-testid="codebuddy-auth-error"]').text()).toContain('upstream down')
    wrapper.unmount()
  })

  it('切到手动填写时不发起任何上游请求，只把模式变更抛给父组件', async () => {
    const { wrapper, flow } = mountFlow()
    await wrapper.find('input[type="radio"][value="manual"]').setValue()

    // mode 是 defineModel：子组件只负责 emit，由父组件持有状态。
    expect(flow().emitted('update:mode')).toEqual([['manual']])
    expect(startMock).not.toHaveBeenCalled()
    expect(pollMock).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})
