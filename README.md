# AI 模拟面试 Agent

基于 Go + [cloudwego/eino](https://github.com/cloudwego/eino) 的文本模拟面试 Agent：候选人提交简历与目标岗位 JD，系统多轮提问、追问、逐题评估，最后生成可追溯的复盘报告。

> 当前阶段：**M0、M1（真实存储 + 会话状态机）、M2（作答链路 + 结构化评估 + 规则裁决）、M3（真实模型 + 题纲出题 + compose.Graph）、M4（收尾与报告）、M5（Redis 会话锁 + LISTEN/NOTIFY 事件推送）、M6（评分回归集 + 批量跑分）、M7（可观测性与成本口径）、M8（最小可视化演示页）已完成**。
> PostgreSQL 是唯一运行期事实源；Redis 只做跨进程会话锁，内存实现只作为单元测试替身。
> 完整设计见 [docs/development-plan.md](docs/development-plan.md)，关键取舍见 [docs/design-rationale.md](docs/design-rationale.md)。
> 开发期以对话式推进，提交记录为近期按里程碑整理，不代表逐日迭代历史。

## 当前能做什么

- 创建会话、查询会话、启动面试（`POST /start` 返回 `202 + run_id`）、提交答案（`POST /answers` 返回 `202 + run_id`）、中止会话。
- 答案写入 `turns`，状态机推进到追问 / 下一题 / 收尾；题纲、回合与评估均落 PostgreSQL。
- 领域包可插拔：会话创建时传 `domain`（默认 `go-backend`），题纲、评分维度与报告维度都来自该包；技术三包完整，非技术岗位用示例包 + 文档。
- 结构化评估：维度分、证据引用、加减分、`missing_points`、`followup` 建议；非法 JSON 自动修复 2 次，仍失败则降级继续面试并记录事件。
- 规则裁决：硬预算门（超时/题纲/追问总数/深度/单题追问/不会回答/评估降级）→ 覆盖门 → 模型建议 → 终裁，追问条数有上限。
- 收尾与报告：`POST /finish`（或对已收尾会话 `POST /report`）→ `reporting` → 只读工具回查（`get_session_overview` / `search_turns` / `list_weak_turns` / `get_turn`）→ 模型写点评 → 结构化 JSON + Markdown 同事务落库 → `completed`；`GET /report` 取报告。
- 报告幂等：同一 session 只生成一份报告，重复 finish 返回已有结果（`Idempotent-Replayed: true`），不重复调用模型；模型不可用时生成确定性降级报告，不中断整场。
- 所有状态提交、run/step 日志、SSE 事件落 PostgreSQL；`Last-Event-ID` / `?after=` 续播。
- 事件推送走 PostgreSQL `LISTEN/NOTIFY`：事件与 `pg_notify` 同事务提交，SSE 连接只被唤醒后回查事件表，另有 15s 心跳；断线重连按 `Last-Event-ID` 续播，不丢不重。
- 跨实例会话锁：Redis `SET NX PX` + token 比较释放，锁有 TTL；抢锁失败返回可重试的 `409 lock_busy`，不会静默丢弃命令。Redis 不可达时启动日志告警并回退进程内锁（仅单实例正确）。
- 幂等键防重复执行：同一 `Idempotency-Key` 只产生一个 run；重复作答返回原 run 并带 `Idempotent-Replayed: true`。
- 启动时识别崩溃遗留的 `running` run，标记 `interrupted` 并从最后成功节点续跑。

可视化演示页已提供：浏览器打开 `http://localhost:8080/demo` 即可完成一场面试，无需 curl。真实模型已接通 eino-ext OpenAI 兼容实现，配置 `OPENAI_API_KEY` 即可联调；未配置时自动回退确定性 `fake-heuristic`，演示与单测不依赖 key。

## 环境要求

- Go 1.25+（以 `go.mod` 为准）
- Docker / docker-compose：PostgreSQL 与 Redis 的正式本地依赖（Redis 用于跨进程会话锁）

## 快速开始

先启动 PostgreSQL 与 Redis：

```powershell
docker compose -f deploy/docker-compose.yml up -d
docker compose -f deploy/docker-compose.yml ps
```

再启动服务：

```powershell
go build ./...
go test ./...
go run ./cmd/server
```

服务启动时会自动执行 `migrations/` 下的 SQL 迁移，并完成一次中断 run 的 reconciliation。

另开一个终端（PowerShell）：

```powershell
# 1. 创建会话（PowerShell 5.1 内联 -d 传中文会按 GBK 发送，先用 UTF-8 文件落盘）
$json = '{"candidate_name":"张三","target_role":"Go 后端开发","resume_text":"三年 Go 经验","jd_text":"熟悉分布式系统"}'
[System.IO.File]::WriteAllText("$PWD\session.json", $json, (New-Object System.Text.UTF8Encoding($false)))

curl.exe -i -X POST http://localhost:8080/v1/sessions `
  -H "Content-Type: application/json; charset=utf-8" `
  --data-binary "@session.json"

# 2. 启动面试（记下返回的 run_id）
curl.exe -i -X POST http://localhost:8080/v1/sessions/<session_id>/start `
  -H "Idempotency-Key: start-demo-1"

# 3. 查询会话，取 pending_turn_id
curl.exe -s http://localhost:8080/v1/sessions/<session_id>

# 4. 提交答案（turn_id 来自上一步；中文同样建议先写 UTF-8 文件）
$answer = '{"turn_id":"<turn_id>","answer":"我在订单服务里用本地消息表加定时补偿保证最终一致性，压测峰值 1.2 万 TPS。"}'
[System.IO.File]::WriteAllText("$PWD\answer.json", $answer, (New-Object System.Text.UTF8Encoding($false)))
curl.exe -i -X POST http://localhost:8080/v1/sessions/<session_id>/answers `
  -H "Content-Type: application/json; charset=utf-8" `
  -H "Idempotency-Key: answer-demo-1" `
  --data-binary "@answer.json"

# 5. 收尾并生成报告（也可用 POST /report；重复调用幂等）
curl.exe -i -X POST http://localhost:8080/v1/sessions/<session_id>/finish `
  -H "Idempotency-Key: finish-demo-1"

# 6. 取报告（structured JSON + markdown）
curl.exe -s http://localhost:8080/v1/sessions/<session_id>/report

# 7. 订阅事件（Ctrl+C 退出）
curl.exe -N http://localhost:8080/v1/sessions/<session_id>/events

# 8. 抓取指标（Prometheus 文本格式）
curl.exe -s http://localhost:8080/metrics
```

预期事件顺序：`session.created` → `run.started` → `session.started` → `question.asked` → `session.phase`。
作答后追加：`run.started` → `answer.received` → `evaluation.completed`（或 `evaluation.degraded`）→ `decision.made` → `question.asked`（追问/下一题）或 `session.phase`（收尾）。
收尾后追加：`session.phase(reporting)` → `report.started` → `report.section` ×5 → `report.tool_calls` → `report.completed` → `session.phase(completed)` → `session.completed`。
重复执行第 2 / 4 / 5 步（同一个 `Idempotency-Key`）会返回同一个 `run_id`，并带 `Idempotent-Replayed: true`；收尾对不同 key 也只会返回已生成的报告，不重复调用模型。

## 可视化演示页（M8）

页面不引入前端工程、没有 npm 构建：`index.html` / `app.js` / `style.css` 通过 `//go:embed` 打进二进制，源码在 `internal/api/http/demo/`，由 `GET /demo` 与 `GET /demo/` 直接提供。

### 启动

```powershell
docker compose -f deploy/docker-compose.yml up -d   # PostgreSQL 必需；Redis 可选
go run ./cmd/server                                 # 不配置 OPENAI_API_KEY 时自动使用 heuristic
```

浏览器打开 <http://localhost:8080/demo>。

### 演示脚本（三步走）

1. 填写「目标岗位」与「职位描述 JD」（简历与称呼可选），主问题数选 3、每题目追问上限选 1，点「创建会话并开始」。首题通过 SSE 实时推送，右上角应显示「SSE 已连接」。
2. 在输入框作答并点「提交作答」。几秒内依次出现：你的回答 → 评分（总分/等级）→ 领域包维度得分条 → 缺失要点 → 评估决策 → 下一题或追问。重复作答直到题纲完成。
3. 点「结束面试并生成报告」，等待报告区显示「已生成」，即可看到综合得分、等级、模型调用/Token，以及五段报告（总评 / 分维度表现 / 逐题点评 / 最需要补的 3 个点 / 下一步练习建议）。

错误与降级不会静默：锁冲突 `409` 会在页面提示并自动重试；评估降级会标注该题不计分、流程继续；报告降级会在报告区显示原因；run 失败会提供「重试本轮」。断线重连由 `EventSource` 携带 `Last-Event-ID` 续播，页面再按 `event_id` 去重，不会重复渲染。

## 评分回归跑分

```powershell
# 无 key：heuristic 基线（30 条回归集，结果写到 docs/eval-results/）
go run ./cmd/evalrunner -provider heuristic

# 有 key：同一条命令走真实 provider（也可省略 -provider，让配置自动选择）
$env:OPENAI_API_KEY="..."
go run ./cmd/evalrunner -provider openai

# go test 子集：硬校验 schema 与证据复核，打印逐条明细
go test -tags=eval -count=1 -v ./internal/agent/eval/...
```

结果文件含时间戳、provider、模型名、prompt 版本、`dataset_sha256`、指标与逐条明细；档位门槛不达标时退出码为 2。

## 本地依赖（PostgreSQL + Redis）

```powershell
docker compose -f deploy/docker-compose.yml up -d
docker compose -f deploy/docker-compose.yml ps
```

为避免和本机已有服务冲突，端口映射为：

| 服务 | 容器端口 | 宿主机端口 | 连接串 |
| --- | --- | --- | --- |
| PostgreSQL | 5432 | **5433** | `postgres://interview:interview@localhost:5433/interview?sslmode=disable` |
| Redis | 6379 | **6380** | `localhost:6380` |

## 配置

当前只读取环境变量，不自动加载 `.env` 文件（`.env.example` 仅作参考）：

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `HTTP_ADDR` | `:8080` | HTTP 监听地址 |
| `DATABASE_URL` | `postgres://interview:interview@localhost:5433/interview?sslmode=disable` | 唯一运行期存储，必填 |
| `REDIS_ADDR` | `localhost:6380` | 会话锁使用的 Redis 地址；不可达时回退进程内锁（单实例） |
| `REDIS_PASSWORD` | 空 | Redis 密码 |
| `REDIS_DB` | `0` | Redis 逻辑库 |
| `LOCK_TTL` | `10s` | 会话锁过期时间，防止崩溃实例永久占锁 |
| `LOCK_ACQUIRE_TIMEOUT` | `3s` | 抢锁最长等待；超时返回 `409 lock_busy`（可重试） |
| `RUN_STALE_AFTER` | `60s` | run 超过该时间未更新视为崩溃遗留 |
| `MODEL_PROVIDER` | 见说明 | `heuristic` 或 `openai`；留空时有 `OPENAI_API_KEY` 则选 openai，否则 heuristic |
| `OPENAI_BASE_URL` | 空（官方默认） | 任意 OpenAI 兼容服务的 base URL |
| `OPENAI_API_KEY` | 空 | 为空时真实 provider 自动降级为 heuristic，启动日志 Warn |
| `OPENAI_MODEL` | `gpt-4o-mini` | 模型名 |
| `MODEL_TIMEOUT` | `30s` | 单次模型调用超时（duration 格式） |
| `MODEL_MAX_RETRIES` | `2` | 可重试失败的最大重试次数（0..5） |

PowerShell 示例：

```powershell
$env:HTTP_ADDR = ":9090"
go run ./cmd/server
```

## 目录职责

| 目录 | 职责 |
| --- | --- |
| `cmd/server/` | 进程入口：配置加载、依赖组装、迁移执行、HTTP 启动、优雅退出 |
| `internal/api/http/` | 路由、DTO、参数校验、错误映射、request_id 中间件 |
| `internal/api/http/demo/` | M8 演示页静态资源（`index.html` / `app.js` / `style.css`，由 `//go:embed` 内嵌，无构建链） |
| `internal/app/` | 用例编排：事务、幂等、会话锁、状态提交、事件发布、run 执行与恢复 |
| `internal/agent/interview/` | 作答链路 `compose.Graph`（snapshot → evaluate → decide → ask 分支）、评估器、出题器、heuristic 假模型（M2/M3） |
| `internal/agent/llm/` | eino `BaseChatModel` 适配为 `llm.Client`；provider 装配（env）、超时/重试/token 计数/失败分类；`FakeChatModel` / 脚本模型 |
| `internal/agent/model/` | 真实 ChatModel provider 预留目录（未启用） |
| `internal/agent/report/` | 报告生成：一次模型写点评 + 确定性合并分数与结构 + 降级报告（M4） |
| `internal/agent/tools/` | 报告阶段只读会话回查：归属校验、参数校验、超时、结果上限、调用预算（M4） |
| `internal/agent/prompt/` | 评估 / 出题 / 报告 prompt、修复消息、prompt 版本（M2/M3/M4） |
| `internal/domain/session/` | 会话实体、状态与阶段、迁移规则、turn/plan/run 类型 |
| `internal/domain/evaluation/` | Rubric 输出结构、计分、证据校验与持久化记录（M2） |
| `internal/domain/report/` | 报告 JSON 契约、分数段、确定性 5 段渲染（M4） |
| `internal/domain/followup/` | 判定门顺序、预算上限、不会回答识别（M2） |
| `internal/domain/domainpack/` | 领域包加载、manifest 校验、方向识别与选题计划（M14） |
| `internal/domain/syllabus/` | 旧题纲 API 的兼容层，转发到内嵌的 `go-backend` 包（M14） |
| `domainpacks/` | 内嵌领域包：`go-backend` / `java-backend` / `ai-agent`，各含 rubric、考点与词表 |
| `internal/domain/resume/` | 结构化简历画像（年限 / 技能 / 项目）与文本解析（M3） |
| `internal/ports/` | 仓储 / 模型 / 事件 / 锁接口与事务边界，由内向外定义 |
| `internal/platform/lock/` | 进程内锁替身（仅单测/单实例降级） |
| `internal/store/postgres/` | PostgreSQL 仓储实现（M1，唯一运行期存储）+ `LISTEN/NOTIFY` 事件监听与订阅分发（M5） |
| `internal/store/redis/` | Redis 会话锁（M5） |
| `internal/store/memory/` | **测试替身**：仅单测注入，不参与运行期装配；含事件订阅替身 |
| `internal/transport/sse/` | SSE 帧写出、心跳、断线续播辅助 |
| `internal/platform/config/` | 环境变量配置 |
| `migrations/` | SQL 迁移（启动时自动执行） |
| `deploy/docker-compose.yml` | PostgreSQL + Redis 本地依赖 |
| `docs/development-plan.md` | 架构、状态机、协议、评测方案与里程碑的单一事实来源 |
| `api/` | OpenAPI 契约（后续里程碑维护） |

依赖方向：`api/http → app → agent → domain`；`store` 实现 `ports`；`domain` 不依赖 eino / SQL / HTTP。

## 存储取舍

- **PostgreSQL 是唯一运行期事实源**：会话、题纲、回合、评估、run、run_step、事件全部落库；不存在 `STORAGE=memory` 运行开关。
- **内存实现只做测试替身**：单测用它验证状态机、幂等与恢复；它与 PostgreSQL 实现跑同一套契约断言。
- **Redis 不是正确性依赖**：M5 才接入，只做跨进程锁与幂等快路径；删掉 Redis 后系统退化为 PostgreSQL advisory lock。
- **恢复不依赖 eino checkpoint**：用 `runs` / `run_steps` 的节点级产物，从最后成功节点续跑。

## 里程碑

M0 骨架（已完成）→ **M1 真实存储 + 会话状态机**（已完成）→ **M2 回合图 + 假模型 + 完整作答链路**（已完成）→ **M3 真实模型 + 题纲出题 + compose.Graph**（已完成）→ **M4 收尾与报告**（已完成）→ **M5 Redis 会话锁 + LISTEN/NOTIFY 事件推送**（已完成）→ **M6 评分回归集 + 批量跑分**（已完成）→ **M7 可观测性与成本口径**（已完成）→ **M8 最小可视化演示页**（已完成）→ **M9 简历附件解析**（已完成）→ **M12 规则版简历诊断**（已完成）→ **M13 闯关式进度条**（已完成）→ **M14 领域包机制**（已完成）。

每个里程碑的可运行验收点见设计文档第 12 节。

## 注意

- M1 没有任何鉴权，仅用于本地开发；不要直接暴露到公网。
- 多实例部署必须配置可达的 Redis；启动时 Redis 不可达会回退进程内锁并打印 Warn，此时只保证单实例正确。
- SSE 的实时性依赖 PostgreSQL `LISTEN`；监听连接断开后自动重连（500ms），期间客户端按 `Last-Event-ID` 续播不会丢事件。
- 语音/ASR、支付、OAuth、多端小程序等明确不做，清单见设计文档第 13 节。
