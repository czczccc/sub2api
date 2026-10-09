package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// CodeBuddy 的 /v1/models 必须带上能力字段：默认复用 claude.Model 时只有
// id/type/display_name/created_at，客户端读不到输入模态就不会发图片。
func TestWriteCodeBuddyModelsListAdvertisesCapabilities(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	// 只给 hy3 配置了上下文窗口与输出上限；glm-5.3 应回落到平台默认模态且不猜数字。
	resolve := func(modelID string) (service.ModelCapabilityEntry, bool) {
		if modelID == "hy3" {
			return service.ModelCapabilityEntry{
				Platform: service.PlatformTencentCodeBuddy, ModelID: "hy3",
				ContextWindow: 200_000, MaxOutputTokens: 64_000,
			}, true
		}
		return service.ModelCapabilityEntry{}, false
	}

	writeCodeBuddyModelsList(c, []string{"hy3", "glm-5.3"}, resolve, nil)
	require.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		Object string `json:"object"`
		Data   []struct {
			ID              string   `json:"id"`
			Type            string   `json:"type"`
			DisplayName     string   `json:"display_name"`
			InputModalities []string `json:"input_modalities"`
			Architecture    *struct {
				InputModalities  []string `json:"input_modalities"`
				OutputModalities []string `json:"output_modalities"`
			} `json:"architecture"`
			ContextWindow   int64 `json:"context_window"`
			MaxOutputTokens int64 `json:"max_output_tokens"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "list", body.Object)
	require.Len(t, body.Data, 2)

	hy3 := body.Data[0]
	require.Equal(t, "hy3", hy3.ID)
	require.Equal(t, "model", hy3.Type)
	require.Equal(t, []string{"text", "image"}, hy3.InputModalities)
	require.Equal(t, int64(200_000), hy3.ContextWindow)
	require.Equal(t, int64(64_000), hy3.MaxOutputTokens)
	require.NotNil(t, hy3.Architecture)
	require.Equal(t, []string{"text", "image"}, hy3.Architecture.InputModalities)
	require.Equal(t, []string{"text"}, hy3.Architecture.OutputModalities)

	glm := body.Data[1]
	require.Equal(t, "glm-5.3", glm.ID)
	require.Equal(t, []string{"text", "image"}, glm.InputModalities)
	require.Zero(t, glm.ContextWindow, "未声明的上下文窗口必须留空而不是编造数字")
	require.Zero(t, glm.MaxOutputTokens)

	// 未声明能力时字段应被 omitempty 省略，避免客户端把 0 当成真实上限。
	raw := rec.Body.String()
	require.NotContains(t, raw, `"context_window":0`)
	require.NotContains(t, raw, `"max_output_tokens":0`)
}

// resolver 为 nil（未装配 settingService）时仍要输出默认模态。
func TestWriteCodeBuddyModelsListWithoutResolver(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	writeCodeBuddyModelsList(c, []string{"auto"}, nil, nil)

	var body struct {
		Data []struct {
			ID              string   `json:"id"`
			InputModalities []string `json:"input_modalities"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Data, 1)
	require.Equal(t, []string{"text", "image"}, body.Data[0].InputModalities)
}

// 上游参数逐字段生效，后台覆盖只压过它填了的字段；并输出 OpenRouter 兼容字段。
func TestWriteCodeBuddyModelsListMergesUpstreamCapabilities(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	noImages := false
	upstream := map[string]service.TencentCodeBuddyModelCapability{
		"glm-5.3": {ContextWindow: 1_000_000, MaxOutputTokens: 48_000, SupportsImages: &noImages, DisplayName: "GLM-5.3"},
	}
	resolve := func(modelID string) (service.ModelCapabilityEntry, bool) {
		if modelID == "glm-5.3" {
			return service.ModelCapabilityEntry{ModelID: modelID, ContextWindow: 200_000}, true
		}
		return service.ModelCapabilityEntry{}, false
	}
	writeCodeBuddyModelsList(c, []string{"glm-5.3"}, resolve, upstream)

	var body struct {
		Data []struct {
			DisplayName     string   `json:"display_name"`
			InputModalities []string `json:"input_modalities"`
			ContextWindow   int64    `json:"context_window"`
			MaxOutputTokens int64    `json:"max_output_tokens"`
			ContextLength   int64    `json:"context_length"`
			TopProvider     struct {
				ContextLength       int64 `json:"context_length"`
				MaxCompletionTokens int64 `json:"max_completion_tokens"`
			} `json:"top_provider"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Data, 1)
	item := body.Data[0]
	require.Equal(t, "GLM-5.3", item.DisplayName)
	require.Equal(t, []string{"text"}, item.InputModalities)
	require.Equal(t, int64(200_000), item.ContextWindow, "后台覆盖优先")
	require.Equal(t, int64(48_000), item.MaxOutputTokens, "覆盖未填的字段沿用上游值")
	require.Equal(t, int64(200_000), item.ContextLength)
	require.Equal(t, int64(200_000), item.TopProvider.ContextLength)
	require.Equal(t, int64(48_000), item.TopProvider.MaxCompletionTokens)
}
