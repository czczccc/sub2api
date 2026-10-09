//go:build unit

package service

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestSanitizeTencentCodeBuddyText(t *testing.T) {
	cases := map[string]struct{ in, want string }{
		"身份句（CLI）":   {"You are Claude Code, Anthropic's official CLI for Claude.", "You are Claude Code, Anthropic's official CLI tool for Claude."},
		"身份句（SDK）":   {"You are Claude Code, Anthropic's official CLI for Claude, running within the Claude Agent SDK.", "You are Claude Code, Anthropic's official CLI tool for Claude, running within the Claude Agent SDK."},
		"计费头与 cc 键值": {"x-anthropic-billing-header: cc_version=2.1; cc_entrypoint=cli; hello", "hello"},
		"裸键名":        {"see `X-Anthropic-Billing-Header` docs", "see `x-anthropic-billing-hdr` docs"},
		"Codex 首句":   {"You are a coding agent running in the Codex CLI, a terminal-based coding assistant.", "You are a coding agent running in the Codex CLI tool, a terminal-based coding assistant."},
		"反馈句":        {"To give feedback, users should report the issue at https://github.com/anthropics/claude-code/issues", "To provide feedback, users should report the issue at https://github.com/anthropics/claude-code/issues"},
		"裸 11128":    {"error code=11128 again", "error code=11-128 again"},
		"无指纹原样":      {"  plain text  ", "  plain text  "},
		"相似但不完全相同不动": {"You are Claude Code.", "You are Claude Code."},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, sanitizeTencentCodeBuddyText(tc.in))
		})
	}
}

func TestPrepareTencentCodeBuddyChatPayload_Sanitize(t *testing.T) {
	body := `{"model":"deepseek-v4.1-flash","messages":[` +
		`{"role":"system","content":"You are Claude Code, Anthropic's official CLI for Claude."},` +
		`{"role":"user","content":[{"type":"text","text":"why 11128?"}]},` +
		`{"role":"assistant","content":null,"reasoning_content":"11128 again","tool_calls":[{"id":"c","type":"function","function":{"name":"f","arguments":"{\"q\":\"11128\"}"}}]},` +
		`{"role":"tool","tool_call_id":"c","content":"ok"}]}`
	out := prepareTencentCodeBuddyChatPayload([]byte(body), codeBuddyPayloadAccount(), "", tencentCodeBuddyContentPolicy{Sanitize: true})
	require.NotContains(t, string(out), "11128")
	require.NotContains(t, string(out), "official CLI for Claude")
	require.Equal(t, "11-128 again", gjson.GetBytes(out, "messages.2.reasoning").String(), "回填镜像出来的 reasoning 也要脱敏")

	// 关闭脱敏时原样。
	out = prepareTencentCodeBuddyChatPayload([]byte(body), codeBuddyPayloadAccount(), "", tencentCodeBuddyContentPolicy{})
	require.Contains(t, string(out), "11128")
}

func TestPrepareTencentCodeBuddyChatPayload_SystemPromptModes(t *testing.T) {
	body := `{"model":"hy3","messages":[{"role":"system","content":"client sys"},{"role":"developer","content":"dev"},{"role":"user","content":"hi"},{"role":"system","content":"mid"}]}`
	account := codeBuddyPayloadAccount()

	out := prepareTencentCodeBuddyChatPayload([]byte(body), account, "", tencentCodeBuddyContentPolicy{PromptMode: WorkBuddyPromptModeCustom, PromptText: "GW"})
	messages := gjson.GetBytes(out, "messages").Array()
	require.Len(t, messages, 2)
	require.Equal(t, "GW", messages[0].Get("content").String())
	require.Equal(t, "user", messages[1].Get("role").String())

	out = prepareTencentCodeBuddyChatPayload([]byte(body), account, "", tencentCodeBuddyContentPolicy{PromptMode: WorkBuddyPromptModeAppend, PromptText: "GW"})
	messages = gjson.GetBytes(out, "messages").Array()
	require.Len(t, messages, 5)
	require.Equal(t, "client sys", messages[0].Get("content").String())
	require.Equal(t, "GW", messages[2].Get("content").String(), "插在开头连续 system/developer 块之后")
	require.Equal(t, "user", messages[3].Get("role").String())

	out = prepareTencentCodeBuddyChatPayload([]byte(body), account, "", tencentCodeBuddyContentPolicy{PromptMode: WorkBuddyPromptModeDegrade, Degraded: true})
	messages = gjson.GetBytes(out, "messages").Array()
	require.Len(t, messages, 2)
	require.Equal(t, tencentCodeBuddyDegradedPrompt, messages[0].Get("content").String())

	out = prepareTencentCodeBuddyChatPayload([]byte(body), account, "", tencentCodeBuddyContentPolicy{PromptMode: WorkBuddyPromptModeDegrade})
	require.Len(t, gjson.GetBytes(out, "messages").Array(), 4, "未降级时透传")
}

func TestTencentCodeBuddyContentPolicyCanDegrade(t *testing.T) {
	require.True(t, tencentCodeBuddyContentPolicy{PromptMode: WorkBuddyPromptModeDegrade}.CanDegrade())
	require.True(t, tencentCodeBuddyContentPolicy{PromptMode: WorkBuddyPromptModeAppend}.CanDegrade())
	require.False(t, tencentCodeBuddyContentPolicy{PromptMode: WorkBuddyPromptModeDegrade, Degraded: true}.CanDegrade())
	require.False(t, tencentCodeBuddyContentPolicy{PromptMode: WorkBuddyPromptModePassthrough}.CanDegrade())
	require.False(t, tencentCodeBuddyContentPolicy{PromptMode: WorkBuddyPromptModeCustom}.CanDegrade())
}

func TestTencentCodeBuddyDegradeGate(t *testing.T) {
	gate := &tencentCodeBuddyDegradeGate{}
	now := time.Date(2026, 10, 9, 22, 30, 0, 0, tencentCodeBuddyResetLoc)
	require.False(t, gate.Active(now))
	gate.Trigger(now)
	require.True(t, gate.Active(now.Add(time.Hour)))
	require.False(t, gate.Active(time.Date(2026, 10, 10, 0, 0, 1, 0, tencentCodeBuddyResetLoc)))
}

func TestIsTencentCodeBuddyContentBlocked(t *testing.T) {
	require.True(t, isTencentCodeBuddyContentBlocked(http.StatusBadRequest, []byte(`{"code":11128,"msg":"x"}`)))
	require.True(t, isTencentCodeBuddyContentBlocked(http.StatusForbidden, []byte(`{"msg":"Illegal API invocation from an unapproved channel"}`)))
	require.False(t, isTencentCodeBuddyContentBlocked(http.StatusBadRequest, []byte(`{"code":11101}`)))
	require.False(t, isTencentCodeBuddyContentBlocked(http.StatusTooManyRequests, []byte(`{"code":11128}`)))
}

func TestTencentCodeBuddyWAFIPGate(t *testing.T) {
	gate := &tencentCodeBuddyWAFIPGate{}
	now := time.Now()
	// 同一账号反复被拦不触发。
	require.False(t, gate.Note("direct", 1, now))
	require.False(t, gate.Note("direct", 1, now.Add(time.Second)))
	// 另一个出口的账号不算。
	require.False(t, gate.Note("proxy:9", 2, now.Add(2*time.Second)))
	require.False(t, gate.Blocked("direct", now.Add(2*time.Second)))
	// 同出口第二个账号：判定 IP 级拦截。
	require.True(t, gate.Note("direct", 3, now.Add(3*time.Second)))
	require.True(t, gate.Blocked("direct", now.Add(10*time.Second)))
	require.False(t, gate.Blocked("proxy:9", now.Add(10*time.Second)))
	require.False(t, gate.Blocked("direct", now.Add(3*time.Second+tencentCodeBuddyWAFIPWindow+time.Second)))

	// 窗口外的旧命中不计。
	gate = &tencentCodeBuddyWAFIPGate{}
	require.False(t, gate.Note("direct", 1, now))
	require.False(t, gate.Note("direct", 2, now.Add(tencentCodeBuddyWAFIPWindow+time.Second)))
}

func TestNormalizeWorkBuddyConfig(t *testing.T) {
	require.Equal(t, WorkBuddyPromptModeDegrade, normalizeWorkBuddyConfig(WorkBuddyConfig{}).PromptMode)
	require.Equal(t, WorkBuddyPromptModeCustom, normalizeWorkBuddyConfig(WorkBuddyConfig{PromptMode: " Custom "}).PromptMode)
	require.Equal(t, WorkBuddyPromptModeDegrade, normalizeWorkBuddyConfig(WorkBuddyConfig{PromptMode: "bogus"}).PromptMode)
	require.NotEmpty(t, WorkBuddyConfig{}.EffectivePromptText())
	require.Equal(t, "x", WorkBuddyConfig{PromptText: "x"}.EffectivePromptText())
}
