# 项目结构

Vercel AI Gateway 多 key 轮询器 / 代理。Go 单进程，无外部依赖，状态存本地 JSON。

## 目录布局

```
.
├── main.go              入口：//go:embed html/*.html 后调用 app.Run(loginHTML, indexHTML)。`go:embed` 必须看到 html/，所以 main.go 必须在根
├── go.mod               module ai-gateway-poller, go 1.21
├── config.txt           运行时配置（必须存在，缺失则 fatal 启动失败）
├── main.exe             编译产物（Linux 上叫 main）
├── data/state.json      持久化：所有 key + 粘性模式 + 冷却阈值
├── build/               编译脚本
│   ├── build.bat        Windows: cd ..  &&  go build -o main_new.exe .（首三行 set GOROOT/GOPATH/PATH 是占位）
│   └── build.sh         Linux/macOS: cd ..  &&  go build -o main .
├── html/
│   ├── login.html       登录页
│   └── index.html       管理 UI（单文件 SPA，所有 JS/CSS 内联）
└── app/                 业务代码全部在 package app
    ├── app.go           Run() 入口、路由表
    ├── auth.go          requireAuth / handleLogin / handleLogout / checkAuthRequest / serveIndex
    ├── config.go        readConfig / loadConfigFile / getenvDefault
    ├── handlers.go      所有 /api/* 接口（state/refresh/sticky/keys/keysBulk/keysBatch/keysBatchTest/keyByID/logs/testKey）
    ├── proxy.go         handleGatewayProxy + 重试逻辑 + dumpSession/dumpReader/dumpWriter
    ├── sse.go           processAnthropicSSE / processOpenAISSE + token 估算 + ensureStreamUsage
    ├── state.go         loadState/save、nextProxyCandidates、markProxySuccess/Failure、pollOne、fetchCredits、publicView、roll30DayWindow、renumberKeys、activeKeyIDs
    ├── transform.go     body 改写：rewriteThinkingSuffix / transformReasoning / sanitizeForVercel / injectProviderOrder / rewriteImageEditsToGenerations / injectImageDefaults
    ├── types.go         所有类型 + 常量 + ProxyLogRing
    └── util.go          writeJSON / withCORS / logRequest / round2 / truncate / displayListenAddr
```

## 关键概念

**Key 状态机 / 三池**
- **活跃池**：`!Scrapped && !Paused` —— 参与轮询
- **冷却池**：`Paused && !Scrapped` —— 不参与轮询，但保留状态可恢复
- **报废池**：`Scrapped` —— 不参与轮询，需手动恢复或批量 haiku 测活恢复

状态流转：
- 活跃 → 月度消费 ≥ `MONTHLY_COOLDOWN_USD` 进冷却（Paused=true）
- 活跃/冷却 → 距上次代理调用满 30 天 → 自动解冻 + 月度账清零（`roll30DayWindow`）
- 活跃 → 收到 402 → 立即冷却；代理连续 3 次 401 → 报废；代理 403/400/429/5xx 会换 key 重试但不直接报废
- 冷却 → 重置月度（resetCost）→ 回活跃
- 报废 → 手动恢复 / haiku 测活通过 → 回活跃

`handleGetState` 响应里三池字段：`keys`（活跃）/ `cooldown_keys`（冷却）/ `scrap_keys`（报废），对应计数 `active_count` / `cooldown_count` / `scrap_count`。`total_balance` 等聚合包含活跃+冷却（不算报废），`active_balance` 只算活跃。

**Key 元数据字段**
- `name`（自定义名称）+ `tier`（账户等级如 team/hobby/pro）+ `api_key`。**没有 email**——AI Gateway 不暴露账号信息（试过 `/me /user /account /usage /info` 全 404），所以这些字段都是用户在导入时自己填的。
- 旧数据里的 `email` 字段被 JSON 反序列化时静默忽略，不需要迁移。
- 自动编号：`handleKeys`（单个新增）仅当请求 `name == "auto"` 时给 `name = maxNum+1` 的数字名；显式给名称/等级时按给的来。`handleKeysBulk` 按导入文本里给的名称走，空就空。前端单个新增的输入框也支持三列粘贴，只有纯 `vck_xxx`（无 name/tier）才触发自动编号。
- `renumberKeys` 只对**纯数字名称**和 `"报废N"` 名称重排，自定义名（非数字）和空名一概不动。这样混合导入不会被覆盖。

**批量导入格式**（`handleKeysBulk`）
每行格式：`[name] [tier] vck_xxx`，分隔符任意空白。`strings.Fields` 切 token，找到第一个 `vck_xxx` token，它前面的 token 依次当 name / tier（最多取两个）。一行有多个 `vck_` 时只第一个能带 name/tier，其余字段留空。纯 `vck_xxx` 一行也支持（name/tier 均空）。

**轮询 / 粘性**
- 轮询模式：`proxyTurn` 自增取模，只在当前可用池里轮转。默认优先等级由 `preferred_tier` 决定（默认 `team`，保持旧行为），命中 `team_priority_models` / `hobby_priority_models` 时覆盖默认优先等级；优先池没有可用 key 时 fallback 到另一池。
- 粘性模式：锁住 `StickyKeyID` 直到该 key 冷却/报废才换；其余候选作为 fallback 防打断
- `hobby_blocked_models` 存模型级 Hobby 限制规则，默认含 `anthropic/claude-opus-4.5`、`anthropic/claude-opus-4.6`、`anthropic/claude-opus-4.7`。匹配规则时会排除 `tier=hobby` 的活跃 key，仍可使用 team/pro 等非 Hobby key；设置页可添加/删除规则，支持每行一个模型或 `*` 结尾前缀。
- `hobby_blocked_models` 是硬限制，优先级高于 `hobby_priority_models`；同一模型同时命中 Hobby 不可用和 Hobby 优先时仍只使用 team。
- 重试状态码和最大尝试次数存 `state.json`：默认 `400,401,402,403,429,5xx`，最大尝试次数默认/上限 20（包含首次请求）。设置页可改成如 `429,401`，表示只对这些状态切换下一个 key。
- 冷却/报废策略存 `data/config.json`：代理命中 `cooldown_status_codes` 进入冷却；代理命中 `proxy_scrap_status_codes` 会累计失败，达到 `proxy_scrap_fail_threshold` 自动报废；刷新余额命中 `refresh_scrap_status_codes` 自动报废，命中 `refresh_cooldown_status_codes` 自动冷却。

**路径前缀决定 provider**
- `/v1/*` → 走 `PROVIDER_ORDER` 全局配置
- `/aws/v1/*` → 强锁 bedrock
- `/vertex/v1/*` → 强锁 vertex
- `/anthropic/v1/*` → 强锁 anthropic
- `/azure/v1/*` → 强锁 azure
- 路径前缀优先级 > `PROVIDER_ORDER`

**渠道路由实现**（`injectProviderOrder`）
Vercel 的渠道路由**不是**靠模型名前缀实现的。`vertex/claude-*` 在 Vercel 不存在（404），`bedrock/claude-*` 虽然不报错但 Vercel 会悄悄退到 anthropic。正确机制：
1. 模型名保持规范 namespace：`anthropic/claude-*` / `google/gemini-*` / `openai/gpt-*`。客户端传裸名时 `canonicalNamespace` 补前缀。
2. 渠道意图写到 body 的 `providerOptions.gateway.order` 数组（如 `["vertex"]` / `["bedrock","anthropic"]` / `["azure","openai"]`），Vercel 按数组顺序尝试，失败自动 fallback。
3. 对 `anthropic` / `google` namespace 直接注入 order；对 `openai` namespace 只保留 `azure` / `openai`，避免把 GPT 强锁到 bedrock/anthropic/vertex；未知 namespace 不动以免破坏自定义模型。
4. 响应里 `provider_metadata.gateway.routing.resolvedProvider` 能看到实际命中哪家（如 `vertexAnthropic`、`bedrock`、`anthropic`、`azure`）。

**Body 改写流水线**（顺序见 `proxy.go` handleGatewayProxy）
1. `rewriteImageEditsToGenerations` — `/v1/images/edits` → `/v1/images/generations`
2. `injectImageDefaults` — 图片接口默认 quality=high, size=3840x2160
3. `rewriteThinkingSuffix` — 模型名 `-{minimal,low,medium,high,xhigh}` / `-thinking-*` 后缀剥离 + 注入 thinking/reasoning
4. `transformReasoning` — `reasoning_effort` / 默认 reasoning → OpenAI reasoning 或 Anthropic thinking
5. `ensureStreamUsage` — OpenAI 流补 `stream_options.include_usage=true`
6. `sanitizeForVercel` — 剥掉 0/负值的 top_k/top_p/max_tokens/n
7. `injectProviderOrder` — 给无 provider 前缀的 model 强加 `bedrock/`、`vertex/` 等

**Reasoning / Thinking 注入**
- `DEFAULT_REASONING_EFFORT` 可选值：`minimal` / `low` / `medium` / `high` / `xhigh`。
- OpenAI 兼容接口 `/v1/chat/completions` 注入 `reasoning: {enabled:true, effort:"..."}`。
- Anthropic 接口 `/v1/messages` 跑 GPT/OpenAI 模型时注入 `providerOptions.openai.reasoningEffort` 和 `reasoningSummary:"detailed"`。
- Anthropic 接口 `/v1/messages` 跑 Claude/Anthropic 模型时注入 `providerOptions.anthropic.thinking: {type:"enabled", budgetTokens:...}`，并删除 `temperature` / `top_p` / `top_k`。
- Claude thinking 预算映射：`minimal=1024`、`low=2048`、`medium=4000`、`high=8000`、`xhigh=16000`。
- 模型名后缀可覆盖默认值，例如 `gpt-5.5-xhigh`、`claude-opus-4.6-high`、`gemini-3-pro-medium`。旧后缀 `-thinking-low/-thinking-mid/-thinking-high/-thinking-max` 保持兼容。

**SSE 防打断**
- Anthropic 流：解析 `message_delta` 累计 output_tokens；EOF / 客户端断开时若没看到真实 usage，补发合成 `message_delta` + `message_stop`
- OpenAI 流：解析 delta 累计 completion_tokens；EOF / 断开时补发带 usage 的 chunk + `[DONE]`

**Dump 调试**
- Debug 默认关闭，开启后目录固定为当前反代工作目录下的 `debug` 子目录。每个 /v1/* 请求落 5 个文件：`*_request_meta.json` `*_request_body.json` `*_upstream.txt`（Vercel 原始 SSE）`*_downstream.txt`（发给客户端的）`*_meta.json`（状态码/耗时/是否断开）。前端 Debug 页可开关、按文件名 / 请求 ID / 内容关键字筛选，并支持查看、下载、删除、清空。

## 路由表

| 路径 | 方法 | 处理函数 |
|---|---|---|
| `/` | GET | serveIndex（未登录 → loginHTML，已登录 → indexHTML） |
| `/api/login` | POST | handleLogin（cookie auth_token, 30 天） |
| `/api/logout` | POST | handleLogout |
| `/api/state` | GET | handleGetState |
| `/api/settings` | GET/POST | handleSettings（保存重试状态码、最大尝试次数、Hobby 不可用模型规则、key 等级优先级规则） |
| `/api/retry-settings` | GET/POST | handleRetrySettings（兼容旧入口，实际复用 handleSettings） |
| `/api/config` | GET/POST | handleConfig（保存运行配置到 `data/config.json`） |
| `/api/debug` | GET/DELETE | handleDebugFiles（列出 / 清空 `./debug`，GET 支持 `q` 关键字） |
| `/api/debug/settings` | GET/POST | handleDebugSettings（读取 / 保存 Debug Dump 开关） |
| `/api/debug/file` | GET/DELETE | handleDebugFile（按 `name` 查看、下载或删除单个 debug 文件） |
| `/api/export/keys` | GET | handleExportKeys（导出 `名称<Tab>等级<Tab>密钥`） |
| `/api/refresh` | POST | handleRefresh（id 留空 → 并发刷新全部活跃 key，sem=10） |
| `/api/sticky` | POST | handleStickyToggle |
| `/api/sticky/select` | POST | handleStickySelect |
| `/api/logs` | GET | handleLogs（环形缓冲 200 条） |
| `/api/keys` | POST | handleKeys（新建单个） |
| `/api/keys/bulk` | POST | handleKeysBulk（粘贴文本，正则抓 `vck_*`） |
| `/api/keys/batch` | POST | handleKeysBatch（action: delete/scrap/restore） |
| `/api/keys/batch/test` | POST | handleKeysBatchTest（haiku 测活 + 自动恢复） |
| `/api/keys/{id}` | GET/PATCH/DELETE | handleKeyByID |
| `/api/test/{id}` | POST | handleTestKey（haiku 测活，单个） |
| `/api/test/{id}/models` | GET | handleTestKey（拉 /models 列表） |
| `/v1/*` `/aws/v1/*` `/vertex/v1/*` `/anthropic/v1/*` `/azure/v1/*` | * | handleGatewayProxy |

## 认证

拆成 Web 登录 token 和 API 调用 token：
- `WEB_AUTH_TOKEN` —— 守 `/`（登录页/SPA）、`/api/login`、`/api/*`
- `API_AUTH_TOKEN` / `api_auth_tokens` —— 守 `/v1/*`、`/aws/v1/*`、`/vertex/v1/*`、`/anthropic/v1/*`、`/azure/v1/*`，运行配置支持多个 API token；可为空，空时代理接口 fail-closed 返回 401，用户可进控制台后生成。

`app.go` 里用动态中间件从 `RuntimeConfig` 读取当前 token，配置页保存后立即生效。`checkAuthRequest` 接受三种来源（优先级）：
1. Cookie `auth_token`（Web UI 登录后下发，仅 web token 用得到）
2. `Authorization: Bearer <token>`
3. `X-Auth-Token` / `X-Api-Key` header

`config.txt` 只作为首次启动/兜底种子。首次启动会从 `config.txt` 读取并生成 `data/config.json`；之后配置页写 `data/config.json`，即使 `config.txt` 不存在也可用已有持久化配置启动。若两者都不存在，或 Web token 为空，启动仍 fail-closed。API token 可为空，但代理接口不会放行。

## Web UI 约定

`html/index.html` 是单文件 SPA，CSS/JS 全部内联，由 `//go:embed html/index.html` 在编译期嵌入 main.go。改完要 `go build` 重启才生效。

**主题色**：CSS 变量定义在 `:root`，`--green/amber/red` + 各自 `*-soft` 对应深浅两套（pill/button 用）。换主题改这一组就行。

**布局**：顶部 metric 卡片 → 工具栏 → 顶部横向六 Tab 容器（活跃/冷却/报废/日志/设置/配置）。设置和配置紧跟日志，不占左侧空间。Tab 切换通过 `switchTab(name)`，名字对应 `panel-active / panel-cooldown / panel-scrap / panel-logs / panel-settings / panel-config`；切到 logs 自动 `loadLogs()`，切到 config 自动 `loadConfig()`。

**分页**：三池都做客户端分页（日志不需要，已是固定环形 200 条）。状态 `_page = {active:1, cooldown:1, scrap:1}` + 全局 `_pageSize`（默认 20，选项 10/20/50/100/全部）。`paginate(list, which)` 返回切片 + total + totalPages + current 并 clamp 越界 page。`pagerHtml(which, p)` 渲染首页/上一页/N/N/下一页/末页/每页选择器；总数为 0 时不渲染。`goPage(which, n)` 改完 `_page` 后 `rerenderPool(which)` 用 `_stateCache` 即时刷新，不重新拉接口。`setPageSize` 同时把三池都重置回第 1 页。

**筛选**：三池都用表头按钮弹出筛选（WPS/表格风格），字段为名称、等级、距下次重置时间（24 小时内 / 7 天内 / 已到期）。日志页用 `_logCache` 缓存最近日志，支持按接口 / provider / 模型 / Key / token / 错误文本搜索，以及成功 / 失败 / 发生重试状态筛选。Debug 页按文件名、请求 ID 和文件内容关键字筛选。

**设置 Tab**：收纳重试状态码、最大尝试次数、Hobby 不可用模型规则、默认 key 等级优先级、模型级 team/hobby 优先规则。模型规则每行一个模型或前缀，保存到 `state.json` 的 `hobby_blocked_models` / `team_priority_models` / `hobby_priority_models`；默认优先等级保存到 `preferred_tier`。

**配置 Tab**：收纳运行配置，保存到 `data/config.json`：Web 登录密码、多 API 调用密钥（支持随机生成 `sk-` + 48 位字母数字）、默认渠道 `provider_order`、默认 reasoning、Gateway 地址、月度冷却阈值、单 Key 月额度、代理/刷新冷却和报废策略、passthrough、监听地址。Debug dump 从配置页移到 Debug 页管理。除监听地址需要重启外，其余保存后按新配置即时生效。

**交互组件**（一律是函数+全局状态，不引框架）：
- `toast(msg, type='info'|'success'|'warn'|'error', ms=2800)` — 右上角，自动消失
- `confirmModal(msg, title, okClass)` — 返回 Promise<bool>，替代 `confirm()`
- `infoModal(title, body)` — 等宽展示，替代 `alert()` 长结果
- `openOverlay(id)` / `closeOverlay(id)` — 通用 modal 开关
- `moreMenu([{label, fn, cls}])` — 行操作的「更多 ▾」下拉，外层点击自动关
- 永远不要再用原生 `alert/confirm/prompt`，难看且阻塞 UI

**行操作模板**：每个池行操作列只露 2 个最常用按钮 + 「更多 ▾」。要加新动作就丢进 `moreMenu([...])` 数组，删除类标 `cls:'danger'`。

**状态拉取**：`loadState()` 拉 `/api/state` 全量重渲三池；`setInterval(loadState, 30000)` 每 30s 自动刷新。选中状态用 `_sel.active / _sel.scrap` 两个 Set 跨重渲保持。

**Key 显示/隐藏**：默认只渲「显示」按钮；点了之后调 `/api/keys/{id}` 拿明文，存 `_revealedKeys[id]`，再次 loadState 时根据 `_revealed` Set 决定是否显形。

**编辑 modal**：`editKey(id)` 拉详情填表 → `saveEdit()` PATCH。API Key 字段留空 = 不修改。

## 编译运行

`go build` 会把所有 Go 源码 + `//go:embed` 嵌入的 HTML 打成一个**静态二进制**，落在项目根目录，~10MB，无外部依赖（Go 标准库静态链接）。这个二进制本身就是启动器，没有额外的 launcher / 守护进程 / runtime。

```sh
# 直接编译
go build -o main.exe .     # Windows  → 产物：main.exe
go build -o main .         # Linux/macOS → 产物：main

# 或用脚本
build\build.bat                                # Windows  → 产物：main_new.exe（避免覆盖运行中的 main.exe）
chmod +x build/build.sh && build/build.sh      # Linux/macOS 首次需加执行位，产物：main

# 运行
./main.exe                 # Windows
./main                     # Linux/macOS
```

**首次启动行为**（无论平台）：
- 当前工作目录下没有 `config.txt` → `log.Fatalf` 退出（不再自动生成）
- 首次启动没有 `data/config.json` 时，`config.txt` 里的 `WEB_AUTH_TOKEN` 留空 → `log.Fatalf` 退出；`API_AUTH_TOKEN` 可留空，之后在配置页生成/填写
- `data/` 目录不存在 → 自动创建
- `data/state.json` 不存在 → 写入空状态
- 监听日志：`AI Gateway poller listening on http://localhost:8211`

**注意**：工作目录敏感，二进制要从项目根目录运行（或者 `cd` 进去再跑），否则它找不到 `config.txt` 直接 fatal。systemd 配 `WorkingDirectory=` 锁住。

**跨平台编译**（在 Windows 上打 Linux 二进制 / 反之）：
```sh
$env:GOOS="linux"; $env:GOARCH="amd64"; go build -o main .   # PowerShell
GOOS=linux GOARCH=amd64 go build -o main .                   # bash
```

监听地址在 `config.txt` 的 `LISTEN_ADDR` 改。启动日志显示的 URL 由 `displayListenAddr` 拼，`:port` 自动加 `localhost`，写全 `host:port` 时按原样显示。

## 常见改动入口

| 想改什么 | 改哪里 |
|---|---|
| 新增配置项 | `types.go` Config + `config.go` readConfig |
| 加新 /api 接口 | `handlers.go` 写函数 + `app.go` mux.HandleFunc |
| 改 body 改写规则 | `transform.go` |
| 改重试/报废策略 | `state.go` `markProxyFailure` + `proxy.go` `shouldRetryWithNextKey` |
| 改 SSE 解析 | `sse.go` |
| 改 UI 主题色 | `html/index.html` `:root` CSS 变量 |
| 加行操作 | `html/index.html` 对应 `renderActive/Cooldown/Scrap` 里的 `moreMenu([...])` |
| 改弹窗交互 | `html/index.html` `toast / confirmModal / infoModal` 三个工具函数 |
| 改登录页 | `html/login.html` |
| 加新 metric 卡片 | `html/index.html` `renderMetrics(s)` |
| 改月度额度滚动逻辑 | `state.go` `roll30DayWindow` |

## 已知 dead code

- `state.go` `(*AppState).keyIDs()` 当前没人调用，refactor 时保留未删
