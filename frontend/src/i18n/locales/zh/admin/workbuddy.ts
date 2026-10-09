export default {
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
        balance: '余额刷新'
      },
      hints: {
        activity: '今天已有对话的账号直接上报活跃；还没对话的先发一次真实短对话再上报。',
        streak: '漏签时用补签卡补昨天，领新手礼包和补偿，兑换已解锁的连登档位，抽完抽奖次数。',
        travel: '没有猫时（当天有对话后）领养，旅行归来领积分，空闲时派出旅行。',
        nickname: '从官网读取账号昵称，显示在账号列表。',
        balance: '定时刷新剩余积分；积分恢复后自动解除“积分耗尽”暂停。'
      },
      balanceMinutes: '刷新间隔（分钟）'
    }
  }
}
