# AI 模拟面试 Agent

把一次真实求职流程跑完的文本面试 Agent：**简历诊断 → JD 匹配 → 多轮面试 → 复盘报告 → 间隔复习**。

Go 1.25 + [cloudwego/eino](https://github.com/cloudwego/eino) + PostgreSQL + Redis。面试主链路可接任意 OpenAI 兼容模型；没有 key 时自动回退确定性 `heuristic`，演示与测试不依赖外部服务。简历解析、简历诊断、JD 匹配、申请材料包与复习排程是**本地规则实现，不调用模型**。

> 已实现：M0–M9、M11–M16，以及演示页视觉轮次 R2–R5。
> 设计单一事实源：[docs/development-plan.md](docs/development-plan.md)；关键取舍：[docs/design-rationale.md](docs/design-rationale.md)；按模块拆解、含 10 分钟演示脚本与已知不足：[docs/project-walkthrough.md](docs/project-walkthrough.md)。
> PostgreSQL 是唯一运行期事实源；Redis 只做跨进程会话锁；内存实现仅用于单测。

## 一条视觉主线

以下截图均为 1440 宽、1.5x，按求职顺序排列。前两屏来自同一份简历的规则版演示（不调用模型），结果整宽展示在主面板，左侧栏只保留简历 / JD 输入入口；后四屏来自一场真实 DeepSeek 会话（5 道题：3 主问题 + 2 追问），**面试与报告文字由 DeepSeek 生成；评分由确定性公式计算**（维度分 × 权重 + 加减分），不依赖模型。

### 简历诊断

![简历诊断](docs/screenshots/flow-r5-resume-diagnosis.png)

整宽结果页：顶部总分 69（3 档）与初筛 77 / 技术 64 / 文档 64，一句结论指出无明示匹配；中部 A–E 维度与分段得分；下部 R03 / R14 / R18 三条问题与「差 → 好」改写折叠展开。

### JD 匹配

![JD 匹配](docs/screenshots/flow-r5-jd-match.png)

同一份简历对上「Go 后端开发工程师（订单履约方向）」：整宽页顶部匹配度 77，中部技能覆盖 chips（绿色已覆盖、红色未体现）与复用领域包生成的 5 题题纲，下部缺口清单与申请材料包入口。

### 面试进行中

![面试进行中](docs/screenshots/flow-r5-interview.png)

SSE 已连接，进度停在第 1 / 3 题；题纲进度条出现「追问 1 / 上限 3」支线，当前问题就是由第 1 题触发的追问。首屏即可看到已用时长与事件流。

### 单题评分依据

![单题评分依据](docs/screenshots/flow-r5-rationale.png)

第一题 20.0 分：维度分 × 权重表、加权公式、加分 / 扣分、证据摘录、缺失要点与决策理由全部展开。分数由确定性公式计算，模型只提供维度分与点评。

### 复盘报告

![复盘报告](docs/screenshots/flow-r5-report.png)

5 题收尾后总分 27.8（待提升），题纲覆盖 5 / 3：维度雷达、逐题趋势与 SM-2 遗忘曲线预测。报告点评由 DeepSeek 生成，评分由确定性公式计算。

### 间隔复习

![间隔复习](docs/screenshots/flow-r5-review.png)

报告提交的同一事务生成复习卡；这里今日到期第 1 / 21 张（到期 11/06），正面问题、参考答案折叠与 0–5 自评按钮同屏，自评后按 SM-2 写回下次到期时间。

## 三分钟跑起来

```powershell
# 1. 本地依赖（PostgreSQL 5433 / Redis 6380，避免与本机服务冲突）
docker compose -f deploy/docker-compose.yml up -d

# 2. 启动服务：自动执行 migrations/ 并完成一次中断 run 的 reconciliation
go run ./cmd/server
```

浏览器打开 <http://localhost:8080/demo>，不需要配置模型 key。

## 一条完整的演示路径

演示页不引入前端工程，`index.html` / `app.js` / `style.css` 由 `//go:embed` 打进二进制。

1. **简历**：粘贴简历文本，或上传 PDF / DOCX / TXT / MD（≤ 5 MB，本地解析）得到结构化画像。
2. **诊断**：点「诊断当前简历文本」，得到规则版问题清单与改写建议。
3. **JD**：粘贴 JD 文本，或填公开岗位链接；解析后可匹配简历、生成「申请材料包」（定制摘要 / 开场 / 题纲 / 预演问题）。材料包只重排简历已有事实，不虚构，也不提供自动投递。
4. **面试**：选目标岗位与主问题数，创建会话并开始；首题经 SSE 实时推送。
5. **作答**：提交后依次出现评分、维度得分、缺失要点与决策（追问 / 下一题 / 收尾），直到题纲完成。
6. **报告**：结束后生成综合分、等级、五段报告与 token / 成本摘要；模型不可用时给确定性降级报告，不中断整场。
7. **复习**：报告提交的同一事务里，把低分题与 `missing_points` 转成 SM-2 复习卡；顶栏「今天该复习 N 张」进入正面 → 参考答案 → 自评 0–5 → 下一张。

## 能力清单

### 面试主链路

- 回合制短图：每次 HTTP 命令触发一次 eino `compose.Graph`，执行完即结束；人类作答跨请求，不依赖长驻 Agent。
- 控制面 `POST` + 数据面 SSE：命令返回 `202 + run_id`，进度从独立 SSE 端点订阅；断线用 `Last-Event-ID` / `?after=` 续播。
- 结构化评估：维度分、证据引用、加减分、`missing_points`、追问建议；非法 JSON 修复 2 次，仍失败则降级并记录事件。
- 规则裁决：硬预算门（超时 / 题纲 / 追问总数 / 深度 / 单题追问 / 不会回答 / 评估降级）→ 覆盖门 → 模型建议 → 终裁，追问条数有硬上限。
- 报告用只读工具回查（`get_session_overview` / `search_turns` / `list_weak_turns` / `get_turn`），不把整场 transcript 塞进 prompt。
- 幂等：同一 `Idempotency-Key` 只产生一个 run；重复作答返回原 run 并带 `Idempotent-Replayed: true`。
- 跨实例锁：Redis `SET NX PX` + token 比较释放；抢锁失败返回可重试的 `409 lock_busy`。
- 崩溃恢复：`run_steps` 节点级产物，从最后一个成功节点续跑；启动时把超时 `running` run 标记 `interrupted`。
- 事件与业务数据同事务落库；`pg_notify` 只做唤醒，SSE 回查事件表，另有 15s 心跳。

### 求职前后（规则版，无模型调用）

- **M9 简历附件解析**：PDF / DOCX / TXT / MD / 图片走本地解析，失败给出可操作的提示。
- **M12 简历诊断**：问题清单、影响说明与改写方向，按维度确定性打分。
- **M15 JD 采集与匹配**：粘贴文本或抓取公开页面（遵守 robots、按 host 限速、有大小与超时上限；不模拟登录、不绕验证码）；解析要求并与简历做覆盖匹配。
- **M16 申请材料包**：定制摘要、开场、面试题纲与预演问题；只重排已有事实，不代投。
- **M11 复习与间隔重复**：SM-2 排程（ease / 间隔 / 重复次数），评分 0–5；保存计算过程与下次到期；「换问法」按重复次数轮换三种规则模板。

### 演示与工程化

- 领域包可插拔：`go-backend` / `java-backend` / `ai-agent`，会话创建时传 `domain`（默认 `go-backend`）；题纲、评分维度与报告维度都来自该包。
- 非技术岗位设计样例（售前解决方案 / FDE / 增长运营）放在 [docs/domainpacks/solution-and-ops.md](docs/domainpacks/solution-and-ops.md)，示例 manifest 不会被运行时加载。
- `/metrics` 暴露 Prometheus 文本指标；报告含模型调用数、token 与 `estimated_cost_usd`。
- 评分回归集 + 批量跑分（见下方「验证」），结果写入 `docs/eval-results/`。

## HTTP 接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/healthz` | 健康检查（PostgreSQL） |
| GET | `/metrics` | Prometheus 文本指标 |
| GET | `/demo` | 可视化演示页 |
| POST | `/v1/resume/parse` | 解析简历附件（本地） |
| POST | `/v1/resume/analyze` | 规则版简历诊断 |
| POST | `/v1/jd/analyze` | 解析 URL 或粘贴文本并与简历匹配 |
| GET | `/v1/jd/{id}` | 查询 JD 解析结果 |
| POST | `/v1/jd/{id}/kit` | 生成申请材料包（不代投） |
| GET | `/v1/domains` | 可用领域包 |
| POST | `/v1/sessions` | 创建会话 |
| GET | `/v1/sessions/{id}` | 查询会话状态与 `pending_turn_id` |
| POST | `/v1/sessions/{id}/start` | 开场（`202 + run_id`） |
| POST | `/v1/sessions/{id}/answers` | 提交作答（`202 + run_id`） |
| POST | `/v1/sessions/{id}/abort` | 中止 |
| POST | `/v1/sessions/{id}/finish` | 收尾并生成报告 |
| POST | `/v1/sessions/{id}/report` | 对已收尾会话生成报告 |
| GET | `/v1/sessions/{id}/report` | 取报告（JSON + Markdown） |
| GET | `/v1/sessions/{id}/events` | SSE 事件流 |
| POST | `/v1/sessions/{id}/runs/{run_id}/retry` | 显式重试失败 run |
| GET | `/v1/review/due` | 到期复习卡（`tz_offset_minutes`，默认 480） |
| GET | `/v1/review` | 按 `session_id` 列出复习卡 |
| GET | `/v1/review/{id}` | 复习卡详情 |
| POST | `/v1/review/{id}/grade` | 自评 0–5，更新 SM-2 排程 |
| GET | `/v1/review/{id}/retest` | 规则生成的「换问法」 |

命令行完整流程（创建 → 开场 → 作答 → 收尾 → 报告 → SSE）：

<details>
<summary>展开 PowerShell / curl 示例</summary>

```powershell
# PowerShell 5.1 内联 -d 传中文会按 GBK 发送，先落 UTF-8 文件
$json = '{"candidate_name":"张三","target_role":"Go 后端开发","resume_text":"三年 Go 经验","jd_text":"熟悉分布式系统"}'
[System.IO.File]::WriteAllText("$PWD\session.json", $json, (New-Object System.Text.UTF8Encoding($false)))

curl.exe -i -X POST http://localhost:8080/v1/sessions -H "Content-Type: application/json; charset=utf-8" --data-binary "@session.json"

curl.exe -i -X POST http://localhost:8080/v1/sessions/<session_id>/start -H "Idempotency-Key: start-demo-1"
curl.exe -s   http://localhost:8080/v1/sessions/<session_id>          # 取 pending_turn_id

$answer = '{"turn_id":"<turn_id>","answer":"我在订单服务里用本地消息表加定时补偿保证最终一致性，压测峰值 1.2 万 TPS。"}'
[System.IO.File]::WriteAllText("$PWD\answer.json", $answer, (New-Object System.Text.UTF8Encoding($false)))
curl.exe -i -X POST http://localhost:8080/v1/sessions/<session_id>/answers -H "Content-Type: application/json; charset=utf-8" -H "Idempotency-Key: answer-demo-1" --data-binary "@answer.json"

curl.exe -i -X POST http://localhost:8080/v1/sessions/<session_id>/finish -H "Idempotency-Key: finish-demo-1"
curl.exe -s   http://localhost:8080/v1/sessions/<session_id>/report
curl.exe -N   http://localhost:8080/v1/sessions/<session_id>/events     # Ctrl+C 退出
curl.exe -s   http://localhost:8080/metrics
```

</details>

事件顺序：`session.created` → `run.started` → `session.started` → `question.asked` → `session.phase`；作答后追加 `answer.received` → `evaluation.completed`（或 `degraded`）→ `decision.made`；收尾后追加 `report.started` → `report.section` ×5 → `report.tool_calls` → `report.completed` → `session.completed`。

## 验证

```powershell
gofmt -l .        # 应为空
go vet ./...
go test ./...

# 集成测试（需要本地 PostgreSQL / Redis）
$env:DATABASE_URL='postgres://interview:interview@127.0.0.1:5433/interview?sslmode=disable'
$env:REDIS_ADDR='127.0.0.1:6380'
go test -tags=integration ./...
```

评分回归跑分：

```powershell
go run ./cmd/evalrunner -provider heuristic   # 无 key 基线，结果写入 docs/eval-results/
$env:OPENAI_API_KEY="..."                     # 有 key 时走真实 provider
go run ./cmd/evalrunner -provider openai

go test -tags=eval -count=1 -v ./internal/agent/eval/...
```

结果文件含时间戳、provider、模型名、prompt 版本、`dataset_sha256`、指标与逐条明细；档位门槛不达标时退出码为 2。

## 架构

```
HTTP / SSE  →  应用服务（事务 / 幂等 / 锁）  →  eino 图（Agent 决策）  →  领域规则 / 端口  →  PostgreSQL / Redis
```

| 层 | 职责 | 允许依赖 | 禁止 |
| --- | --- | --- | --- |
| `api/http` | 路由、参数校验、SSE 写出、错误映射 | `app`、`dto`、`transport/sse` | 直接调 LLM、开事务、写 SQL |
| `app` | 用例编排、事务、幂等、锁、事件发布 | `domain`、`agent`、`ports` | 拼 prompt、操作 eino 类型 |
| `agent` | eino 图、prompt、模型调用、输出解析、工具 | `domain`、`ports`、eino | 依赖 `api`、直接读写数据库 |
| `domain` | 实体、状态迁移、评分 / 追问 / 排程规则 | 标准库 | eino、SQL、Redis、HTTP |
| `ports` | 仓储 / 模型 / 事件 / 锁接口 | `domain` | 具体实现 |
| `store` | PostgreSQL / Redis / 内存实现 | `ports`、`domain` | 反向依赖 `app` / `api` |

关键点：eino 类型只在 `agent` 内部出现；`domain` 与 `api` 不出现 `schema.Message`、`compose.Runnable`。

### 存储与恢复取舍

- **PostgreSQL 是唯一运行期事实源**：会话、题纲、回合、评估、run、run_step、事件、JD、复习卡全部落库；没有 `STORAGE=memory` 运行开关。
- **内存实现只做测试替身**：与 PostgreSQL 实现跑同一套契约断言。
- **Redis 不是正确性依赖**：只做跨进程锁；不可达时启动 Warn 并回退进程内锁（此时只保证单实例正确）。
- **恢复不依赖 eino checkpoint**：用 `runs` / `run_steps` 的节点级产物，从最后成功节点续跑；已流出但未落库的模型文本直接丢弃并重新生成。

## 配置

只读取环境变量，不自动加载 `.env`（`.env.example` 仅作参考）：

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `HTTP_ADDR` | `:8080` | HTTP 监听地址 |
| `DATABASE_URL` | `postgres://interview:interview@localhost:5433/interview?sslmode=disable` | 唯一运行期存储，必填 |
| `REDIS_ADDR` | `localhost:6380` | 会话锁 Redis 地址；不可达时回退进程内锁 |
| `REDIS_PASSWORD` | 空 | Redis 密码 |
| `REDIS_DB` | `0` | Redis 逻辑库 |
| `LOCK_TTL` | `10s` | 会话锁过期时间 |
| `LOCK_ACQUIRE_TIMEOUT` | `3s` | 抢锁最长等待，超时返回 `409 lock_busy` |
| `RUN_STALE_AFTER` | `60s` | run 超过该时间未更新视为崩溃遗留 |
| `MODEL_PROVIDER` | 见说明 | `heuristic` 或 `openai`；留空时有 `OPENAI_API_KEY` 则选 openai，否则 heuristic |
| `OPENAI_BASE_URL` | `https://api.deepseek.com/v1` | 任意 OpenAI 兼容服务 base URL；OpenAI 官方改为 `https://api.openai.com/v1` |
| `OPENAI_API_KEY` | 空 | 为空时真实 provider 自动降级为 heuristic，启动日志 Warn |
| `OPENAI_MODEL` | `deepseek-flash` | 模型名；DeepSeek 现行模型，旧名 `deepseek-v4-flash` 仍兼容，`deepseek-chat` / `deepseek-reasoner` 已于 2026-07-24 停用；OpenAI 可用 `gpt-4o-mini` |
| `MODEL_TIMEOUT` | `30s` | 单次模型调用超时 |
| `MODEL_MAX_RETRIES` | `2` | 可重试失败的最大重试次数（0..5） |

## 本地依赖

| 服务 | 容器端口 | 宿主机端口 | 连接串 |
| --- | --- | --- | --- |
| PostgreSQL | 5432 | **5433** | `postgres://interview:interview@localhost:5433/interview?sslmode=disable` |
| Redis | 6379 | **6380** | `localhost:6380` |

## 目录职责

| 目录 | 职责 |
| --- | --- |
| `cmd/server/` | 入口：配置、依赖组装、迁移、HTTP 启动、优雅退出 |
| `cmd/evalrunner/` | 评分回归集批量跑分 |
| `internal/api/http/` | 路由、DTO、参数校验、错误映射、request_id 中间件 |
| `internal/api/http/demo/` | 演示页静态资源（`//go:embed`，无构建链） |
| `internal/app/` | 用例编排：事务、幂等、锁、状态提交、事件发布、run 执行与恢复 |
| `internal/agent/interview/` | 作答链路 `compose.Graph`、评估器、出题器、heuristic 假模型 |
| `internal/agent/llm/` | `BaseChatModel` 适配、provider 装配、超时 / 重试 / token 计数 |
| `internal/agent/report/` | 报告生成：模型写点评 + 确定性合并分数与结构 + 降级报告 |
| `internal/agent/tools/` | 报告阶段只读回查：归属校验、参数校验、结果上限、调用预算 |
| `internal/agent/prompt/` | 评估 / 出题 / 报告 prompt、修复消息与版本 |
| `internal/agent/eval/` | 评分回归集与契约测试（`eval` tag） |
| `internal/domain/session/` | 会话实体、状态与阶段、迁移规则、turn / plan / run 类型 |
| `internal/domain/evaluation/` | Rubric 输出结构、计分、证据校验 |
| `internal/domain/report/` | 报告 JSON 契约、分数段、确定性 5 段渲染 |
| `internal/domain/followup/` | 判定门顺序、预算上限、不会回答识别 |
| `internal/domain/domainpack/` | 领域包加载、manifest 校验、方向识别与选题计划 |
| `internal/domain/syllabus/` | 旧题纲 API 兼容层，转发到内嵌 `go-backend` 包 |
| `internal/domain/resume/` | 简历画像与文本解析 |
| `internal/domain/jd/` | JD 解析、匹配、申请材料包 |
| `internal/domain/review/` | 复习卡生成、SM-2 排程、换问法模板 |
| `internal/ports/` | 仓储 / 模型 / 事件 / 锁接口与事务边界 |
| `internal/platform/` | 配置、日志、ID、时钟、可观测性、公开页面抓取 |
| `internal/platform/jobfetch/` | 公开岗位页抓取：robots、限速、大小 / 超时上限 |
| `internal/store/postgres/` | PostgreSQL 仓储与 `LISTEN/NOTIFY` 订阅 |
| `internal/store/redis/` | Redis 会话锁 |
| `internal/store/memory/` | 测试替身（不参与运行期装配） |
| `internal/transport/sse/` | SSE 帧、心跳、断线续播 |
| `domainpacks/` | 内嵌运行时领域包：`go-backend` / `java-backend` / `ai-agent` |
| `migrations/` | SQL 迁移（启动时自动执行） |
| `deploy/docker-compose.yml` | PostgreSQL + Redis 本地依赖 |
| `docs/development-plan.md` | 架构、状态机、协议、评测与里程碑的单一事实来源 |
| `docs/design-rationale.md` | 关键取舍与反例 |
| `docs/domainpacks/` | 非技术岗位设计样例（售前 / FDE / 运营，不参与运行） |
| `docs/screenshots/` | 演示页截图 |

## 里程碑

M0 骨架 → M1 真实存储 + 会话状态机 → M2 作答链路 + 结构化评估 + 规则裁决 → M3 真实模型 + 题纲出题 + `compose.Graph` → M4 收尾与报告 → M5 Redis 锁 + `LISTEN/NOTIFY` → M6 评分回归集 → M7 可观测性与成本 → M8 演示页 → M9 简历附件解析 → **M11 复习与间隔重复** → M12 规则版简历诊断 → M13 闯关式进度条 → M14 领域包机制 → **M15 JD 采集与匹配** → **M16 申请材料包**；演示页视觉轮次 R2 → R3 → **R4**。

每个里程碑的可运行验收点见设计文档第 12 节。

## 边界

- 没有鉴权，仅用于本地开发；不要直接暴露到公网。
- 多实例部署必须配置可达的 Redis；否则只保证单实例正确。
- JD 抓取只处理公开页面，不模拟登录、不绕验证码、不做自动投递。
- 复习卡、简历诊断、JD 匹配与材料包都是规则实现，不调用模型；模型只用于面试提问 / 评估 / 报告。
- 语音 / ASR、支付、OAuth、多端小程序等明确不做，清单见设计文档第 13 节。
