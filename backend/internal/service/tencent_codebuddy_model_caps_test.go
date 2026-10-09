package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseTencentCodeBuddyModelCapabilities_CatalogShape(t *testing.T) {
	body := []byte(`{"code":0,"data":{"models":[
		{"id":"glm-5.3","name":"GLM-5.3","maxInputTokens":1000000,"maxOutputTokens":48000,"supportsImages":true},
		{"id":"hy3","maxInputTokens":"192000","supportsImages":true,"disabledMultimodal":true,"contextWindow":{"supportedLengths":[128000]}},
		{"id":"kimi-k3-1","disabled":false}
	]}}`)
	caps := parseTencentCodeBuddyModelCapabilities(body)
	require.Len(t, caps, 2, "没有任何能力字段的模型不入表")

	glm := caps["glm-5.3"]
	require.Equal(t, int64(1_000_000), glm.ContextWindow)
	require.Equal(t, int64(48_000), glm.MaxOutputTokens)
	require.NotNil(t, glm.SupportsImages)
	require.True(t, *glm.SupportsImages)
	require.Equal(t, "GLM-5.3", glm.DisplayName)
	require.Equal(t, []string{"text", "image"}, glm.InputModalities())

	hy3 := caps["hy3"]
	require.Equal(t, int64(192_000), hy3.ContextWindow, "字符串数字也接受；contextWindow 对象被跳过")
	require.NotNil(t, hy3.SupportsImages)
	require.False(t, *hy3.SupportsImages, "disabledMultimodal 优先于 supportsImages")
}

func TestParseTencentCodeBuddyModelCapabilities_OtherShapes(t *testing.T) {
	enterprise := parseTencentCodeBuddyModelCapabilities([]byte(`{"code":0,"data":[{"id":"a","maxOutputTokens":1000}]}`))
	require.Equal(t, int64(1000), enterprise["a"].MaxOutputTokens)

	product := parseTencentCodeBuddyModelCapabilities([]byte(`{"productName":"x","models":[{"id":"b","maxInputTokens":2000}]}`))
	require.Equal(t, int64(2000), product["b"].ContextWindow)

	require.Nil(t, parseTencentCodeBuddyModelCapabilities([]byte(`not json`)))
	require.Nil(t, parseTencentCodeBuddyModelCapabilities([]byte(`{"data":{"models":[{"id":"c"}]}}`)))
}

func TestAccountTencentCodeBuddyModelCapability_Priority(t *testing.T) {
	account := &Account{Platform: PlatformTencentCodeBuddy}

	capability, source := account.TencentCodeBuddyModelCapability("glm-5.3")
	require.Equal(t, TencentCodeBuddyCapabilitySourceBuiltin, source)
	require.Equal(t, int64(48_000), capability.MaxOutputTokens)

	_, source = account.TencentCodeBuddyModelCapability("unknown-model")
	require.Equal(t, TencentCodeBuddyCapabilitySourceDefault, source)

	no := false
	account.Extra = map[string]any{TencentCodeBuddyModelCapabilitiesExtraKey: TencentCodeBuddyModelCapabilitySnapshot{
		Models: map[string]TencentCodeBuddyModelCapability{"glm-5.3": {ContextWindow: 200_000, SupportsImages: &no}},
	}}
	capability, source = account.TencentCodeBuddyModelCapability("glm-5.3")
	require.Equal(t, TencentCodeBuddyCapabilitySourceUpstream, source)
	require.Equal(t, int64(200_000), capability.ContextWindow)
	require.Equal(t, int64(48_000), capability.MaxOutputTokens, "快照缺的字段用内置表补")
	require.False(t, *capability.SupportsImages)
}

func TestResolveTencentCodeBuddyGroupModelCapability_TakesMinimum(t *testing.T) {
	yes, no := true, false
	snapshot := func(cw, out int64, images *bool) map[string]any {
		return map[string]any{TencentCodeBuddyModelCapabilitiesExtraKey: TencentCodeBuddyModelCapabilitySnapshot{
			Models: map[string]TencentCodeBuddyModelCapability{"m": {ContextWindow: cw, MaxOutputTokens: out, SupportsImages: images}},
		}}
	}
	accounts := []Account{
		{ID: 1, Platform: PlatformTencentCodeBuddy, Extra: snapshot(1_000_000, 32_000, &yes)},
		{ID: 2, Platform: PlatformTencentCodeBuddy, Extra: snapshot(200_000, 64_000, &no)},
		{ID: 3, Platform: PlatformOpenAI, Extra: snapshot(1, 1, &yes)},
	}
	capability, source := ResolveTencentCodeBuddyGroupModelCapability(accounts, "m")
	require.Equal(t, TencentCodeBuddyCapabilitySourceUpstream, source)
	require.Equal(t, int64(200_000), capability.ContextWindow)
	require.Equal(t, int64(32_000), capability.MaxOutputTokens)
	require.False(t, *capability.SupportsImages)
}

func TestTencentCodeBuddyClampMaxOutputTokens(t *testing.T) {
	account := &Account{ID: 7, Platform: PlatformTencentCodeBuddy}
	body := []byte(`{"model":"glm-5.3","max_tokens":128000,"max_completion_tokens":1000}`)

	// 只有内置表（推断值）时不裁剪。
	require.Equal(t, string(body), string(tencentCodeBuddyClampMaxOutputTokens(body, account, 0)))

	// 后台覆盖优先。
	out := tencentCodeBuddyClampMaxOutputTokens(body, account, 40_000)
	require.JSONEq(t, `{"model":"glm-5.3","max_tokens":40000,"max_completion_tokens":1000}`, string(out))

	// 上游快照上限。
	account.Extra = map[string]any{TencentCodeBuddyModelCapabilitiesExtraKey: TencentCodeBuddyModelCapabilitySnapshot{
		Models: map[string]TencentCodeBuddyModelCapability{"glm-5.3": {MaxOutputTokens: 48_000}},
	}}
	out = tencentCodeBuddyClampMaxOutputTokens(body, account, 0)
	require.JSONEq(t, `{"model":"glm-5.3","max_tokens":48000,"max_completion_tokens":1000}`, string(out))
}

func TestFetchModelCatalog_MergesProductConfig(t *testing.T) {
	catalog := `{"code":0,"data":{"models":[{"id":"glm-5.3","maxInputTokens":1000000},{"id":"hy3"}]}}`
	config := `{"code":0,"data":{"models":[{"id":"glm-5.3","maxOutputTokens":48000},{"id":"hy3","maxInputTokens":192000,"supportsImages":true},{"id":"other","maxInputTokens":1}]}}`
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(catalog))},
		{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(config))},
	}}
	client := NewTencentCodeBuddyClient(upstream)
	account := tencentCodeBuddyTestAccount(map[string]any{
		tencentCodeBuddyCredProduct: TencentCodeBuddyProductWorkBuddy,
		tencentCodeBuddyCredRegion:  TencentCodeBuddyRegionChina,
	})

	models, caps, _, err := client.FetchModelCatalog(context.Background(), account)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"glm-5.3", "hy3"}, models)
	require.Len(t, upstream.requests, 2)
	require.Equal(t, "https://www.workbuddy.cn"+tencentCodeBuddyProductConfigPath, upstream.requests[1].URL.String())
	require.Equal(t, int64(1_000_000), caps["glm-5.3"].ContextWindow, "目录接口的值优先")
	require.Equal(t, int64(48_000), caps["glm-5.3"].MaxOutputTokens, "缺的字段由产品配置补齐")
	require.Equal(t, int64(192_000), caps["hy3"].ContextWindow)
	_, hasOther := caps["other"]
	require.False(t, hasOther, "目录里没有的模型不写入快照")
}

func TestBuildTencentCodeBuddyModelCapabilityReport(t *testing.T) {
	account := tencentCodeBuddyTestAccount(nil)
	account.Extra = map[string]any{TencentCodeBuddyModelCapabilitiesExtraKey: TencentCodeBuddyModelCapabilitySnapshot{
		SyncedAt: "2026-10-09T00:00:00Z",
		Models:   map[string]TencentCodeBuddyModelCapability{"hy3-x": {ContextWindow: 128_000}},
	}}
	report := BuildTencentCodeBuddyModelCapabilityReport([]Account{*account})
	require.Equal(t, "2026-10-09T00:00:00Z", report.SyncedAt)
	sources := map[string]string{}
	for _, row := range report.Models {
		sources[row.ModelID] = row.Source
	}
	require.Equal(t, TencentCodeBuddyCapabilitySourceUpstream, sources["hy3-x"])
	require.Equal(t, TencentCodeBuddyCapabilitySourceBuiltin, sources["glm-5.3"])
	// minimax-m3 只有内置的推理档位，没有参数，也算内置来源。
	require.Equal(t, TencentCodeBuddyCapabilitySourceBuiltin, sources["minimax-m3"])
}
