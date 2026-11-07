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
| 可观测 | 8 个 Prometheus 指标；结构化日志带 `request_id / session_id / run_id / turn_id`；报告含 token 与调用次数（不计算金额） |

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
$env:OPENAI_API_KEY="sk-..."                       # 只放本机，不要提交
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

---

## 架构与目录

```
HTTP / SSE  →  应用服务（事务 / 幂等 / 锁）  →  eino 图（Agent 决策）  →  领域规则 / 端口  →  PostgreSQL / Redis
```

- **PostgreSQL 是唯一运行期事实源**：会话、题纲、回合、评估、run / run_step、事件、JD、复习卡全部落库；内存实现只做测试替身。
- **Redis 不是正确性依赖**：只做跨进程锁，不可达时回退进程内锁，此时只保证单实例正确。
- **恢复不依赖 eino checkpoint**：用 `runs` / `run_steps` 的节点级产物，从最后成功节点续跑。
- **回合制短图**：每次 HTTP 命令触发一次 `compose.Graph`，执行完即结束；人类作答跨请求，不依赖长驻 Agent。

| 目录 | 职责 |
| --- | --- |
| `cmd/server/` | 入口：配置、依赖组装、迁移、HTTP 启动、优雅退出 |
| `cmd/evalrunner/` | 评分回归集批量跑分 |
| `internal/api/http/` | 路由、DTO、参数校验、错误映射、SSE 写出、`request_id` |
| `internal/app/` | 用例编排：事务、幂等、锁、状态提交、事件发布、run 执行与恢复 |
| `internal/agent/` | eino 图、prompt、模型调用、评估 / 出题 / 报告、只读工具 |
| `internal/domain/` | 会话、评估、报告、追问、领域包、题纲、简历、JD、复习规则 |
| `internal/ports/` | 仓储 / 模型 / 事件 / 锁接口与事务边界 |
| `internal/platform/` | 配置、日志、ID、可观测性、公开页面抓取 |
| `internal/store/` | PostgreSQL / Redis 实现与内存测试替身 |
| `internal/transport/sse/` | SSE 帧、心跳、断线续播 |
| `internal/api/http/demo/` | 演示页静态资源（`//go:embed`，无构建链） |
| `domainpacks/` | 内嵌领域包：`go-backend` / `java-backend` / `ai-agent` |
| `migrations/` | SQL 迁移，启动时自动执行 |
| `deploy/docker-compose.yml` | PostgreSQL + Redis 本地依赖 |

模块级职责与行号见 [拆解读物](docs/project-walkthrough.md)。

---

## 接口与事件

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/healthz` | 健康检查（PostgreSQL） |
| GET | `/metrics` | Prometheus 文本指标 |
| GET | `/demo` | 可视化演示页 |
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
| POST | `/v1/sessions/{id}/runs/{run_id}/retry` | 显式重试失败 run |
| GET | `/v1/review/due` | 到期复习卡（`tz_offset_minutes`，默认 480） |
| GET | `/v1/review` | 按 `session_id` 列出复习卡 |
| GET | `/v1/review/{id}` | 复习卡详情 |
| POST | `/v1/review/{id}/grade` | 自评 0–5，更新 SM-2 排程 |
| GET | `/v1/review/{id}/retest` | 规则生成的「换问法」 |

事件顺序：`session.created` → `run.started` → `session.started` → `question.asked` → `session.phase`；作答后追加 `answer.received` → `evaluation.completed`（或 `degraded`）→ `decision.made`；收尾后追加 `report.started` → `report.section` ×5 → `report.tool_calls` → `report.completed` → `session.completed`。

<details>
<summary>命令行完整流程（PowerShell / curl）</summary>

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
```

| 门槛 | 要求 |
| --- | --- |
| schema 合法率 | 100% |
| 档位命中率 | ≥ 80% |
| 相邻档命中率 | ≥ 95% |
| 维度 MAE | ≤ 0.6 |
| 证据引用率 | 100% |

回归集 30 条；结果文件含时间戳、provider、模型名、prompt 版本、`dataset_sha256`、指标与逐条明细，门槛不达标时退出码为 2。真实模型基线为 **DeepSeek + `eval-v2`**（band_hit 0.80 通过）；当前代码 prompt 已到 `eval-v3`，深度锚点改动后尚未重跑。

---

## 已知不足

- **没有鉴权**：接口匿名可用，会话 ID 就是能力凭证；仅适合本地演示，公网部署不可接受。
- **简历 / JD 仍是规则语义**：正则与关键词可复现，但同义词、隐性技能、复杂排版会漏判；没有 embedding 语义匹配。
- **图片简历与扫描版 PDF 没有 OCR**：附件只支持 txt / md / docx / pdf；PNG / JPG 返回 `image_requires_ocr`，扫描版 PDF 提不出文字。
- **报告证据包有截断**：回答摘要、弱项条数、搜索结果都有上限；只有一轮模型点评，无跨场趋势与多轮修订。
- **复习只有 SM-2 + 自评**：没有每日上限、优先级与长期统计；0–5 是用户自报，系统不做客观回忆验证。
- **run 队列在进程内**：没有 worker pool、背压与跨实例消费；恢复粒度是 step，不是精确断点。
- **可观测性只在进程内**：指标重启归零，没有集中日志、分布式追踪与告警；多实例各看各的。
- **JD 抓取只适配静态 HTML**：不执行 JS、不碰登录墙与验证码；抓不到就要求粘贴。

完整 12 条见 [拆解读物 · 已知不足](docs/project-walkthrough.md#11-已知不足)。另有明确边界：JD 抓取不代投；语音 / ASR、支付、OAuth、多端小程序不在范围内。

---

## 文档与里程碑

| 文档 | 内容 |
| --- | --- |
| [开发计划](docs/development-plan.md) | 架构、状态机、协议、评测与里程碑的单一事实源 |
| [设计取舍](docs/design-rationale.md) | 13 节 + 附录：为什么这样写、换一种做法会怎样 |
| [拆解读物](docs/project-walkthrough.md) | 9 个模块 + 10 分钟演示脚本 + 12 条已知不足 |

已实现 M0–M9、M11–M16（M10 未做），演示页视觉轮次 R2–R5。每个里程碑的可运行验收点见[开发计划第 12 节](docs/development-plan.md#12-里程碑与验收点)。
