export default {
  modelCapabilities: {
    title: 'Model capability overrides',
    description:
      'Declare context window, output limit and input modalities by hand. Intended for upstreams that expose no capability metadata and match no public registry (typically Tencent CodeBuddy).',
    advertiseOnlyHint:
      'These declarations only affect what the gateway advertises (/v1/models and the Codex manifest); they do not change request forwarding. Leaving a field empty means "not declared" and the gateway keeps its own fallback.',
    empty: 'No override entries yet.',
    platform: 'Platform',
    allPlatforms: 'All platforms',
    modelId: 'Model ID',
    modelIdPlaceholder: 'e.g. hy3',
    contextWindow: 'Context window',
    maxOutputTokens: 'Max output tokens',
    unset: 'Not declared',
    inputModalities: 'Input modalities',
    addEntry: 'Add entry',
    addCodeBuddyPreset: 'Fill CodeBuddy models',
    loadFailed: 'Failed to load model capabilities: {message}',
    saveSuccess: 'Model capability overrides saved',
    saveFailed: 'Failed to save model capability overrides: {message}',
    modalities: {
      text: 'Text',
      image: 'Image',
      audio: 'Audio',
      video: 'Video',
    },
  },
}
