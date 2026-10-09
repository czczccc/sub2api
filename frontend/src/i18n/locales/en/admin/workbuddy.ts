export default {
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
    }
  }
}
