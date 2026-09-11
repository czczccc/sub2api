package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 这些用例锁住 CodeBuddy 私有上游的请求体约束（依据参考实现
// Sliverkiss/workbuddy2api internal/upstream/payload.go 的实测结论）。
// 回归症状：messages[].role=developer → 上游 HTTP 400 code=11128。

func TestNormalizeTencentCodeBuddyUpstreamPayload_DeveloperRoleBecomesSystem(t *testing.T) {
	body := []byte(`{"model":"auto","messages":[
		{"role":"developer","content":"You are a coding agent."},
		{"role":"System","content":"keep me"},
		{"role":"user","content":"hi"},
		{"role":"assistant","content":"hello"},
		{"role":"tool","content":"result"}
	]}`)

	out := normalizeTencentCodeBuddyUpstreamPayload(body)

	roles := make([]string, 0, 5)
	for _, m := range gjson.GetBytes(out, "messages").Array() {
		roles = append(roles, m.Get("role").String())
	}
	require.Equal(t, []string{"system", "System", "user", "assistant", "tool"}, roles)
	// 只改 role，其余内容与消息条数不变。
	require.Equal(t, "You are a coding agent.", gjson.GetBytes(out, "messages.0.content").String())
	require.Len(t, gjson.GetBytes(out, "messages").Array(), 5)
}

func TestNormalizeTencentCodeBuddyUpstreamPayload_DeveloperRolePaddedAndMixedCase(t *testing.T) {
	body := []byte(`{"messages":[{"role":"  DEVELOPER  ","content":"x"}]}`)
	out := normalizeTencentCodeBuddyUpstreamPayload(body)
	require.Equal(t, "system", gjson.GetBytes(out, "messages.0.role").String())
}

func TestNormalizeTencentCodeBuddyUpstreamPayload_ToolChoiceObjectForms(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		check func(t *testing.T, out []byte)
	}{
		{
			name: "auto object becomes string",
			body: `{"messages":[],"tool_choice":{"type":"auto"},"tools":[{"type":"function"}]}`,
			check: func(t *testing.T, out []byte) {
				require.Equal(t, "auto", gjson.GetBytes(out, "tool_choice").String())
				require.True(t, gjson.GetBytes(out, "tool_choice").Type == gjson.String)
				require.True(t, gjson.GetBytes(out, "tools").Exists())
			},
		},
		{
			name: "required object becomes string",
			body: `{"messages":[],"tool_choice":{"type":"required"}}`,
			check: func(t *testing.T, out []byte) {
				require.Equal(t, "required", gjson.GetBytes(out, "tool_choice").String())
			},
		},
		{
			name: "named function becomes bare name",
			body: `{"messages":[],"tool_choice":{"type":"function","function":{"name":"run_command"}}}`,
			check: func(t *testing.T, out []byte) {
				require.Equal(t, "run_command", gjson.GetBytes(out, "tool_choice").String())
			},
		},
		{
			name: "none drops tool_choice and tools",
			body: `{"messages":[],"tool_choice":"none","tools":[{"type":"function"}],"functions":[{"name":"f"}]}`,
			check: func(t *testing.T, out []byte) {
				require.False(t, gjson.GetBytes(out, "tool_choice").Exists())
				require.False(t, gjson.GetBytes(out, "tools").Exists())
				require.False(t, gjson.GetBytes(out, "functions").Exists())
			},
		},
		{
			name: "object none drops tool_choice and tools",
			body: `{"messages":[],"tool_choice":{"type":"none"},"tools":[{"type":"function"}]}`,
			check: func(t *testing.T, out []byte) {
				require.False(t, gjson.GetBytes(out, "tool_choice").Exists())
				require.False(t, gjson.GetBytes(out, "tools").Exists())
			},
		},
		{
			name: "unknown object form is dropped but tools kept",
			body: `{"messages":[],"tool_choice":{"type":"mystery"},"tools":[{"type":"function"}]}`,
			check: func(t *testing.T, out []byte) {
				require.False(t, gjson.GetBytes(out, "tool_choice").Exists())
				require.True(t, gjson.GetBytes(out, "tools").Exists())
			},
		},
		{
			name: "plain string forms are preserved",
			body: `{"messages":[],"tool_choice":"auto"}`,
			check: func(t *testing.T, out []byte) {
				require.Equal(t, "auto", gjson.GetBytes(out, "tool_choice").String())
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.check(t, normalizeTencentCodeBuddyUpstreamPayload([]byte(tc.body)))
		})
	}
}

func TestNormalizeTencentCodeBuddyUpstreamPayload_LeavesUnrelatedBodiesUntouched(t *testing.T) {
	// 无 messages / 无 tool_choice：必须逐字节不变（避免无谓的键重排）。
	body := []byte(`{"model":"auto","stream":true,"temperature":0.7}`)
	require.Equal(t, string(body), string(normalizeTencentCodeBuddyUpstreamPayload(body)))

	// 非法 JSON 原样返回，交给上游报错，不在网关侧吞掉。
	broken := []byte(`{"messages":[`)
	require.Equal(t, string(broken), string(normalizeTencentCodeBuddyUpstreamPayload(broken)))

	// 空 body。
	require.Empty(t, normalizeTencentCodeBuddyUpstreamPayload(nil))
}

func TestNormalizeTencentCodeBuddyUpstreamPayload_DoesNotTouchOtherFields(t *testing.T) {
	body := []byte(`{"model":"deepseek-v4.1-flash","max_tokens":12345678901,"messages":[{"role":"developer","content":"x"}],"seed":9007199254740993}`)
	out := normalizeTencentCodeBuddyUpstreamPayload(body)

	// 定点改写必须保住大整数不被 float64 破坏（这正是用 sjson 而非整体反序列化的原因）。
	require.Equal(t, "12345678901", gjson.GetBytes(out, "max_tokens").Raw)
	require.Equal(t, "9007199254740993", gjson.GetBytes(out, "seed").Raw)
	require.Equal(t, "deepseek-v4.1-flash", gjson.GetBytes(out, "model").String())
}
