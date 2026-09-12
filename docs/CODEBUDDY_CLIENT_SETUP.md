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

> **国际版（WorkBuddy）**：`codebuddy` 平台账号现在支持 4 个站点
> （CodeBuddy / WorkBuddy × 大陆 / 国际），选站方式见第 9 节。第 1–8 节的能力结论
> 是在**大陆 CodeBuddy**（`copilot.tencent.com`）上实测的。国际版（`www.workbuddy.ai`）
> 已于 2026-09-12 用真实账号验证：**聊天链路可用，但模型目录与大陆站不重合**，
> 详见第 9.3 节。

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
| 两站点目录不重合 | 大陆 15 个、国际 18 个，交集有限（GPT/Gemini 系列只有国际有，`deepseek-v4-pro` 等只有大陆有）。模型名不通用，用错站点返回 `11102`。见第 9.3.2 节 |
| 目录随套餐变化 | `data.models` 与 `data.modelPromotions` 由服务端下发（含促销与倍率），因此静态兜底只是快照，运行时以实时目录为准 |

---

## 9. 站点矩阵（CodeBuddy / WorkBuddy × 大陆 / 国际）

`codebuddy` 平台账号带两个维度：`product`（品牌）与 `region`（区域），
组合决定上游站点。四个 host 均已实测可达（`/v3/config` 200、`/v2/chat/completions` 401）。

| product | region | 上游 host（API root = host + `/v2`） | 默认 X-Domain | 备注 |
|---|---|---|---|---|
| `codebuddy` | `china` | `https://copilot.tencent.com` | `www.codebuddy.cn` | 历史默认；本文第 1–8 节的实测环境 |
| `codebuddy` | `global` | `https://www.codebuddy.ai` | `www.codebuddy.ai` | CodeBuddy 国际版 |
| `workbuddy` | `china` | `https://www.workbuddy.cn` | `www.workbuddy.cn` | WorkBuddy 大陆版 |
| `workbuddy` | `global` | `https://www.workbuddy.ai` | `www.workbuddy.ai` | **WorkBuddy 国际版**（用户目标站点） |

四个站点的**路径完全一致**（`/v2/chat/completions`、`/v2/plugin/auth/state`、
`/v2/plugin/auth/token`、`/v2/plugin/auth/token/refresh`、`/v2/plugin/login/account`、
`/v3/config`、`/console/enterprises/personal/models`），只有 host 与 `X-Domain` 不同。

### 9.1 怎么选站

「添加账号」→ 选 **CodeBuddy / WorkBuddy** 平台 → 表单顶部的「站点」二选一网格里点选
（4 个站点）。选好后「生成授权链接」得到的 `authUrl` 由该站点自行派生，因此**登录页
自动就是对的站点**，不需要额外参数。切换站点会作废已生成的链接与 `state`
（`state` 是站点侧签发的，跨站轮询拿不到凭据）。

手工填写路径共享同一个站点选择器——`product` / `region` 是前端必须显式提交的字段，
后端无法从令牌本身可靠推断。

### 9.2 账号级 `domain` 与站点默认值

`credentials.domain` 只影响 `X-Domain` 头，**不改变 host**。它对应官方登录态里的
`auth.domain`：留空即用所选站点的默认域；仅当账号确实属于别的域时才填。
把默认值落盘会让它固化成显式配置，之后用户改了站点，`X-Domain` 不会跟随——
因此 `domain` 等于站点默认值时会被有意丢弃。

### 9.3 国际版实测（2026-09-12，真实 workbuddy.ai 订阅账号）

以下结论用账号的 `access_token` 直接打 `https://www.workbuddy.ai` 逐项验证过。

#### 9.3.1 目录接口的正解是 `/v2/enterprises/personal/models`

**踩过的坑（曾据此写出错误结论，特此更正）**：早期实现（含参考项目
`Sliverkiss/workbuddy2api`）用的是 `GET {host}/console/enterprises/personal/models`。
它在大陆站可用，但在 `www.workbuddy.ai` 上**带令牌恒返回 APISIX 原始 500**
（HTML 500，不是 `{code,msg,data}` 业务信封），且与 UA 无关；无令牌时四个站点都返回
302（网页控制台路由）。当时误判为"国际站没有目录接口"，于是退化成静态清单——
而那份清单是拿**大陆站模型名**逐个试探出来的，**完全漏掉了国际站真实目录**。

正解来自官方客户端本体（`WorkBuddy/resources/app.asar` 里
`daemon.cloudAgent` 的 `listAvailableModels`）：

```
GET {endpoint}/v2/enterprises/personal/models
```

实测：该路径在大陆与国际站点都稳定返回 **401**（未带令牌），是行为一致的 API 路由。
用真实账号打通后返回 **200 + 18 个模型**，且响应里的 `data.models[].credits`
就是客户端选择器显示的倍率（`glm-5.3` → `x0.79`、`gpt-5.6-sol` → `x3.47`），
`data.modelPromotions` 是 "Free now" 角标的来源。**这份列表与用户截图完全对上。**

因此实现改为：主路径 `/v2/enterprises/personal/models`，失败再回退老路径
`/console/enterprises/personal/models`；令牌失效（401/403）不再换路径重试——
那说明凭据有问题，换路径也一样，只会把一次明确的鉴权错误拖成两次往返。

> 教训：**不要拿另一个站点的模型名去"探测"本站点的目录**。清单要么来自实时接口，
> 要么来自官方客户端源码，不要靠猜。

#### 9.3.2 两个站点的模型目录不重合

国际版（`www.workbuddy.ai`，真实账号实测，共 18 个）：

| ID | 名称 | credits |
|---|---|---|
| `default-model` | Auto（默认） | x0.79 |
| `fast-model` | Fast | x0.34 |
| `balanced-model` | Balanced | x0.59 |
| `primary-model` | Primary | x3.31 |
| `deep-model` | Deep | x3.33 |
| `hy4-preview` | Hy4 preview | x0.00 |
| `hy3` | Hy3 | x0.00 |
| `gpt-5.6-sol` | GPT-5.6-Sol | x3.47 |
| `gpt-5.6-terra` | GPT-5.6-Terra | x1.39 |
| `gpt-5.6-luna` | GPT-5.6-Luna | x0.14 |
| `gpt-5.5` | GPT-5.5 | x3.31 |
| `gpt-5.4` | GPT-5.4 | x1.65 |
| `gpt-5.3-codex` | GPT-5.3-Codex | x1.25 |
| `gemini-3.5-flash` | Gemini-3.5-Flash | x0.99 |
| `glm-5.3` | GLM-5.3 | x0.79 |
| `glm-5.2` | GLM-5.2 | x0.79 |
| `kimi-k3` | Kimi-K3 | x1.62 |
| `kimi-k2.6` | Kimi-K2.6 | x0.52 |

`default-model` / `fast-model` / `balanced-model` / `primary-model` / `deep-model` 是
**路由别名**（服务端按档位选真实模型），不是具体厂商模型。

与大陆站（`data.agents[cli].models`，15 个：`auto` `hy4-preview` `hy3` `hy3-x`
`deepseek-v4.1-flash` `deepseek-v4-pro` `glm-5.3` `glm-5.3-flash` `glm-5.2` `glm-5.1`
`glm-5v-turbo` `kimi-k3-1` `kimi-k2.7` `kimi-k2.6` `minimax-m3`）**不重合**：
`hy3-x` / `deepseek-v4-pro` / `glm-5.3-flash` / `kimi-k3-1` 只有大陆有，
GPT 与 Gemini 系列只有国际有。**用错站点的模型名会拿到
`11102 model [x] service info not found`**。

> 注意：目录**随账号/套餐/时间变化**（促销与可用模型由服务端下发），所以静态兜底
> 只是快照，运行时永远以实时目录为准。

#### 9.3.3 频率限制是 `code 6004`，不是鉴权失败

```json
{"code":6004,"msg":"usage exceeds frequency limit, ... your usage will reset at <时间>, alternatively, you can switch to the other models to continue using it."}
```

额度触顶时上游返回 **HTTP 429 + code 6004**，并给出重置时间。Sub2API 会把它当作普通
429 做故障转移，最终可能表现为 `no available accounts supporting model`（池被排除空）
——这**不是**域名或令牌错误。换一个模型即可继续。

另注：**可调用集合可能大于目录列表**。例如 `deepseek-v4.1-flash` 不在上述 18 项里，
但直接调用是成功的（返回 6004 限流而非 11102 不存在）。

#### 9.3.4 `/v3/config` 要求 `CodeBuddy/<version>` 形式的 UA

`GET /v3/config` 在 UA 不含 `CodeBuddy/<版本>` 时返回 `12403 check ua, get coding
copilot version error`；换成 `CodeBuddy/1.0.0` 即 200。注意该接口**不返回模型列表**
（`data.models` 为 `null`，只有 `enterpriseId` / `productFeatures*`），因此不能用它
替代目录接口。当前客户端固定使用 `codebuddy2openai/2.0`（参考实现已验证可用），
不受此约束影响。

### 9.4 仍未验证的部分（如实记录）

- **国际版的图片 / `reasoning_effort` 能力**：未逐项复测，第 1–8 节结论只对大陆
  CodeBuddy 有实测依据。国际站目录里 `data.models[].supportsImages` 全为 `true`，
  但那是上游声明，未实测。
- **`codebuddy × global`（www.codebuddy.ai）**：没有该站点的账号，未验证其目录接口
  是否可用；新路径在两站点都返回 401（路由存在），大概率可用但未证实。
- **`gpt-6-astra`**：用户客户端截图里出现过，但当前账号的目录与可调用集合里都没有，
  未确认它对哪些套餐开放。

### 9.5 自查命令

```bash
# 站点是否活着（未带凭据应为 200 + data.models 可能为 null）
for host in copilot.tencent.com www.codebuddy.ai www.workbuddy.cn www.workbuddy.ai; do
  printf '%-22s ' "$host"
  curl -s -o /dev/null -w '%{http_code}\n' "https://$host/v3/config"
done

# 授权链接是否落在指定站点（authUrl 的 host 应与请求 host 一致）
curl -sS -X POST 'https://www.workbuddy.ai/v2/plugin/auth/state?platform=CLI' | head -c 300

# 用账号令牌验证国际站模型是否可用（$AT / $UID 从账号凭据取）
#   200 / 6004 => 模型存在；11102 => 该站点没有这个模型
curl -sS -N https://www.workbuddy.ai/v2/chat/completions \
  -H "Authorization: Bearer $AT" -H "User-Agent: CodeBuddy/1.0.0" \
  -H "X-Domain: www.workbuddy.ai" -H "X-User-Id: $UID" \
  -H 'Content-Type: application/json' \
  -d '{"model":"deepseek-v3","stream":true,"messages":[
        {"role":"system","content":"You are helpful."},
        {"role":"user","content":"hi"}]}'
```

> ⚠️ 第一条消息必须是 `system`：否则上游返回
> `11128 first message is not system prompt`（大陆/国际一致）。

