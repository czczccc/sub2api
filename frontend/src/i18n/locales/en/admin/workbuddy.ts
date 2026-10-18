export default {
  workbuddyCredits: {
    action: 'Credits',
    title: 'WorkBuddy credits summary',
    reload: 'Reload',
    remain: 'Remaining credits',
    ofTotal: 'of {total}',
    tokens: 'Approx. tokens (DeepSeek Flash)',
    tokenSplit: 'Input {input} / output {output}',
    noMultiplier: 'DeepSeek Flash multiplier missing; enter one or sync models first',
    officialValue: 'Value at DeepSeek list price (off-peak)',
    peakValue: 'Peak hours approx. ¥{cost}',
    multiplier: 'DeepSeek Flash credit multiplier',
    multiplierFromUpstream: 'From upstream model catalog ({model})',
    multiplierFromSettings: 'From WorkBuddy settings',
    multiplierManual: 'Edited here, not saved',
    multiplierMissing: 'Not found in the upstream catalog; set it in Settings → WorkBuddy',
    tokensPerCredit: 'Tokens per credit at 1x',
    tokensPerCreditHint: 'Not official; community estimate by default',
    outputPercent: 'Output token share (%)',
    outputPercentHint: 'Used to price the tokens',
    formula:
      'Formula: remaining credits ÷ multiplier × tokens per credit = tokens. Value uses DeepSeek deepseek-flash cache-miss list prices: off-peak input ¥{idleInput} / output ¥{idleOutput}, peak input ¥{peakInput} / output ¥{peakOutput} per million tokens. Estimate only.',
    pricingLink: 'DeepSeek pricing',
    coverage: '{reported} of {count} accounts have credit data.',
    unsupported: '{count} international accounts cannot be queried.',
    errors: '{count} accounts failed their last query; their previous values are used.',
    oldestChecked: 'Oldest data checked at {time}.',
    earliestExpire: 'Earliest credits expire at {time}.',
    columns: {
      account: 'Account',
      remain: 'Remaining / total',
      expire: 'Earliest expiry',
      checked: 'Checked at'
    },
    empty: 'No WorkBuddy accounts in this group',
    queryFailed: 'Query failed',
    notSupported: 'N/A',
    loadFailed: 'Failed to load credits summary: {message}'
  },
  workbuddySettings: {
    title: 'WorkBuddy gateway',
    description: 'Applies only to outbound requests of WorkBuddy (Tencent CodeBuddy platform) accounts.',
    loadFailed: 'Failed to load WorkBuddy settings: {message}',
    saveSuccess: 'WorkBuddy settings saved',
    saveFailed: 'Failed to save WorkBuddy settings: {message}',
    sanitize: {
      title: 'Fingerprint sanitizing',
      description:
        'Upstream blocks the fixed template sentences injected by Claude Code / Codex verbatim (error 11128). When enabled, billing headers and cc_* key-values are removed and those sentences get a one-word change before sending; meaning is unchanged.'
    },
    prompt: {
      title: 'System prompt',
      mode: 'Mode',
      modes: {
        degrade: 'Pass through, degrade when blocked (recommended)',
        passthrough: 'Pass through',
        append: 'Append gateway prompt after the client prompt',
        custom: 'Replace the client prompt with the gateway prompt'
      },
      modeHints: {
        degrade: 'Forwards the client system prompt as is; once blocked by content review, switches to a neutral prompt until midnight (Beijing time) and retries immediately.',
        passthrough: 'Forwards as is; a block is returned to the client.',
        append: 'Keeps the client system prompt and adds the gateway prompt after it; degrades the same way when blocked.',
        custom: 'Drops all client system messages and sends only the gateway prompt.'
      },
      text: 'Gateway prompt',
      textHint: 'Leave empty to use the built-in default prompt.',
      useDefault: 'Fill in the built-in default'
    },
    tasks: {
      title: 'Daily keep-alive tasks',
      description: 'Applies to mainland China personal accounts only, runs on the hour (Beijing time); the latest result per account shows in the account list.',
      enabled: 'Enabled',
      hours: 'Hours',
      hoursHint: 'Hours 0–23, comma separated',
      runNow: 'Run now',
      runStarted: 'Started in the background; check the account list for results',
      runFailed: 'Run failed: {message}',
      names: {
        activity: 'Activity report',
        streak: 'Streak manager',
        travel: 'Cat travel',
        nickname: 'Nickname sync',
        growth: 'Growth tasks',
        blackcat: 'Night owl',
        balance: 'Balance refresh'
      },
      hints: {
        activity: 'Accounts that already chatted today report activity directly; others send one real short chat first, then report.',
        streak: "Uses a makeup card for a missed yesterday, claims newbie gift and compensation, redeems unlocked streak tiers, and uses all lottery draws.",
        travel: 'Adopts a cat when there is none (after a chat that day), claims rewards on return, and sends the cat travelling when idle.',
        nickname: 'Reads the account nickname from the website and shows it in the account list.',
        growth: 'Accepts all tasks, completes chat tasks with real chats (5 chats, GLM-5.2 chat, first cat), and claims every reward that is ready. Other tasks must be done in the official client; their rewards are claimed automatically afterwards.',
        blackcat: 'Between 23:00 and 08:00 Beijing time, tops up the night-owl task with real glm-5.2 chats and claims the reward; set the hours inside that window.',
        balance: 'Refreshes remaining credits periodically; lifts the credits-exhausted pause once credits are back.'
      },
      balanceMinutes: 'Interval (minutes)'
    },
    credits: {
      title: 'Credit conversion',
      description: 'Parameters used by the group "Credits summary" to convert remaining credits into tokens and DeepSeek list prices. Display only.',
      tokensPerCredit: 'Tokens per credit at 1x',
      tokensPerCreditHint: 'Not published officially; the default 31874 is a community estimate. Calibrate it against your own usage details.',
      multiplier: 'DeepSeek Flash credit multiplier',
      multiplierHint: '0 uses the multiplier synced from the upstream model catalog; a positive value is used as-is.',
      outputPercent: 'Output token share (%)',
      outputPercentHint: 'Share of output tokens assumed when pricing the tokens at DeepSeek list prices.'
    },
    taskCenter: {
      open: 'Growth tasks',
      title: 'Growth tasks · {name}',
      empty: 'No tasks',
      progress: 'Progress {current}/{target}',
      reward: '+{credit} credits',
      rewardEnergy: '+{credit} credits +{energy} energy',
      claimed: 'Claimed',
      locked: 'Locked',
      manual: 'Complete in the official client',
      run: 'Complete',
      claim: 'Claim',
      running: 'Running…',
      loadFailed: 'Failed to load tasks: {message}',
      actionFailed: 'Action failed: {message}',
      refresh: 'Refresh'
    }
  }
}
