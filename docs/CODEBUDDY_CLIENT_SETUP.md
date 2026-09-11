# 腾讯 CodeBuddy 客户端接入说明（识图 / 推理档位 / 能力声明）

> Client setup for Tencent CodeBuddy upstreams: image input, reasoning effort and
> capability declaration. **TL;DR** — Sub2API already advertises image input and
> reasoning levels for CodeBuddy; clients that keep their **own** model capability
> table (DSH, pi, …) must declare the same capabilities locally, otherwise they
> block images and hide the thinking selector before the request ever leaves the
> client.

本文记录腾讯 CodeBuddy 私有上游的**实测能力**，以及为什么"服务端声明了"之后客户端
往往还要再配一次。适用于 Sub2API 的 `codebuddy` 平台账号。

实测日期：**2026-09-12**，账号为真实腾讯 CodeBuddy 订阅，模型目录为当期 15 个模型。

---

## 1. 实测能力（服务端视角）

全部通过 Sub2API 的 `/v1/chat/completions` 打到
`https://copilot.tencent.com/v2/chat/completions`，`stream: true`。

### 1.1 图片输入：15/15 模型全部支持

请求形态为 OpenAI 形状的 `messages[].content` 图片部分：

```json
{"type": "image_url", "image_url": {"url": "data:image/png;base64,..."}}
```

| 形态 | 结果 |
|---|---|
| `data:image/png;base64,…`（内联） | ✅ 200，正确识别 |
| `https://…` 公网 URL | ✅ 200，**上游自己去下载** |
| 不存在的域名 / 404 的 URL | ❌ 400（上游抓取失败） |

**对照组（关键）**：同一张三色横条图 + 同一问题

| 请求 | 回答 |
|---|---|
| 带图 | 「3 个，红色，蓝色，绿色」✅ |
| 不带图 | 「5 个，…」❌ 纯猜 |

说明模型确实"看到"了图片，而不是从文字里推断。

`auto / hy4-preview / hy3 / hy3-x / deepseek-v4.1-flash / deepseek-v4-pro /
glm-5.3 / glm-5.3-flash / glm-5.2 / glm-5.1 / glm-5v-turbo / kimi-k3-1 /
kimi-k2.7 / kimi-k2.6 / minimax-m3` 全部通过。

> 注意：测试图是三个纯色块，只证明"图片能进去且被感知"，不代表 OCR / 细粒度
> 图像理解的质量。

### 1.2 推理档位：`reasoning_effort` 是有效字段

指标为响应 `usage.completion_thinking_tokens`，单次采样（数值有波动）：

| 模型 | 不传 | low | medium | high |
|---|---|---|---|---|
| auto | 0 | 82 | 77 | 59 |
| hy4-preview | 1048 | 369 | 475 | 303 |
| hy3 | 335 | 317 | 396 | 365 |
| hy3-x | 447 | 326 | 336 | 356 |
| deepseek-v4.1-flash | 0 | 85 | 85 | 80 |
| deepseek-v4-pro | 0 | 123 | 96 | 159 |
| glm-5.3 | 126 | 1 | 23 | 27 |
| glm-5.3-flash | 91 | 0 | 0 | 0 |
| glm-5.2 | 0 | 320 | 568 | 330 |
| glm-5.1 | 0 | 265 | 268 | 225 |
| glm-5v-turbo | 0 | 631 | 563 | 766 |
| kimi-k3-1 | 80 | 67 | 105 | 95 |
| kimi-k2.7 | 175 | 100 | 123 | 127 |
| kimi-k2.6 | 1 | 623 | 950 | 572 |
| minimax-m3 | 0 | 94 | 63 | 103 |

两条结论：

- 上游**识别** `reasoning_effort`：7 个模型不传时为 0，传任意档位后变成 59~766。
- **档位 → 思考量的映射是模型私有的，且不单调**（glm-5.2: 320/568/330；
  hy4-preview: 369/475/303）。网关只做透传，不对思考量作任何承诺。

被**静默忽略**的字段（都返回 200，思考量不变）：`thinking: {type:"enabled"}`、
`enable_thinking: true`、`reasoning: {effort:"high"}`。因此传错参数不会报错，
但也别指望生效。

### 1.3 其它

| 项 | 结论 |
|---|---|
| `max_tokens` | ✅ 生效且正确截断（5→5、20→20，`finish_reason=length`）；上游不校验上限（试到 100 万仍 200） |
| 非流式请求 | ❌ **不支持**，上游回业务码 `11101`。必须 `stream: true` |
| `messages[].role: "developer"` | ❌ 拒绝，业务码 `11128`（OpenAI 新规范里 system 的别名） |
| `tool_choice` 对象形态 | ❌ 只接受字符串 |
| 错误封套 | `{"code": 11101, "msg": "...", "data": ...}`，**不是** OpenAI 形状 |

`developer` 与 `tool_choice` 两项已由 Sub2API 在转发前自动归一化
（`backend/internal/service/openai_gateway_codebuddy_payload.go`），客户端无需处理。

---

## 2. Sub2API 已经声明了什么

以下能力由服务端自动补齐，**客户端不需要额外配置**（前提是客户端愿意读）：

| 接口 | 字段 |
|---|---|
| `GET /v1/models` | `input_modalities`、`architecture.input_modalities`、`context_window`、`max_output_tokens` |
| Codex manifest | `input_modalities`、`context_window`、`max_context_window`、`supported_reasoning_levels`、`default_reasoning_level` |

上下文窗口与输出上限没有可靠来源（腾讯目录只回 `{id, disabled}`，账号也不存
base_url 所以匹配不到 models.dev），因此由管理员在
**系统设置 → 模型能力** 里手工声明，留空表示"不声明"（不会编造数字）。

写入 `settings.model_capability_config`，只影响对外广告，**不改变转发行为**。

```json
{
  "models": [
    {
      "platform": "codebuddy",
      "model_id": "hy3",
      "context_window": 200000,
      "max_output_tokens": 64000,
      "input_modalities": ["text", "image"]
    }
  ]
}
```

---

## 3. 为什么客户端还要配一次（这一节是重点）

**DSH、pi 这类客户端有自己的模型能力表，不会读 `/v1/models` 的能力字段。**

以 DSH 为例，拦截图片的判断在 `dsh-api-session-controller`：

```js
const model = await this.ctx.llm.resolveModelInfo(current.provider, current.model);
if (model.inputModalities !== undefined && !model.inputModalities.includes("image"))
    throw new RemoteError(..., { reason: "MODEL_DOES_NOT_SUPPORT_IMAGES" });
```

`resolveModelInfo` 来自**适配器自己的配置**（`~/.dsh/settings.yaml`），不是
Sub2API。所以手写进配置的模型如果没有声明模态，就会被判定为纯文本 —— 图片在
客户端就被拦下，**根本没有发到 Sub2API**。表现是聊天界面弹出
「当前模型不支持图片，请切换支持图片的模型」，而模型把附件当成 `[1]` 之类的
文字回应。

思考档位同理：菜单由 `buildModelCatalog` 从 `resolveModelInfo(...).reasoning`
构建，而适配器只有在模型条目声明了 `reasoningEfforts` 时才会返回 `reasoning`。

---

## 4. DSH 配置（适配器 `dsh-llm-pi-ai`）

配置文件：`~/.dsh/settings.yaml`

```yaml
agent-default-model:
  provider: aaa
  model: deepseek-v4.1-flash
llm-pi-ai:
  providers:
    {
      aaa:
        {
          apiKeyEnv: AAA_API_KEY,
          api: openai-completions,
          baseURL: http://127.0.0.1:8080/v1,
          models:
            [
              {
                id: deepseek-v4.1-flash,
                name: deepseek-v4.1-flash,
                input: [text, image],
                reasoningEfforts: { "off": null, "low": "low", "medium": "medium", "high": "high" },
                compat: { supportsReasoningEffort: true }
              }
            ]
        }
    }
permission:
  defaultPreset: danger-full-access
```

### 4.1 字段说明

| 字段 | 必填 | 作用 |
|---|---|---|
| `input` | 识图必填 | 模型支持的输入模态，白名单只有 `text` / `image`。**缺省为 `["text"]`** |
| `reasoningEfforts` | 档位菜单必填 | 字典：**DSH 档位 → 发往上游的 wire 值**。只列出提供的档位 |
| `compat.supportsReasoningEffort` | 档位生效必填 | 允许 pi-ai 往请求体写 `reasoning_effort` |

档位白名单（`THINKING_LEVELS`）：
`off` / `minimal` / `low` / `medium` / `high` / `xhigh` / `max`。
只声明 `low`/`medium`/`high` 时，菜单就是这 3 档 + "Provider default"。

`"off": null` 是刻意写法：DSH 约定 `off` 的 wire 值为 `null`，表示**不发送该
字段**。实测 `deepseek-v4.1-flash` 不带 `reasoning_effort` 时思考量为 0，所以
「Off」是真的关闭，而不是发一个假值。

至少要有**一个非 `off` 档位**，否则 DSH 加载时会报
`reasoningEfforts offers no level beyond "off"`。

### 4.2 三个容易踩的坑

1. **字段名按适配器而不同。** `dsh-llm-pi-ai` 用 `input`；
   `dsh-llm-deepseek` 用 `inputModalities`。写错名字会被 schema 静默忽略，
   比报错更难查。
2. **`compat.supportsReasoningEffort` 不能省。** 省了菜单会出现，但选了什么
   都不会发出去 —— pi-ai 的 `openai-completions` 里最终那条兜底分支是这个条件：
   ```js
   else if (options?.reasoningEffort && model.reasoning && compat.supportsReasoningEffort) {
       params.reasoning_effort = model.thinkingLevelMap?.[options.reasoningEffort] ?? options.reasoningEffort;
   }
   ```
3. **YAML 1.1 会把裸写的 `off` 解析成布尔 `false`。** DSH 用 js-yaml 4
   （YAML 1.2，`off` 是普通字符串）所以裸写也能用，但请加引号
   `"off": null`，避免被 PyYAML 之类的 1.1 解析器或格式化工具改坏。

### 4.3 生效方式

pi-ai 适配器每次操作都重读配置（含模型目录），**改完不需要重启 DSH**，
刷新页面重新打开模型菜单即可。

### 4.4 图片限额

声明 `input` 含 `image` 后，适配器会给默认限额：单张 ≤ **1 MiB**
（`requestImageMaxBytes`）、像素预算 **4194304**（2048×2048）。需要放宽就在
同一个 provider 下加：

```yaml
requestImageMaxBytes: 5242880
```

---

## 5. DSH 配置（适配器 `dsh-llm-deepseek`）

如果你用的是官方 DeepSeek 适配器而不是 pi-ai，字段名与档位来源都不同：

```yaml
llm-deepseek:
  providers:
    deepseek-official:
      models:
        - id: deepseek-v4.1-flash
          name: deepseek-v4.1-flash
          inputModalities: [text, image]
          imageMaxBytes: 1048576
```

两个适配器的差异：

| | `dsh-llm-pi-ai` | `dsh-llm-deepseek` |
|---|---|---|
| 模态字段 | `input` | `inputModalities` |
| 档位声明 | 模型级 `reasoningEfforts`（档位 → wire 值可自定义） | 无；档位由适配器固定为 `off` / `low` / `high` / `max` |
| 档位默认 | provider 级 `reasoning` | provider 级 `thinking: enabled\|disabled` + `reasoningEffort` |
| 图片限额 | `requestImageMaxBytes` / `requestImagePixelBudget`（provider 级） | `imageMaxBytes` / `imagePixelBudget`（模型级） |

注意后者的档位词汇**写死在适配器里**，且 `thinking: disabled` 时任何非 `off` 的
档位都会报 `UNSUPPORTED_REASONING_EFFORT`。腾讯上游实测 `low` / `medium` / `high`
都可用，而官方适配器没有 `medium`，所以想要完整档位请用 pi-ai 适配器。

---

## 6. 其它客户端

| 客户端 | 行为 |
|---|---|
| Codex（读 `/backend-api/codex/models`） | 自动生效，Sub2API 的 manifest 已声明图片与档位 |
| 读 `GET /v1/models` 能力字段的客户端 | 自动生效 |
| **自带模型能力表的客户端**（DSH、pi、各类本地 Agent） | **必须在本机配置里声明**，见上文 |

判断方法：把图片加进去看客户端有没有拦。如果客户端弹"不支持图片"，那就是
客户端自己的表没声明；如果图片发出去了但模型答非所问，那才回去查 Sub2API。

---

## 7. 自查命令

```bash
# 图片（应先 200 并答出颜色；不带图会给出错误答案）
curl -N -sS http://127.0.0.1:8080/v1/chat/completions \
  -H "Authorization: Bearer $SUB2API_KEY" -H 'Content-Type: application/json' \
  -d '{"model":"glm-5v-turbo","stream":true,"messages":[{"role":"user","content":[
        {"type":"text","text":"这张图从上到下依次是什么颜色？"},
        {"type":"image_url","image_url":{"url":"data:image/png;base64,<B64>"}}]}]}'

# 推理档位（观察 usage.completion_thinking_tokens 是否随档位变化）
curl -N -sS http://127.0.0.1:8080/v1/chat/completions \
  -H "Authorization: Bearer $SUB2API_KEY" -H 'Content-Type: application/json' \
  -d '{"model":"deepseek-v4.1-flash","stream":true,"reasoning_effort":"high",
       "messages":[{"role":"user","content":"12 个球分 3 组每组几个？只回答数字。"}]}'

# 服务端声明（应含 input_modalities）
curl -sS http://127.0.0.1:8080/v1/models -H "Authorization: Bearer $SUB2API_KEY"
```

---

## 8. 已知限制

| 限制 | 说明 |
|---|---|
| 非流式不支持 | 上游回 `11101`。需要非流式的客户端（如某些 Agent 的"生成会话标题"请求）会失败；Sub2API 侧若要支持需在网关做「强制 stream + SSE 聚合」，当前未实现 |
| 档位不单调 | 上游行为，网关只透传 |
| 图片 URL 抓取失败 = 400 | 上游自己去下载外链，抓不到就报错；内联 base64 不受影响 |
| 上下文窗口靠人工 | 上游与公共注册表都没有数据，需在「模型能力」里手工声明 |
| 能力字段是"广告" | Sub2API 只声明，不做请求侧校验；声明错了不会拦请求，只会误导客户端 |
