import { apiClient } from '../client'

/** WorkBuddy 系统提示词模式，与后端 WorkBuddyPromptMode* 常量一致。 */
export const WORKBUDDY_PROMPT_MODES = ['degrade', 'passthrough', 'append', 'custom'] as const
export type WorkBuddyPromptMode = (typeof WORKBUDDY_PROMPT_MODES)[number]

/** WorkBuddy 网关行为配置。 */
export interface WorkBuddyConfig {
  /** true 时关闭出站指纹脱敏（默认开启）。 */
  sanitize_disabled: boolean
  prompt_mode: WorkBuddyPromptMode
  /** append / custom 模式的网关提示词；为空时使用内置默认提示词。 */
  prompt_text: string
}

export interface WorkBuddyConfigResponse {
  config: WorkBuddyConfig
  default_prompt: string
}

export async function getWorkBuddyConfig(): Promise<WorkBuddyConfigResponse> {
  const { data } = await apiClient.get<WorkBuddyConfigResponse>('/admin/settings/workbuddy')
  return data
}

export async function updateWorkBuddyConfig(config: WorkBuddyConfig): Promise<WorkBuddyConfigResponse> {
  const { data } = await apiClient.put<WorkBuddyConfigResponse>('/admin/settings/workbuddy', config)
  return data
}
