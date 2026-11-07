# AI 模拟面试 Agent

把一次真实求职流程跑完的文本面试 Agent：**简历诊断 → JD 匹配 → 多轮面试 → 复盘报告 → 间隔复习**。

![Go 1.25](docs/assets/badges/go-1.25.svg)
![PostgreSQL 16](docs/assets/badges/postgresql-16.svg)
![CI passing](docs/assets/badges/ci-passing.svg)
![Tests passing](docs/assets/badges/tests-passing.svg)
![License MIT](docs/assets/badges/license-mit.svg)

<table>
  <tr>
    <td width="50%"><img src="docs/screenshots/flow-r5-interview.png" alt="面试进行中：SSE 实时推送、题纲进度与追问支线"></td>
    <td width="50%"><img src="docs/screenshots/flow-r5-report.png" alt="复盘报告：总分、雷达、逐题趋势与复习曲线"></td>
  </tr>
  <tr>
    <td><b>面试进行中</b>：SSE 已连接，题纲进度停在 1 / 3，并出现「追问 1 / 上限 3」支线。</td>
    <td><b>复盘报告</b>：5 题收尾后的总分 27.8（待提升）、维度雷达、逐题趋势与 SM-2 曲线。</td>
  </tr>
</table>

Go 1.25 + [cloudwego/eino](https://github.com/cloudwego/eino) + PostgreSQL + Redis。面试主链路可接任意 OpenAI 兼容模型；没有 key 时自动回退确定性 `heuristic`，演示与测试不依赖外部服务。

---

## 能做什么

| 阶段 | 你得到什么 | 关键实现 |
| --- | --- | --- |
| **简历诊断** | 总分与档位、初筛 / 技术 / 文档三项分、A–E 维度、问题清单与「差 → 好」改写 | 本地规则，不调模型 |
| **JD 匹配** | 匹配度、技能覆盖（已覆盖 / 未体现）、缺口清单、复用题纲、申请材料包 | 关键词与行规则；公开页抓取遵守 robots 与限速 |
| **模拟面试** | 一题一评：维度分、证据、加减分、缺失要点、追问或推进 | eino 图 + 10 级规则裁决 |
| **复盘报告** | 五段报告、综合分与等级、雷达图、逐题趋势、token 与调用次数快照 | 3 个只读工具确定性采集，模型只写点评 |
| **间隔复习** | SM-2 复习卡、到期队列、换问法、0–5 自评与遗忘曲线 | 本地规则，不调模型 |

### 工程能力

| 维度 | 做法 |
| --- | --- |
| 控制面 / 数据面 | `POST` 返回 `202 + run_id`；进度从独立 SSE 订阅，支持 `Last-Event-ID` 续播与 15s 心跳 |
| 作答评估 | 五维打分（correctness / depth / engineering_judgment / structure / evidence_credibility）+ 加减分 + 缺失要点；证据在**去空白、标点并转小写后可定位**，不在原文中的证据会被丢弃 |
| 追问裁决 | 10 级门：时间 → 题纲耗尽 → 总追问预算 → 深度上限 → 单题上限 → 「不会」识别 → 评估降级 → 要点覆盖 → 模型建议 → 兜底推进 |
| 报告 | 固定五段；模型只写点评，分数由确定性公式计算；`get_session_overview` / `search_turns` / `list_weak_turns` 三个只读工具采集后交模型（`get_turn` 已实现，报告未使用）；失败降级为确定性拼装 |
| 幂等 | 同一 `Idempotency-Key` 只产生一个 run；重复作答返回原 run 并带 `Idempotent-Replayed: true` |
| 跨实例锁 | Redis `SET NX PX` + token 比较释放；抢锁失败返回可重试的 `409 lock_busy` |
| 崩溃恢复 | `runs` / `run_steps` 节点级产物，从最后一个成功节点续跑；启动时把超时 `running` run 标记 `interrupted` |
| 队列消费 | PostgreSQL `FOR UPDATE SKIP LOCKED` 认领 + `lease_expires_at` 租约；多实例并行不重复，崩溃后租约过期可被其他实例接管 |
| 限流与死信 | 按用户 + 动作的 Redis 固定窗口（创建会话 / 提交作答 / 生成报告），超限 429 `rate_limited`；匿名 demo 更严格；连续失败超阈值进入 `dead`，可查询、可人工重试 |
| 可观测 | Prometheus 指标覆盖模型、SSE、锁、run 失败、worker 认领 / 租约、限流与死信；结构化日志带 `request_id / session_id / run_id / turn_id / worker_id / lease_id`；报告含 token 与调用次数（不计算金额） |
| 评测 | 60 条回归集（Go 后端 35 / AI Agent 25），五个门槛指标；只读 `/eval` 面板查看历史结果、逐条失准与领域分布，`cmd/evaldiff` 对比两份结果文件 |

---

## 视觉主线

六屏按求职顺序排列，统一 1440 宽、1.5x。前两屏是同一份简历的规则版演示；后四屏来自一场真实 DeepSeek 会话（3 主问题 + 2 追问）。

**① 简历诊断**｜场景：粘贴一份真实简历，点「诊断当前简历文本」｜画面：总分 69（3 档）、初筛 77 / 技术 64 / 文档 64、A–E 维度与「差 → 好」改写折叠。

<img src="docs/screenshots/flow-r5-resume-diagnosis.png" width="100%" alt="简历诊断：总分、三项分、A–E 维度与改写建议">

**② JD 匹配**｜场景：同一份简历对上「Go 后端开发工程师（订单履约方向）」｜画面：匹配度 77、技能覆盖 chips、复用领域包的 5 题题纲、缺口清单与材料包入口。

<img src="docs/screenshots/flow-r5-jd-match.png" width="100%" alt="JD 匹配：匹配度、技能覆盖、复用题纲与缺口清单">

**③ 面试进行中**｜场景：创建会话并开场｜画面：SSE 已连接、题纲 1 / 3、追问支线「1 / 上限 3」、已用时长与事件流。

<img src="docs/screenshots/flow-r5-interview.png" width="100%" alt="面试进行中：SSE、进度、追问支线与事件流">

**④ 单题评分依据**｜场景：提交第一题作答｜画面：20.0 分的维度权重表、加权公式、加分 / 扣分、证据摘录、缺失要点与决策理由。

<img src="docs/screenshots/flow-r5-rationale.png" width="100%" alt="单题评分依据：维度分、公式、证据、缺失要点与决策">

**⑤ 复盘报告**｜场景：收尾并生成报告｜画面：总分 27.8（待提升）、维度雷达、逐题趋势、SM-2 遗忘曲线与五段点评。

<img src="docs/screenshots/flow-r5-report.png" width="100%" alt="复盘报告：总分、雷达、趋势、复习曲线与五段点评">

**⑥ 间隔复习**｜场景：报告落库后进入复习页｜画面：今日到期 1 / 21、正面问题、参考答案折叠、0–5 自评与遗忘曲线。

<img src="docs/screenshots/flow-r5-review.png" width="100%" alt="间隔复习：到期队列、卡片正面、自评与遗忘曲线">

---

## 三分钟跑起来

```powershell
# 1) 起依赖：PostgreSQL 5433 / Redis 6380（避免与本机服务冲突）
docker compose -f deploy/docker-compose.yml up -d

# 2) 启动：自动执行 migrations/，并做一次中断 run 的 reconciliation
go run ./cmd/server
```

浏览器打开 **<http://localhost:8080/demo>**。

**不配 key 也能跑**：没有 `OPENAI_API_KEY` 且 `MODEL_PROVIDER` 留空时，服务自动使用确定性 `heuristic`，开场、评分、报告全部可演示。

接真实模型（任意 OpenAI 兼容服务，示例为 DeepSeek）：

```powershell
$env:OPENAI_API_KEY="<your-api-key>"                # 只放本机，不要提交
$env:OPENAI_BASE_URL="https://api.deepseek.com/v1"
$env:OPENAI_MODEL="deepseek-flash"
go run ./cmd/server
```

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `HTTP_ADDR` | `:8080` | HTTP 监听地址 |
| `DATABASE_URL` | `postgres://interview:interview@localhost:5433/interview?sslmode=disable` | 唯一运行期事实源 |
| `REDIS_ADDR` | `localhost:6380` | 会话锁；不可达时回退进程内锁 |
| `MODEL_PROVIDER` | 见说明 | `heuristic` / `openai`；留空时有 key 则选 openai，否则 heuristic |
| `OPENAI_BASE_URL` | `https://api.deepseek.com/v1` | 任意 OpenAI 兼容 base URL |
| `OPENAI_MODEL` | `deepseek-flash` | 模型名；旧名 `deepseek-v4-flash` 仍兼容 |
| `RUN_STALE_AFTER` | `60s` | 超过该时间未更新的 `running` run 视为崩溃遗留 |
| `JWT_SECRET` | 无 | **生产必须设置**；缺失时启动生成随机临时值并打 `WARN`，重启后旧 token 失效 |
| `JWT_TTL` | `24h` | 访问令牌有效期 |
| `DEMO_LOGIN_LIMIT` | `5` | 单 IP 在窗口内允许的匿名 demo 登录次数 |
| `DEMO_LOGIN_WINDOW` | `1h` | 匿名 demo 限流窗口 |
| `WORKER_ID` | 主机名-PID-ULID | worker 实例标识；多实例部署时显式区分 |
| `WORKER_CONCURRENCY` | `2` | 单实例同时处理的 run 数上限，超出的留在队列 |
| `WORKER_LEASE_TTL` | `30s` | 认领租约时长；进程崩溃后到期可被其他实例接管 |
| `WORKER_HEARTBEAT_INTERVAL` | `10s` | 处理中续租间隔；续租失败即放弃本地 run |
| `WORKER_POLL_INTERVAL` | `1s` | 空队列时的轮询间隔 |
| `RUN_MAX_FAILURES` | `3` | 连续失败达到该次数进入 `dead`（死信） |
| `RATE_LIMIT_WINDOW` | `1h` | 单用户限流窗口 |
| `RATE_LIMIT_SESSION` | `20` | 每窗口创建会话次数 |
| `RATE_LIMIT_ANSWER` | `120` | 每窗口提交作答次数 |
| `RATE_LIMIT_REPORT` | `10` | 每窗口生成报告次数 |
| `DEMO_RATE_LIMIT_SESSION` | `3` | 匿名 demo 创建会话额度（更严格） |
| `DEMO_RATE_LIMIT_ANSWER` | `20` | 匿名 demo 提交作答额度（更严格） |
| `DEMO_RATE_LIMIT_REPORT` | `3` | 匿名 demo 生成报告额度（更严格） |

---

## 架构与目录

```
HTTP / SSE  →  应用服务（事务 / 幂等 / 锁）  →  eino 图（Agent 决策）  →  领域规则 / 端口  →  PostgreSQL / Redis
```

- **PostgreSQL 是唯一运行期事实源**：会话、题纲、回合、评估、run / run_step、事件、JD、复习卡全部落库；内存实现只做测试替身。
- **Redis 不是正确性依赖**：做跨进程锁与限流计数；锁不可达时回退进程内锁（此时只保证单实例正确），限流不可达时 fail-open 并打 `WARN` + 指标。
- **队列也在 PostgreSQL 里**：worker 用 `SELECT … FOR UPDATE SKIP LOCKED` 认领 + 租约续租，不引入额外消息中间件；单实例并发由 `WORKER_CONCURRENCY` 限制，超出留在队列。
- **恢复不依赖 eino checkpoint**：用 `runs` / `run_steps` 的节点级产物，从最后成功节点续跑。
- **回合制短图**：每次 HTTP 命令触发一次 `compose.Graph`，执行完即结束；人类作答跨请求，不依赖长驻 Agent。

| 目录 | 职责 |
| --- | --- |
| `cmd/server/` | 入口：配置、依赖组装、迁移、HTTP 启动、优雅退出 |
| `cmd/evalrunner/` | 评分回归集批量跑分 |
| `cmd/evaldiff/` | 对比两份已有评测结果文件（指标差异 + 判定翻转，不调模型） |
| `internal/api/http/` | 路由、DTO、参数校验、错误映射、SSE 写出、`request_id` |
| `internal/app/` | 用例编排：事务、幂等、锁、状态提交、事件发布、run 执行与恢复；`worker.go` 为跨实例认领 / 租约 / 死信消费 |
| `internal/auth/` | 密码哈希、HS256 JWT 签发与校验、注册 / 登录 / 匿名 demo |
| `internal/agent/` | eino 图、prompt、模型调用、评估 / 出题 / 报告、只读工具 |
| `internal/domain/` | 会话、评估、报告、追问、领域包、题纲、简历、JD、复习规则 |
| `internal/ports/` | 仓储 / 模型 / 事件 / 锁 / 限流接口与事务边界 |
| `internal/platform/` | 配置、日志、ID、可观测性、公开页面抓取 |
| `internal/store/` | PostgreSQL / Redis 实现与内存测试替身；`redis/ratelimit.go` 固定窗口计数 |
| `internal/transport/sse/` | SSE 帧、心跳、断线续播 |
| `internal/api/http/demo/` | 演示页静态资源（`//go:embed`，无构建链） |
| `domainpacks/` | 内嵌领域包：`go-backend` / `java-backend` / `ai-agent` |
| `migrations/` | SQL 迁移，启动时自动执行 |
| `docs/eval-results/` | 历史评测结果 JSON（`/eval` 与 `evaldiff` 的数据源） |
| `deploy/docker-compose.yml` | PostgreSQL + Redis 本地依赖 |

模块级职责与行号见 [拆解读物](docs/project-walkthrough.md)。

---

## 接口与事件

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/healthz` | 健康检查（PostgreSQL） |
| GET | `/metrics` | Prometheus 文本指标 |
| GET | `/demo` | 可视化演示页 |
| GET | `/eval` | 只读评测面板：历史结果、门槛指标、逐条失准、领域分组与 A/B |
| POST | `/v1/auth/register` | 注册并签发 JWT（邮箱 + 密码，bcrypt） |
| POST | `/v1/auth/login` | 登录并签发 JWT |
| POST | `/v1/auth/demo` | 匿名体验：每次创建独立临时用户，按 IP 限流 |
| POST | `/v1/resume/parse` | 解析简历附件（txt / md / docx / pdf） |
| POST | `/v1/resume/analyze` | 规则版简历诊断 |
| POST | `/v1/jd/analyze` | 解析 URL 或粘贴文本并与简历匹配 |
| GET | `/v1/jd/{id}` | 查询 JD 解析结果 |
| POST | `/v1/jd/{id}/kit` | 生成申请材料包（不代投） |
| GET | `/v1/domains` | 可用领域包 |
| POST | `/v1/sessions` | 创建会话 |
| GET | `/v1/sessions/{id}` | 会话状态与 `pending_turn_id` |
| POST | `/v1/sessions/{id}/start` | 开场（`202 + run_id`） |
| POST | `/v1/sessions/{id}/answers` | 提交作答（`202 + run_id`） |
| POST | `/v1/sessions/{id}/abort` | 中止 |
| POST | `/v1/sessions/{id}/finish` | 收尾并生成报告 |
| POST | `/v1/sessions/{id}/report` | 对已收尾会话补生成报告 |
| GET | `/v1/sessions/{id}/report` | 取报告（JSON + Markdown） |
| GET | `/v1/sessions/{id}/events` | SSE 事件流 |
| POST | `/v1/sessions/{id}/runs/{run_id}/retry` | 显式重试失败 / 中断 / 死信 run（重新入队） |
| GET | `/v1/runs` | 查询本人的异常 run（`status` 取 `all` / `failed` / `interrupted` / `dead`，含失败原因与最后错误） |
| GET | `/v1/review/due` | 到期复习卡（`tz_offset_minutes`，默认 480） |
| GET | `/v1/review` | 按 `session_id` 列出复习卡 |
| GET | `/v1/review/{id}` | 复习卡详情 |
| POST | `/v1/review/{id}/grade` | 自评 0–5，更新 SM-2 排程 |
| GET | `/v1/review/{id}/retest` | 规则生成的「换问法」 |

事件顺序：`session.created` → `run.started` → `session.started` → `question.asked` → `session.phase`；作答后追加 `answer.received` → `evaluation.completed`（或 `degraded`）→ `decision.made`；收尾后追加 `report.started` → `report.section` ×5 → `report.tool_calls` → `report.completed` → `session.completed`。

除 `/healthz`、`/metrics`、`/demo` 与三个 `/v1/auth/*` 端点外，所有 `/v1` 接口都要求 `Authorization: Bearer <token>`；缺失或过期返回 `401 unauthorized`。跨账号访问统一返回 **404**，不泄露资源是否存在；SSE 订阅前同样校验归属。EventSource 无法自定义 Header，因此 `GET /v1/sessions/{id}/events` 额外接受 `?access_token=`（其他路径不接受查询参数令牌）。

**限流**：按「用户 + 动作」在 Redis 做固定窗口计数，动作分 `create_session` / `submit_answer` / `generate_report` 三类，各自独立额度；匿名 demo 用户走更严格的一组额度。超限返回 **429**，错误码 `rate_limited`，`Retry-After` 头与 `details` 里给出动作、额度、窗口与剩余等待秒数。Redis 不可用时 **fail-open**（放行请求），同时打 `WARN` 日志并累加 `rate_limit_degraded_total{action}`，既不静默也不整站拒绝。

**失败与死信**：run 每次失败记录 `last_error` / `last_error_class`（`model` / `state_conflict` / `internal` / `panic` 等），失败计数达到 `RUN_MAX_FAILURES` 后进入终止状态 `dead`。`GET /v1/runs` 可查询本人失败的 run 及原因；`POST /v1/sessions/{id}/runs/{run_id}/retry` 对 `dead` run 会重置失败计数并重新入队（获得全新重试预算），对 `failed` / `interrupted` 则保留计数继续累计。

**数据归属与旧数据**：`sessions / runs / run_steps / session_events / turns / evaluations / reports / review_cards / jd_listings` 均带 `user_id` 与索引。迁移 `0008_auth.sql` 把历史行回填到占位用户 `usr_legacy`；该用户密码哈希为空，任何登录路径都无法认证为它，因此旧数据不会暴露给新注册账号。新行必须显式写入 owner，迁移后的列已去掉默认值，漏写会直接失败。迁移 `0009_worker.sql` 给 `runs` 加租约与失败字段：历史行的 lease 为空、失败计数为 0，因此旧的 `running` run 可被 worker 立即认领；`Reconcile` 仍先把超时 run 标为 `interrupted` 并保留原事件，历史失败 run 不会自动进入死信。

<details>
<summary>命令行完整流程（PowerShell / curl）</summary>

```powershell
# 先登录或匿名体验，拿到 Bearer token
curl.exe -s -X POST http://localhost:8080/v1/auth/demo -o token.json
$token = (Get-Content token.json | ConvertFrom-Json).access_token
$h = @("-H", "Authorization: Bearer $token")

# PowerShell 5.1 内联 -d 传中文会按 GBK 发送，先落 UTF-8 文件
$json = '{"candidate_name":"张三","target_role":"Go 后端开发","resume_text":"三年 Go 经验","jd_text":"熟悉分布式系统"}'
[System.IO.File]::WriteAllText("$PWD\session.json", $json, (New-Object System.Text.UTF8Encoding($false)))

curl.exe -i -X POST http://localhost:8080/v1/sessions @h -H "Content-Type: application/json; charset=utf-8" --data-binary "@session.json"

curl.exe -i -X POST http://localhost:8080/v1/sessions/<session_id>/start @h -H "Idempotency-Key: start-demo-1"
curl.exe -s   http://localhost:8080/v1/sessions/<session_id> @h          # 取 pending_turn_id

$answer = '{"turn_id":"<turn_id>","answer":"我在订单服务里用本地消息表加定时补偿保证最终一致性，压测峰值 1.2 万 TPS。"}'
[System.IO.File]::WriteAllText("$PWD\answer.json", $answer, (New-Object System.Text.UTF8Encoding($false)))
curl.exe -i -X POST http://localhost:8080/v1/sessions/<session_id>/answers @h -H "Content-Type: application/json; charset=utf-8" -H "Idempotency-Key: answer-demo-1" --data-binary "@answer.json"

curl.exe -i -X POST http://localhost:8080/v1/sessions/<session_id>/finish @h -H "Idempotency-Key: finish-demo-1"
curl.exe -s   http://localhost:8080/v1/sessions/<session_id>/report @h
curl.exe -N   "http://localhost:8080/v1/sessions/<session_id>/events?access_token=$token"  # Ctrl+C 退出
```

</details>

---

## 验证与评测

```powershell
gofmt -l .        # 应为空
go vet ./...
go test ./...

# 集成测试（需要本地 PostgreSQL / Redis）
$env:DATABASE_URL='postgres://interview:interview@127.0.0.1:5433/interview?sslmode=disable'
$env:REDIS_ADDR='127.0.0.1:6380'
go test -tags=integration ./...

# 评分回归
go run ./cmd/evalrunner -provider heuristic
$env:OPENAI_API_KEY="..."
go run ./cmd/evalrunner -provider openai

# A/B 对比两份已有结果文件（不调用模型，没有 key 也能跑）
go run ./cmd/evaldiff -base docs/eval-results/<baseline>.json -candidate docs/eval-results/<candidate>.json

# 只读评测面板：浏览器打开 http://localhost:8080/eval
go run ./cmd/server
```

| 门槛 | 要求 |
| --- | --- |
| schema 合法率 | 100% |
| 档位命中率 | ≥ 80% |
| 相邻档命中率 | ≥ 95% |
| 维度 MAE | ≤ 0.6 |
| 证据引用率 | 100% |

**回归集 60 条**：Go 后端 35、AI Agent 25。类型分布：high 15、mid 18、low 7、empty 3、exaggerated 3、off_topic 3、jargon 3、unverified 3、conclusion_only 3、verbose_offtopic 2。每条都有 `expected_band`、五维人工分、说明归因的 `notes`，以及必须能在答案原文中定位的 `evidence_anchor`；加载时会校验锚点、档位与分数是否自洽。

**门槛为什么是这四个**：`schema_valid_rate=100%` 因为下游依赖结构化字段，解析失败等于不可用；`band_hit_rate≥80%` 因为三档是产品直接展示的结论，允许少量边界抖动；`adjacent_band_rate≥95%` 因为 ±1 档可接受、跨两档才是明显误判；`dimension_mae≤0.6` 因为五维是 1–5 分制，0.6 约等于 12% 满量程。`evidence_quote_rate=100%` 是硬不变量，单独列出。

**失准归因**：结果文件同时记录 `deviations`（哪个维度差了几分）、`misses_by_type` 与 `misses_by_domain`。面板按领域包分组展示「期望档 → 实际档」、偏差维度、答案锚点与 `notes`；对比两份文件时列出指标差异与档位翻转。归因顺序是先看某一类（长但空洞 / 指标无出处 / 跑题 / 只答结论）是否系统性偏移，再看单个 case 的期望值是否需要人工复核。

**A/B**：`cmd/evaldiff` 与 `/eval?base=a.json&candidate=b.json` 只读历史文件，不发起模型调用，没有 key 也能跑；输出五个指标的差值、门槛状态变化，以及 `improved / regressed / changed` 判定翻转。

结果文件含时间戳、provider、模型名、prompt 版本、`dataset_sha256`、指标与逐条明细，门槛不达标时退出码为 2。**真实模型基线已更新为 `eval-v4` 的 60 条数据**（2026-11-07 跑分；请求模型 `deepseek-chat`，供应商实际返回 `deepseek-flash` / DeepSeek-V4.1-Flash-1017；一次跑完 60 次调用，0 降级、0 错误）：schema 合法率 100%、档位命中率 **80.0%（48/60）**、相邻档 100%、维度 MAE **0.4733**、证据引用率 100%，**五个门槛全部通过**；分领域命中率 Go 后端 82.9%（29/35）、AI Agent 76.0%（19/25）。与同集 `eval-v3` 的 A/B：档位命中率 **81.7%→80.0%（−1 条，净效应变差）**、维度 MAE **0.4967→0.4733（改善）**，其余三项持平；实际档位翻转 2 条且均为回退（`ai-mid-03-memory`、`ai-mid-07-context-budget` 3→5）；标注修正使 `ai-high-01-rag-pipeline` 由期望 5 档改为 3 档并转为命中，但模型实际分未变（74），不计为模型改进。v4 结果：[eval-openai-deepseek-chat-eval-v4-20261107T143259Z.json](docs/eval-results/eval-openai-deepseek-chat-eval-v4-20261107T143259Z.json)；v3 结果保留：[eval-openai-deepseek-chat-eval-v3-20261107T134323Z.json](docs/eval-results/eval-openai-deepseek-chat-eval-v3-20261107T134323Z.json)。

---

## 已知不足

- **鉴权是最小实现**：只有邮箱密码 + JWT，没有 RBAC、找回密码、第三方登录与刷新令牌；匿名 demo 的业务动作限流已走 Redis，但 demo 登录次数仍是单进程 IP 计数，多副本需统一到 Redis。
- **简历 / JD 仍是规则语义**：正则与关键词可复现，但同义词、隐性技能、复杂排版会漏判；没有 embedding 语义匹配。
- **图片简历与扫描版 PDF 没有 OCR**：附件只支持 txt / md / docx / pdf；PNG / JPG 返回 `image_requires_ocr`，扫描版 PDF 提不出文字。
- **报告证据包有截断**：回答摘要、弱项条数、搜索结果都有上限；只有一轮模型点评，无跨场趋势与多轮修订。
- **复习只有 SM-2 + 自评**：没有每日上限、优先级与长期统计；0–5 是用户自报，系统不做客观回忆验证。
- **队列是 PostgreSQL 认领（做浅了）**：`SKIP LOCKED` + 租约已支持多实例与崩溃接管，但空队列靠轮询（跨实例唤醒未接 `LISTEN/NOTIFY`），没有优先级、延迟队列与指数退避；恢复粒度是 step，不是精确断点。
- **可观测性只在进程内**：指标重启归零，没有集中日志、分布式追踪与告警；多实例各看各的。
- **JD 抓取只适配静态 HTML**：不执行 JS、不碰登录墙与验证码；抓不到就要求粘贴。

完整 12 条见 [拆解读物 · 已知不足](docs/project-walkthrough.md#11-已知不足)。另有明确边界：JD 抓取不代投；语音 / ASR、支付、RBAC / 找回密码 / OAuth、多端小程序不在范围内。

---

## 文档与里程碑

| 文档 | 内容 |
| --- | --- |
| [开发计划](docs/development-plan.md) | 架构、状态机、协议、评测与里程碑的单一事实源 |
| [设计取舍](docs/design-rationale.md) | 13 节 + 附录：为什么这样写、换一种做法会怎样 |
| [拆解读物](docs/project-walkthrough.md) | 9 个模块 + 10 分钟演示脚本 + 13 条已知不足 |

已实现 M0–M9、M11–M19（M10 未做），演示页视觉轮次 R2–R5。每个里程碑的可运行验收点见[开发计划第 12 节](docs/development-plan.md#12-里程碑与验收点)。
