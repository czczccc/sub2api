export default {
  modelCapabilities: {
    title: '模型能力覆盖',
    description:
      '手工声明模型的上下文窗口、输出上限与输入模态。用于上游不返回能力字段、公共注册表也匹配不到的平台（典型是腾讯 CodeBuddy）。',
    advertiseOnlyHint:
      '这些声明只影响对外广告（/v1/models 与 Codex manifest），不改变实际转发行为；留空表示不声明，网关会沿用自身的兜底值。',
    empty: '还没有任何覆盖条目。',
    platform: '平台',
    allPlatforms: '全部平台',
    modelId: '模型 ID',
    modelIdPlaceholder: '例如 hy3',
    contextWindow: '上下文窗口',
    maxOutputTokens: '最大输出 tokens',
    unset: '不声明',
    inputModalities: '输入模态',
    addEntry: '添加条目',
    addCodeBuddyPreset: '补全 CodeBuddy 模型',
    loadFailed: '加载模型能力配置失败：{message}',
    saveSuccess: '模型能力配置已保存',
    saveFailed: '保存模型能力配置失败：{message}',
    modalities: {
      text: '文本',
      image: '图片',
      audio: '音频',
      video: '视频',
    },
  },
}
