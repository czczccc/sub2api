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
  activity_task: WorkBuddyTaskSchedule
  streak_task: WorkBuddyTaskSchedule
  travel_task: WorkBuddyTaskSchedule
  nickname_task: WorkBuddyTaskSchedule
  /** true 时关闭余额定时刷新。 */
  balance_refresh_disabled: boolean
  balance_refresh_minutes: number
}

/** 日常保号任务的开关与执行时点（北京时间整点）。 */
export interface WorkBuddyTaskSchedule {
  disabled: boolean
  hours: number[]
}

/** 日常保号任务名，与后端 WorkBuddyTask* 常量一致。 */
export const WORKBUDDY_SCHEDULED_TASKS = ['activity', 'streak', 'travel', 'nickname'] as const
export type WorkBuddyScheduledTask = (typeof WORKBUDDY_SCHEDULED_TASKS)[number]
export type WorkBuddyTask = WorkBuddyScheduledTask | 'balance'

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

/** 立即在后台执行一次日常任务，结果写入各账号后在账号列表查看。 */
export async function runWorkBuddyTask(task: WorkBuddyTask): Promise<void> {
  await apiClient.post(`/admin/accounts/codebuddy/tasks/${task}/run`)
}
