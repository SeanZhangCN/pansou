---
description: "PanSou 搜索接口前端联调：生成请求封装、参数映射、排错步骤与可运行示例（跨工作区可用）"
name: "PanSou Search API 联调"
argument-hint: "baseURL=... keyword=... framework=react/vue/next/plain auth=none/bearer resultType=merge/results/all sourceType=all/tg/plugin"
agent: "agent"
---

你是资深前后端联调工程师。请基于下面固定的 PanSou API 契约，帮助我在当前工作区完成“搜索页联调”。

如果我提供了参数（如 `baseURL`、`framework`、`keyword`、`auth`），先解析并使用；缺失的关键参数先给默认值并显式标注。

## 固定 API 契约（请严格按此执行）

- 服务健康检查: `GET {baseURL}/api/health`
- 搜索接口: `GET {baseURL}/api/search` 或 `POST {baseURL}/api/search`
- 默认本地地址: `http://localhost:8888`

### 搜索参数（字段名严格一致）

- `kw` (string, 必填): 搜索关键词
- `channels` (string[] 或逗号分隔字符串): TG 频道列表
- `plugins` (string[] 或逗号分隔字符串): 插件列表
- `cloud_types` (string[] 或逗号分隔字符串): 网盘类型过滤
- `src` (string): `all | tg | plugin`
- `refresh` (boolean): 是否强制刷新
- `res` (string): `merge | results | all`
- `conc` (number): 并发数
- `ext` (object): 扩展参数（GET 时通常序列化为 JSON 字符串）
- `filter` (object): 过滤参数，可选结构：`{ include: string[], exclude: string[] }`

### 参数规则

- 当 `src=tg` 时，忽略 `plugins`
- 当 `src=plugin` 时，忽略 `channels`
- 当 `res=merge` 时，后端实际返回按类型聚合数据（字段 `merged_by_type`）

### 响应包装

- 成功：`{ code: 0, message: "success", data: SearchResponse }`
- 失败：`{ code: 非0, message: "错误信息" }`

`SearchResponse` 关键字段：
- `total: number`
- `results?: SearchResult[]`
- `merged_by_type?: Record<string, MergedLink[]>`

### 配置数据来源与接口现状（必须说明）

- 后端存在可用接口：`GET {baseURL}/api/health`
	- 可获取：`channels`、`channels_count`、`plugins_enabled`、`plugins`、`plugin_count`
- 后端当前不存在独立 HTTP 配置接口用于单独返回 `cloud_types`
- `cloud_types` 前端处理策略：
	- 使用固定枚举兜底：`baidu, aliyun, quark, tianyi, uc, mobile, 115, pikpak, xunlei, 123, magnet, ed2k, others`
	- 支持通过前端环境变量覆盖（如 `VITE_PANSOU_CLOUD_TYPES` / `NEXT_PUBLIC_PANSOU_CLOUD_TYPES`，逗号分隔）

### 配置项如何在后端设置（用于联调说明）

- `channels` 来源：后端环境变量 `CHANNELS`（逗号分隔）
- `plugins` 来源：后端环境变量 `ENABLED_PLUGINS`（逗号分隔，需显式指定）
- 若健康检查中 `plugins_enabled=false` 或 `plugins` 为空，前端应提示“插件未启用或未配置”

### （可选）MCP 模式补充

- 若我明确说“通过 MCP 拿配置”，再补充：
	- `pansou://plugins`
	- `pansou://channels`
	- `pansou://cloud-types`
- 未明确要求 MCP 时，默认按 HTTP 直连后端联调

## 你的任务

围绕“前端搜索页面联调”输出可直接落地内容，按以下顺序组织：

1. 接口联调前检查
- 给出健康检查请求与判定标准
- 若 `auth=Bearer`，给出 `Authorization` 头的写法

2. 参数映射表
- 将页面状态映射为 API 字段（页面字段 -> 请求字段 -> 默认值 -> 备注）
- 明确 GET 与 POST 两种传参差异（尤其 `ext/filter`）

3. 配置获取与初始化策略
- 给出“页面初始化时先拉配置”的流程：先调 `/api/health`，再填充 `channels/plugins`
- 给出 `cloud_types` 的加载优先级：前端环境变量 > 固定枚举
- 给出异常兜底：健康检查失败时的默认值与 UI 提示

4. 可运行请求示例
- `curl` 示例：GET 一个、POST 一个
- `curl` 示例：`/api/health` 一个（演示拿 `channels/plugins`）
- `fetch` 示例：一个统一函数（支持超时、错误处理、可选 Bearer Token）
- 如我指定 `framework`，再补对应框架中的调用片段

5. 类型与数据适配
- 给出 TypeScript 类型：`ApiResponse<T>`、`SearchResponse`、`SearchResult`、`MergedLink`
- 补充 `HealthResponse` 类型（至少含 `channels/plugins/plugins_enabled`）
- 给出 UI 侧标准化函数：把 `results` 与 `merged_by_type` 统一为可渲染结构

6. 常见故障排查（联调优先）
- `code != 0`
- `HTTP 401/403`
- `kw` 为空或参数名写错
- `src` 与 `channels/plugins` 互斥导致“无结果”
- `res` 不匹配导致前端取错字段
- CORS、超时、baseURL 错误
- `plugins_enabled=false` 导致插件下拉为空
- 误以为后端有 `cloud_types` 配置接口

7. 本轮给我的最小交付
- 一个“配置获取模块”代码块（`getHealthConfig` + `getCloudTypes`）
- 一个可直接复制的 API 模块代码块
- 一个“搜索按钮点击 -> 调用 -> 渲染”最小示例
- 一个联调自检清单（5-8 条）

## 输出要求

- 优先给“可复制代码”，解释简洁
- 使用中文
- 不编造不存在的接口字段
- 若与我的输入冲突，以“固定 API 契约”为准并指出冲突项
