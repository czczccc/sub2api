/**
 * WorkBuddy 积分 → tokens → DeepSeek 官方价 的折算。
 *
 * 换算链：剩余积分 ÷ DeepSeek Flash 积分倍率 × 每积分 tokens（1 倍率）= 可用 tokens；
 * 再按 DeepSeek 官方 deepseek-flash 单价（元/百万 tokens）和设定的输出占比折算等价金额。
 * WorkBuddy 官方没有公布积分与 token 的换算，"每积分 tokens"是可调的估值，结果仅供参考。
 */

/** DeepSeek 官方 deepseek-flash（V4.1 Flash）价格，元/百万 tokens，2026-09-10 起生效。 */
export const DEEPSEEK_FLASH_PRICE = {
  idle: { cacheHit: 0.02, input: 1, output: 4 },
  peak: { cacheHit: 0.04, input: 2, output: 8 }
} as const

export const DEEPSEEK_PRICING_URL = 'https://api-docs.deepseek.com/zh-cn/quick_start/pricing'

export interface CreditTokenEstimate {
  /** 可用 tokens 总数（输入 + 输出）。 */
  tokens: number
  inputTokens: number
  outputTokens: number
  /** 按官方价折算的等价金额（元），闲时 / 高峰。 */
  costIdle: number
  costPeak: number
}

/**
 * 折算剩余积分；倍率或每积分 tokens 无效时返回 null。
 * outputPercent 按 0–100 截断。
 */
export function estimateCreditTokens(
  credits: number,
  multiplier: number,
  tokensPerCredit: number,
  outputPercent: number
): CreditTokenEstimate | null {
  if (!(multiplier > 0) || !(tokensPerCredit > 0) || !Number.isFinite(credits)) {
    return null
  }
  const tokens = (Math.max(credits, 0) / multiplier) * tokensPerCredit
  const share = Math.min(Math.max(outputPercent, 0), 100) / 100
  const outputTokens = tokens * share
  const inputTokens = tokens - outputTokens
  const cost = (price: { input: number; output: number }) =>
    (inputTokens * price.input + outputTokens * price.output) / 1_000_000
  return {
    tokens,
    inputTokens,
    outputTokens,
    costIdle: cost(DEEPSEEK_FLASH_PRICE.idle),
    costPeak: cost(DEEPSEEK_FLASH_PRICE.peak)
  }
}

/** 把 token 数格式化为紧凑写法（中文 1.2亿 / 英文 120M）。 */
export function formatTokenCount(tokens: number, locale?: string): string {
  return new Intl.NumberFormat(locale, { notation: 'compact', maximumFractionDigits: 2 }).format(tokens)
}
