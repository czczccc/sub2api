import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

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
  domain: 'www.codebuddy.cn'
}

function mountFlow() {
  return mount(CodeBuddyAuthFlow, { props: { mode: 'oauth' } })
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
      auth_url: 'https://copilot.tencent.com/login?platform=CLI&state=st-1'
    })
    pollMock.mockResolvedValue({ status: 'ready', nickname: '测试号', credentials: READY_CREDENTIALS })

    const wrapper = mountFlow()
    await wrapper.find('[data-testid="codebuddy-generate-link"]').trigger('click')
    await flushPromises()

    expect(startMock).toHaveBeenCalledTimes(1)
    const input = wrapper.find('[data-testid="codebuddy-auth-url"]').element as HTMLInputElement
    expect(input.value).toBe('https://copilot.tencent.com/login?platform=CLI&state=st-1')

    // 生成链接后自动开始轮询，无需再点按钮。
    expect(pollMock).toHaveBeenCalledWith('st-1')
    expect(wrapper.emitted('authorized')).toEqual([[READY_CREDENTIALS, '测试号']])
    expect(wrapper.find('[data-testid="codebuddy-auth-success"]').exists()).toBe(true)
    wrapper.unmount()
  })

  it('轮询 pending 时不报错、不发射凭据，只展示等待提示', async () => {
    startMock.mockResolvedValue({ state: 'st-1', auth_url: 'https://copilot.tencent.com/login?state=st-1' })
    pollMock.mockResolvedValue({ status: 'pending' })

    const wrapper = mountFlow()
    await wrapper.find('[data-testid="codebuddy-generate-link"]').trigger('click')
    await flushPromises()

    expect(wrapper.emitted('authorized')).toBeUndefined()
    expect(wrapper.find('[data-testid="codebuddy-auth-error"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('生成链接失败时展示错误且不进入轮询', async () => {
    startMock.mockRejectedValue(new Error('upstream down'))

    const wrapper = mountFlow()
    await wrapper.find('[data-testid="codebuddy-generate-link"]').trigger('click')
    await flushPromises()

    expect(pollMock).not.toHaveBeenCalled()
    expect(wrapper.find('[data-testid="codebuddy-auth-error"]').text()).toContain('upstream down')
    wrapper.unmount()
  })

  it('切到手动填写时不发起任何上游请求，只把模式变更抛给父组件', async () => {
    const wrapper = mountFlow()
    await wrapper.find('input[type="radio"][value="manual"]').setValue()

    // mode 是 defineModel：子组件只负责 emit，由父组件持有状态。
    expect(wrapper.emitted('update:mode')).toEqual([['manual']])
    expect(startMock).not.toHaveBeenCalled()
    expect(pollMock).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})
