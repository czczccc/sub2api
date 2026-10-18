import { describe, expect, it } from 'vitest'
import { estimateCreditTokens, formatTokenCount } from '../workbuddyCredits'

describe('estimateCreditTokens', () => {
  it('按倍率和每积分 tokens 折算，并按官方价算等价金额', () => {
    const estimate = estimateCreditTokens(1000, 0.5, 10_000, 20)!
    expect(estimate.tokens).toBe(20_000_000)
    expect(estimate.outputTokens).toBe(4_000_000)
    expect(estimate.inputTokens).toBe(16_000_000)
    // 闲时：16M × 1 + 4M × 4 = 32 元；高峰翻倍。
    expect(estimate.costIdle).toBeCloseTo(32)
    expect(estimate.costPeak).toBeCloseTo(64)
  })

  it('倍率或换算无效时返回 null', () => {
    expect(estimateCreditTokens(1000, 0, 10_000, 20)).toBeNull()
    expect(estimateCreditTokens(1000, 0.5, 0, 20)).toBeNull()
  })

  it('负积分按 0、输出占比截断到 0–100', () => {
    expect(estimateCreditTokens(-5, 1, 100, 20)!.tokens).toBe(0)
    expect(estimateCreditTokens(10, 1, 100, 150)!.inputTokens).toBe(0)
  })
})

describe('formatTokenCount', () => {
  it('使用紧凑写法', () => {
    expect(formatTokenCount(120_000_000, 'en')).toBe('120M')
  })
})
