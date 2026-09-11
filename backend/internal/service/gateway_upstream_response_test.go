package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// extractUpstreamErrorMessage 决定客户端和运维看到的错误文案。各上游的错误封套
// 形态不同，这里逐形态锁定，避免新增兜底分支时改变既有平台的行为。
func TestExtractUpstreamErrorMessage(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "Claude 风格 error.message",
			body: `{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens: must be greater than 0"}}`,
			want: "max_tokens: must be greater than 0",
		},
		{
			name: "Claude 风格 message 内嵌完整 JSON",
			body: `{"error":{"message":"{\"error\":{\"message\":\"inner detail\"}}"}}`,
			want: "inner detail",
		},
		{
			name: "ChatGPT 内部 API 风格 detail",
			body: `{"detail":"Unsupported parameter: store"}`,
			want: "Unsupported parameter: store",
		},
		{
			name: "顶层 message",
			body: `{"message":"rate limit exceeded"}`,
			want: "rate limit exceeded",
		},
		{
			name: "腾讯封套 msg + 业务码",
			body: `{"code":11101,"msg":"Non-stream chat request is currently not supported","data":null}`,
			want: "Non-stream chat request is currently not supported (code 11101)",
		},
		{
			name: "腾讯封套 msg 非法调用渠道",
			body: `{"code":11128,"msg":"Illegal API invocation from an unapproved channel"}`,
			want: "Illegal API invocation from an unapproved channel (code 11128)",
		},
		{
			name: "腾讯封套只有 msg 没有 code",
			body: `{"msg":"quota exhausted"}`,
			want: "quota exhausted",
		},
		{
			name: "腾讯封套 code 非数字时不拼接",
			body: `{"code":"11101","msg":"non-stream not supported"}`,
			want: "non-stream not supported",
		},
		{
			name: "顶层 message 优先于 msg",
			body: `{"code":11101,"message":"preferred","msg":"ignored"}`,
			want: "preferred",
		},
		{
			name: "无可用字段时返回空串（调用方回落 Upstream error: <status>）",
			body: `{"code":11101,"data":null}`,
			want: "",
		},
		{
			name: "空 body",
			body: ``,
			want: "",
		},
		{
			name: "非 JSON body",
			body: `upstream is on fire`,
			want: "",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, extractUpstreamErrorMessage([]byte(tc.body)))
		})
	}
}
