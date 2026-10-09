import { apiClient } from '../client'

/**
 * 模型能力覆盖条目。
 *
 * 用途：部分上游（腾讯 CodeBuddy 的私有目录接口、以及公共注册表匹配不到的
 * 国产模型）不返回上下文窗口、输出上限、输入模态等能力字段，客户端因此看不到
 * 真实能力（最典型的是"上游明明支持图片，客户端却不发图片"）。这里由管理员
 * 手工声明，网关只在对外广告时使用，不改变实际转发行为。
 */
export interface ModelCapabilityEntry {
  /** 空字符串表示对所有平台生效（仅按模型 ID 匹配）。 */
  platform: string
  model_id: string
  /** 0 表示未声明：网关会沿用自身的兜底值，而不是把 0 当作真实上限。 */
  context_window?: number
  max_output_tokens?: number
  input_modalities?: string[]
}

export interface ModelCapabilityConfig {
  models: ModelCapabilityEntry[]
}

export async function getModelCapabilities(): Promise<ModelCapabilityConfig> {
  const { data } = await apiClient.get<ModelCapabilityConfig>('/admin/settings/model-capabilities')
  return data
}

export async function updateModelCapabilities(
  config: ModelCapabilityConfig
): Promise<ModelCapabilityConfig> {
  const { data } = await apiClient.put<ModelCapabilityConfig>(
    '/admin/settings/model-capabilities',
    config
  )
  return data
}

/** 输入模态白名单，与后端 modelCapabilityInputModalities 保持一致。 */
export const MODEL_CAPABILITY_MODALITIES = ['text', 'image', 'audio', 'video'] as const

/** CodeBuddy / WorkBuddy 模型生效参数（未叠加后台覆盖）。 */
export interface CodeBuddyModelCapabilityRow {
  model_id: string
  context_window?: number
  max_output_tokens?: number
  supports_images?: boolean
  /** upstream=上游快照，builtin=内置参数表，default=平台默认 */
  source: 'upstream' | 'builtin' | 'default'
}

export interface CodeBuddyModelCapabilityReport {
  models: CodeBuddyModelCapabilityRow[]
  synced_at?: string
}

export async function getCodeBuddyModelCapabilities(): Promise<CodeBuddyModelCapabilityReport> {
  const { data } = await apiClient.get<CodeBuddyModelCapabilityReport>(
    '/admin/accounts/codebuddy/model-capabilities'
  )
  return data
}

export async function refreshCodeBuddyModelCapabilities(): Promise<{
  refreshed: number
  failed: number
  report: CodeBuddyModelCapabilityReport
}> {
  const { data } = await apiClient.post<{
    refreshed: number
    failed: number
    report: CodeBuddyModelCapabilityReport
  }>('/admin/accounts/codebuddy/model-capabilities/refresh')
  return data
}
