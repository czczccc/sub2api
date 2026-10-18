export default {
  workbuddyCredits: {
    action: '积分汇总',
    title: 'WorkBuddy 积分汇总',
    reload: '重新加载',
    remain: '剩余积分',
    ofTotal: '共 {total}',
    tokens: '约可用 tokens（DeepSeek Flash）',
    tokenSplit: '输入 {input} / 输出 {output}',
    noMultiplier: '缺少 DeepSeek Flash 倍率，请填写倍率或先同步模型',
    officialValue: '按 DeepSeek 官方价约值（闲时）',
    peakValue: '高峰时段约 ¥{cost}',
    multiplier: 'DeepSeek Flash 积分倍率',
    multiplierFromUpstream: '来自上游模型目录（{model}）',
    multiplierFromSettings: '来自 WorkBuddy 设置',
    multiplierManual: '临时修改，未保存',
    multiplierMissing: '上游目录里没读到倍率，可在 设置 → WorkBuddy 填写',
    tokensPerCredit: '1 倍率下每积分 ≈ tokens',
    tokensPerCreditHint: '官方未公布，默认取社区估值',
    outputPercent: '输出 token 占比（%）',
    outputPercentHint: '用于按官方价折算金额',
    formula:
      '算法：剩余积分 ÷ 倍率 × 每积分 tokens = 可用 tokens；金额按 DeepSeek 官方 deepseek-flash 缓存未命中价计算，闲时输入 ¥{idleInput} / 输出 ¥{idleOutput}，高峰输入 ¥{peakInput} / 输出 ¥{peakOutput}（每百万 tokens）。结果仅供参考。',
    pricingLink: 'DeepSeek 官方价格',
    coverage: '{count} 个账号中 {reported} 个有积分数据。',
    unsupported: '{count} 个国际版账号不支持查询。',
    errors: '{count} 个账号最近一次查询失败，按上次结果计入。',
    oldestChecked: '最旧数据查询于 {time}。',
    earliestExpire: '最早一批积分 {time} 到期。',
    columns: {
      account: '账号',
      remain: '剩余 / 总量',
      expire: '最早到期',
      checked: '查询时间'
    },
    empty: '这个分组里没有 WorkBuddy 账号',
    queryFailed: '查询失败',
    notSupported: '不支持',
    loadFailed: '加载积分汇总失败：{message}'
  },
  workbuddySettings: {
    title: 'WorkBuddy 网关设置',
    description: '只作用于 WorkBuddy（腾讯 CodeBuddy 平台）账号的出站请求。',
    loadFailed: '加载 WorkBuddy 设置失败：{message}',
    saveSuccess: 'WorkBuddy 设置已保存',
    saveFailed: '保存 WorkBuddy 设置失败：{message}',
    sanitize: {
      title: '指纹脱敏',
      description:
        '上游会逐字拦截 Claude Code / Codex 注入的固定模板句（错误码 11128）。开启后出站前删掉计费头、cc_* 键值，并把这些模板句改一个词，语义不变。'
    },
    prompt: {
      title: '系统提示词',
      mode: '模式',
      modes: {
        degrade: '透传，被拦截时降级（推荐）',
        passthrough: '原样透传',
        append: '在客户端提示词后追加网关提示词',
        custom: '用网关提示词替换客户端提示词'
      },
      modeHints: {
        degrade: '原样转发客户端的 system 提示词；一旦被内容审核拦截，当天（到北京时间 0 点）改用一句中性提示词并立即重试。',
        passthrough: '原样转发，被拦截时直接把错误返回给客户端。',
        append: '保留客户端的 system 提示词，在其后加一条网关提示词；被拦截时同样当天降级。',
        custom: '删除客户端的全部 system 提示词，只发送网关提示词。'
      },
      text: '网关提示词',
      textHint: '留空使用内置默认提示词。',
      useDefault: '填入内置默认提示词'
    },
    tasks: {
      title: '日常保号任务',
      description: '只对中国大陆个人账号生效，按北京时间整点执行；每个账号的最近结果显示在账号列表里。',
      enabled: '启用',
      hours: '执行时点',
      hoursHint: '0–23 的整点，用逗号分隔',
      runNow: '立即执行',
      runStarted: '已在后台开始执行，稍后在账号列表查看结果',
      runFailed: '执行失败：{message}',
      names: {
        activity: '活跃上报',
        streak: '连登管家',
        travel: '猫猫旅行',
        nickname: '昵称同步',
        growth: '成长任务',
        blackcat: '夜猫子',
        balance: '余额刷新'
      },
      hints: {
        activity: '今天已有对话的账号直接上报活跃；还没对话的先发一次真实短对话再上报。',
        streak: '漏签时用补签卡补昨天，领新手礼包和补偿，兑换已解锁的连登档位，抽完抽奖次数。',
        travel: '没有猫时（当天有对话后）领养，旅行归来领积分，空闲时派出旅行。',
        nickname: '从官网读取账号昵称，显示在账号列表。',
        growth: '报名全部任务，用真实对话完成对话类任务（对话 5 次、GLM-5.2 对话、领养第一只猫），并领取所有已达标的奖励。其它任务需在官方客户端里完成，完成后会被自动领奖。',
        blackcat: '北京时间 23:00–08:00 内用 glm-5.2 真实对话补足夜猫子任务次数并领奖；时点请设在这个窗口内。',
        balance: '定时刷新剩余积分；积分恢复后自动解除“积分耗尽”暂停。'
      },
      balanceMinutes: '刷新间隔（分钟）'
    },
    credits: {
      title: '积分折算',
      description: '分组「积分汇总」里把剩余积分折算成 tokens 和 DeepSeek 官方价时使用的参数，只影响展示。',
      tokensPerCredit: '1 倍率下每积分 ≈ tokens',
      tokensPerCreditHint: '官方未公布换算，默认 31874 是社区实测估值，可按自己的用量明细校准。',
      multiplier: 'DeepSeek Flash 积分倍率',
      multiplierHint: '填 0 使用上游模型目录里同步到的倍率；填正数则固定使用该值。',
      outputPercent: '输出 token 占比（%）',
      outputPercentHint: '按 DeepSeek 官方价折算金额时，假设可用 tokens 中输出所占的比例。'
    },
    taskCenter: {
      open: '成长任务',
      title: '成长任务 · {name}',
      empty: '没有任务',
      progress: '进度 {current}/{target}',
      reward: '+{credit} 积分',
      rewardEnergy: '+{credit} 积分 +{energy} 能量',
      claimed: '已领取',
      locked: '未解锁',
      manual: '需在官方客户端完成',
      run: '自动完成',
      claim: '领取',
      running: '执行中…',
      loadFailed: '加载任务失败：{message}',
      actionFailed: '操作失败：{message}',
      refresh: '刷新'
    }
  }
}
