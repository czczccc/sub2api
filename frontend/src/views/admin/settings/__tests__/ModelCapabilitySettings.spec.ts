import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import ModelCapabilitySettings from '../ModelCapabilitySettings.vue'

const messages: Record<string, string> = {
  'admin.modelCapabilities.title': 'Model capability overrides',
  'admin.modelCapabilities.addEntry': 'Add entry',
  'admin.modelCapabilities.addCodeBuddyPreset': 'Fill CodeBuddy models',
  'admin.modelCapabilities.modelId': 'Model ID',
  'admin.modelCapabilities.platform': 'Platform',
  'admin.modelCapabilities.allPlatforms': 'All platforms',
  'admin.modelCapabilities.contextWindow': 'Context window',
  'admin.modelCapabilities.maxOutputTokens': 'Max output tokens',
  'admin.modelCapabilities.inputModalities': 'Input modalities',
  'common.save': 'Save',
  'common.delete': 'Delete',
  'common.loading': 'Loading',
}

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => messages[key] ?? key,
    }),
  }
})

const { showSuccess, showError } = vi.hoisted(() => ({
  showSuccess: vi.fn(),
  showError: vi.fn(),
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showSuccess, showError }),
}))

const { getModelCapabilities, updateModelCapabilities } = vi.hoisted(() => ({
  getModelCapabilities: vi.fn(),
  updateModelCapabilities: vi.fn(),
}))

vi.mock('@/api/admin/modelCapabilities', async () => {
  const actual = await vi.importActual<typeof import('@/api/admin/modelCapabilities')>(
    '@/api/admin/modelCapabilities'
  )
  return { ...actual, getModelCapabilities, updateModelCapabilities }
})

const mountEditor = () => mount(ModelCapabilitySettings)

describe('ModelCapabilitySettings', () => {
  beforeEach(() => {
    showSuccess.mockReset()
    showError.mockReset()
    getModelCapabilities.mockReset().mockResolvedValue({ models: [] })
    updateModelCapabilities.mockReset()
  })

  it('保存前把条目归一化为后端契约', async () => {
    updateModelCapabilities.mockResolvedValue({ models: [] })
    const wrapper = mountEditor()
    await flushPromises()

    await wrapper.get('button.btn-secondary').trigger('click')
    const inputs = wrapper.findAll('input[type="text"]')
    await inputs[0]!.setValue('hy3')
    const numberInputs = wrapper.findAll('input[type="number"]')
    await numberInputs[0]!.setValue('200000')

    const saveButton = wrapper
      .findAll('button')
      .find(button => button.text() === 'Save')
    await saveButton!.trigger('click')
    await flushPromises()

    expect(updateModelCapabilities).toHaveBeenCalledWith({
      models: [
        {
          platform: '',
          model_id: 'hy3',
          input_modalities: ['text'],
          context_window: 200000,
        },
      ],
    })
    expect(showSuccess).toHaveBeenCalled()
  })

  // 客户端"上游支持图片却发不出图片"的根因是能力声明，所以预置必须带上 image。
  it('CodeBuddy 预置为 15 个模型声明 text+image', async () => {
    const wrapper = mountEditor()
    await flushPromises()

    const presetButton = wrapper
      .findAll('button')
      .find(button => button.text() === 'Fill CodeBuddy models')
    await presetButton!.trigger('click')
    await flushPromises()

    const modelIDInputs = wrapper.findAll<HTMLInputElement>('input[type="text"]')
    expect(modelIDInputs.length).toBe(15)

    const saveButton = wrapper
      .findAll('button')
      .find(button => button.text() === 'Save')
    await saveButton!.trigger('click')
    await flushPromises()

    const saved = updateModelCapabilities.mock.calls[0]?.[0]
    expect(saved.models).toHaveLength(15)
    for (const entry of saved.models) {
      expect(entry.platform).toBe('codebuddy')
      expect(entry.input_modalities).toEqual(['text', 'image'])
    }
  })

  it('加载失败时提示错误且不崩溃', async () => {
    getModelCapabilities.mockRejectedValue(new Error('boom'))
    const wrapper = mountEditor()
    await flushPromises()

    expect(showError).toHaveBeenCalled()
    expect(wrapper.text()).toContain('admin.modelCapabilities.empty')
  })
})
