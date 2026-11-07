# AI 模拟面试 Agent 开发计划

> 状态：M0（设计基线 + 可编译骨架）、M1（真实存储 + 会话状态机）、M2（作答链路 + 结构化评估 + 规则裁决）、M3（真实模型 + 题纲出题 + compose.Graph）、M4（收尾与报告）、M5（Redis 会话锁 + LISTEN/NOTIFY 事件推送）、M6（评分回归集 + 批量跑分）、M7（可观测性与成本口径）、M8（最小可视化演示页）、M9（简历附件上传与本地解析）、M11（复习与间隔重复）、M12（规则版简历诊断）、M13（闯关式进度条）、M14（领域包机制）、M15（JD 采集与匹配）、M16（申请材料包）已完成；M10 未做。
> 本文档是后续实现与面试讲解的单一事实来源；代码目录、状态机、接口约定如与本文档冲突，先改文档再改代码。
> eino 尚未到 1.0，本文档刻意不写死具体版本号，版本以 `go.mod` 锁定的小版本为准。

## 0. 一页纸概览

**产品**：候选人提交简历与目标岗位 JD，Agent 进行多轮技术面试（提问 → 等待作答 → 评估 → 决定追问/下一题/收尾），结束后生成段落化复盘报告。

**核心工程问题**（也是面试讲解的主线）：

1. 一场面试跨多个 HTTP 请求，如何保证状态一致、可恢复、可重放。
2. LLM 输出不可信，如何用结构化 schema + 校验 + 修复重试兜底。
3. 如何控制追问不无限套娃，以及不把整场对话塞进报告 prompt。
4. eino 图与业务状态机、存储、HTTP 的边界怎么划，才能既用上框架又不被框架绑死。

**一句话架构**：

```
HTTP/SSE  →  应用服务（事务/幂等/锁）  →  eino 图（Agent 决策）  →  领域规则/端口  →  PostgreSQL/Redis
```

**M1 已交付范围**：PostgreSQL 作为唯一运行期事实源，落地会话状态机、命令接口（202 + run_id）、SSE 续播、幂等键与节点级崩溃恢复。

**M2 已交付范围**：作答提交（turn 落库 + 状态机推进 + 202/run_id + SSE）、结构化评估（eino `BaseChatModel` 接口 + JSON 校验/2 次修复/降级）、规则裁决（硬预算门 → 覆盖门 → 模型建议 → 终裁）、题纲与回合持久化、进程内会话锁。默认模型为确定性 `fake-heuristic`，真实模型在 M3 注入；Redis 跨进程锁在 M5 接入。

**M3 已交付范围**：真实模型装配（eino-ext OpenAI 兼容 `BaseChatModel`，`MODEL_PROVIDER` / `OPENAI_BASE_URL` / `OPENAI_API_KEY` / `OPENAI_MODEL` / `MODEL_TIMEOUT` / `MODEL_MAX_RETRIES`；无 key 自动回退 heuristic）、模型调用封装（单次超时、指数退避重试、token 计数、失败分类）、作答链路升级为 eino `compose.Graph`（snapshot → evaluate → decide → ask_followup / ask_next / END，节点级 `run_steps` 恢复不变）、题纲出题（`expected_points` 落库并贯穿评估与覆盖门、历史题去重、模型失败确定性降级）、内置 Go / MySQL / Redis 三套题纲与结构化简历画像（技能 / 项目 / 年限）。

---

## 1. 目标与非目标

### 1.1 目标

- 单场文本技术面试可完整跑通：开场 → 多轮问答 → 追问 → 收尾 → 报告。
- 每一轮的系统行为可解释：为什么问这题、为什么追问、为什么给这个分。
- 进程崩溃、客户端断线、重复提交后，会话仍能继续，且不重复计分。
- 代码按层可拆解学习：HTTP / 应用 / Agent / 领域 / 存储，每层能独立测试。
- 面试官视角可演示：SSE 实时事件、结构化评分、可追溯证据、崩溃恢复。

### 1.2 非目标

见第 13 节 YAGNI。简单说：不做语音、不做支付、不做 RBAC / 找回密码 / 第三方登录、不做小程序、不做前端大屏。

### 1.3 质量目标（可验收，不是口号）

| 目标 | 验收方式 |
| --- | --- |
| 单场 8 题面试可完成 | 集成测试用假模型跑完整状态机 |
| 评估输出永远可解析 | 100% 记录 schema 校验结果；非法输出有修复重试与失败路径 |
| 断线可续播 | SSE 带 `Last-Event-ID` 重连，事件不丢不乱序 |
| 崩溃不重复计分 | 故障注入杀进程后重启，评估条数与分数不变 |
| 追问有硬上限 | 任何配置下 `depth` / `followups_used` 不越界，单测覆盖 |
| 评分档位稳定 | 30 条回归集跑分，档位完全命中率 ≥ 80%，相邻一档 ≥ 95%（见 6.5） |
| 成本可估算 | 每场面试输出 token 数与模型调用次数摘要（不计算金额，见 10.4） |
| 报告不全量塞上下文 | 报告链路只能通过工具按需取片段，prompt 有 token 上限 |

---

## 2. 架构总览

### 2.1 分层与依赖方向

```
cmd/server
   │
   ▼
internal/api/http ──────► internal/app ──────► internal/agent（eino 图）
   │                        │                        │
   │                        ├──────► internal/domain（纯实体/规则）
   │                        │                        │
   │                        └──────► internal/ports ◄┘
   │                                     ▲
   │                                     │
   └──────────────► internal/transport/sse
                                         │
                    internal/store/{postgres,redis,memory} 实现 ports
```

**依赖规则**（代码评审时按此检查）：

| 层 | 职责 | 允许依赖 | 禁止 |
| --- | --- | --- | --- |
| `api/http` | 路由、参数校验、SSE 写出、错误映射 | `app`、`dto`、`transport/sse` | 直接调 LLM、直接开事务、直接写 SQL |
| `app` | 用例编排、事务边界、幂等、锁、状态提交、事件发布 | `domain`、`agent`、`ports` | 拼 prompt、操作 eino 类型、处理 HTTP 细节 |
| `agent` | eino 图、prompt、模型调用、输出解析、工具定义 | `domain`、`ports`、eino | 依赖 `api`、直接读写数据库、依赖 HTTP |
| `domain` | 实体、状态迁移规则、评分规则、追问策略、预算 | 标准库 | eino、SQL、Redis、HTTP |
| `ports` | 仓储/模型/事件接口（由内向外定义） | `domain` | 具体实现 |
| `store` | PostgreSQL / Redis / 内存实现 | `ports`、`domain` | 反向依赖 `app`/`api` |
| `platform` | 配置、日志、ID、时钟等横切能力 | 标准库 | 业务规则 |

关键点：**eino 类型只在 `agent` 内部出现**。`domain` 与 `api` 不出现 `schema.Message`、`compose.Runnable`，避免框架升级波及全仓。

### 2.2 eino 的使用边界

采用官方 examples 里最常见的稳定组合：

- `compose.NewGraph[I, O]` 组装「加载快照 → 评估 → 决策 → 生成问题」的有向图。
- `compose.ChatTemplate` 组装 system / 历史摘要 / 当前问题 / 候选人答案。
- `model.ChatModel`（`Generate` / `Stream`）做模型调用，测试时替换为假模型。
- `schema.Message` 只在 agent 层内部流转。

**刻意不用**（MVP 阶段）：

- eino 的 ADK / agentic 新抽象：API 仍在演进，学习成本高，且会把 Agent 生命周期锁进框架。
- eino `ToolsNode` 自主工具循环：报告回查由代码固定执行 3 次只读调用，模型不绑定工具，调用次数可审计。
- eino 的 interrupt/resume、checkpoint 做人类等待：面试的「等待作答」天然跨请求，框架级挂起会把长连接、事务、恢复三者耦合，难排障。
- 预置的 ReAct Agent / 多 Agent 协作：本场景是单 Agent 有限状态循环，自己写状态机更可控。

**版本隔离策略**：`go.mod` 锁定 eino 小版本；升级时先跑 `agent` 层的契约测试（图可编译、结构化输出可解析、工具可调用），再合入。本文档不写死版本号。

### 2.3 关键取舍（ADR）

| 编号 | 决策 | 理由 | 代价 |
| --- | --- | --- | --- |
| ADR-001 | **回合制短图**：每次 HTTP 命令触发一次 eino 图执行，执行完即结束；不用长驻 Agent runtime | 人类作答是跨请求的；短图无内存驻留状态，崩溃恢复简单；每回合可独立重试、测试 | 需要自己维护会话状态机与步骤日志 |
| ADR-002 | **控制面 POST + 数据面 SSE**：命令用 JSON POST 返回 `202 + run_id`，进度从独立 SSE 端点订阅 | 断线重连不影响执行；多标签页可共享；命令重试天然幂等 | 前端要维护两个连接 |
| ADR-003 | **自管状态机 + 自管步骤日志**，不用框架 checkpoint | 恢复粒度可控到「节点产物」；不依赖未稳定 API；状态可查可审计 | 需要设计 `runs` / `run_steps` 表 |
| ADR-004 | **结构化列 + JSONB 混合**：会话/评分核心字段建列，证据、分数、配置用 JSONB | 便于查询与约束，同时保留 LLM 输出的灵活结构 | JSONB 内容需应用层校验 |
| ADR-005 | **报告用工具回查**，不拼全量 transcript | 控制 token、降低幻觉、报告结论可引用 turn_id | 固定采集计划由代码执行，模型不主动调用工具 |

---

## 3. 会话状态机

### 3.1 标识符体系

| 标识 | 生成 | 作用 |
| --- | --- | --- |
| `session_id` | ULID | 一场面试的唯一标识，所有数据按它聚合 |
| `run_id` | ULID | 一次图执行的标识：开场、每次作答、报告生成各一次 |
| `turn_id` | ULID | 一道题（含追问）及其回答的记录单元 |
| `event_id` | PostgreSQL `bigserial` | SSE 事件游标，全局单调递增；按 session 过滤后仍保序 |
| `state_version` | 会话内自增整数 | 乐观并发控制；每次提交状态 +1 |
| `idempotency_key` | 客户端 Header 或服务端派生 | 防重复提交/重放 |

**幂等键设计**：

- 客户端可传 `Idempotency-Key`；未传时服务端按 `sha256(action | session_id | turn_id | state_version | canonical_body)` 派生。
- 唯一约束落在 `runs(session_id, kind, idempotency_key)`，并以 `request_hash` 校验「同一个 key 是否复用于不同请求」。
- 重复请求的处理：
  - 已成功 → 返回原 `run_id` 与结果，响应头 `Idempotent-Replayed: true`。
  - 执行中 → `409 run_in_progress`，带上 `run_id`，客户端继续订阅 SSE。
  - 已失败 → 返回原失败结果；需要重试时走显式 `POST /runs/{run_id}/retry`，避免隐式重放掩盖问题。
- Redis 的 `idem:*` 只是快路径缓存，**正确性以 PostgreSQL 唯一约束为准**。

### 3.2 状态与阶段

**会话粗状态（`sessions.status`）**：

```
created ──start──► active ──收尾──► reporting ──成功──► completed
   │                 │                  │
   └──abort──────────┴──────abort───────┴──► aborted
                      │
                      └──不可恢复错误──► failed
```

**`active` 内的稳定阶段（`sessions.phase`）**：

- `awaiting_answer`：已抛出问题，等待候选人作答。**唯一允许提交答案的阶段**。
- `ready_to_ask`：上一回合已完成，等待/正在生成下一题（通常是瞬时态，持久化用于恢复）。
- `ready_to_finalize`：题目或追问预算耗尽，等待生成报告。

**瞬时执行阶段不落会话表**，只存在于 `runs` / `run_steps`：

```
evaluating → deciding → asking
```

这样崩溃后不会出现「会话说自己在 evaluating，但其实什么都没在执行」的歧义。

### 3.3 主循环状态迁移

```
                 ┌─────────────────────────────────────────────┐
                 │                                             │
created ──start──► active(awaiting_answer) ◄──asking──┐        │
                 │                                     │        │
                 │ submit answer                       │        │
                 ▼                                     │        │
          run: evaluating                              │        │
                 │                                     │        │
                 ▼                                     │        │
          run: deciding ──┬── follow_up / next_question ┘        │
                         │                                      │
                         ├── finish ──► active(ready_to_finalize)
                         │                                      │
                         ▼                                      │
                 active(awaiting_answer)                        │
                                                                │
active(ready_to_finalize) ──report──► reporting ──► completed ◄─┘
```

**迁移表**：

| 当前 | 事件/命令 | 前置条件 | 结果 |
| --- | --- | --- | --- |
| `created` | `POST /start` | 简历/JD 非空，配置合法 | 生成题纲与第一题 → `active/awaiting_answer` |
| `active/awaiting_answer` | `POST /answers` | `turn_id` 等于 `pending_turn_id`，答案非空 | 执行 AnswerRun → 追问/下一题/收尾 |
| `active/awaiting_answer` | `POST /finish` | 无 | 同一事务内 `Finish` + `BeginReport` → `reporting`，直接生成报告 |
| `active/ready_to_finalize` | `POST /finish` 或 `POST /report` | 至少 1 题已回答（`/report` 要求已到该阶段） | 执行 ReportRun → `reporting` → `completed` |
| 任意非终态 | `POST /abort` | 无 | `aborted`，幂等 |
| `reporting` | 报告执行失败 | 仅 ctx 取消/存储错误 | 保持 `reporting`，run 记为失败，可重试；模型失败走降级报告而非失败 |
| `reporting` | 报告提交成功 | 单事务内完成落库与事件 | `completed`（终态） |

**不变量**（单测 + 数据库约束共同保证）：

1. 同一 session 同时最多一个 `running` 的 run（Redis 锁 + DB 状态检查）。
2. `active/awaiting_answer` 时最多一个未作答的 `pending_turn_id`。
3. `state_version` 只增不减；所有状态提交走 CAS。
4. 追问计数只增不减；`depth ≥ 1`，且不超过配置上限。
5. 已 `completed` 的 session 不接受任何写命令。
6. 每次状态提交必须同时写事件，事件与业务数据在同一事务内。

### 3.4 并发控制

- 跨进程：Redis `SET lock:session:{id} <token> NX PX <LOCK_TTL>`，默认 TTL 10s；释放用 Lua 比较 token 后删除，过期或换主后旧持有者无法误删。
- 抢锁：默认最多等待 3s（`LOCK_ACQUIRE_TIMEOUT`，每 50ms 重试），仍失败返回 `ports.ErrLockBusy` → HTTP `409 lock_busy`（`retryable=true`），不静默丢弃命令。
- 重入：同一 token 再次 `Acquire` 只刷新 TTL，返回成功；不做嵌套计数，一次 `Release` 即释放。不同 token 一律互斥。app 层每个命令只取一次锁，不做嵌套。
- 进程内：`platform/lock.ProcessLocker` 仅用于单测与 Redis 不可达时的单实例降级；运行期默认注入 Redis 实现。
- 数据库兜底：`UPDATE sessions ... WHERE state_version = $2` 的 CAS、`turns` 的 `UPDATE ... WHERE status='pending'`、`(session_id, seq)` 与 `runs(session_id, kind, idempotency_key)` 唯一约束；即使锁失效也不会产生双写。
- Redis 不可达：`cmd/server` 启动时 Ping 失败会记录 Warn 并回退进程内锁；**多实例正确性必须以 Redis 可达为前提**，正式环境需按 docker-compose 路径验证。

### 3.5 崩溃恢复

核心思路：**恢复到「最后一个成功节点」，而不是恢复模型输出流**。

1. 每个 run 的每个节点执行前写 `run_steps(status=running, attempt++)`，执行成功后在同一事务写 `output_json + status=succeeded`，并把业务投影（turn / evaluation / session 状态）与事件一起提交。
2. 若进程在节点执行中崩溃：该 step 仍是 `running`。重启后由 reconciliation 把超过心跳时间（60s）的 run 标为 `interrupted`，step 标为 `failed`。
3. 恢复时从 `run_steps` 中第一个非 `succeeded` 的节点开始：
   - 已有 `output_json` 的节点直接回放，不再调用模型；
   - 没有产物的节点用存储的输入重新执行（模型调用可能重复，但业务写入幂等）。
4. **模型 token 流不可恢复**：已流出的半截问题文本直接丢弃，重新生成选题；SSE 的 `question.delta` 只用于展示，不参与持久化。
5. 恢复入口：
   - 服务启动时扫描 `interrupted` 的 run，标记并在 SSE 发出 `run.interrupted`；
   - 客户端用同一幂等键重发命令，或显式 `POST /runs/{run_id}/retry` 续跑。
6. **不重复计分**：`evaluations.turn_id` 主键 + `run_steps` 回放 + 状态 CAS 三重保证；同一 turn 的评估写入是 upsert 且校验 `state_version`。

### 3.6 超时与失败

| 对象 | 超时 | 处理 |
| --- | --- | --- |
| 单次模型调用 | 30s | 可重试错误重试，超时计入 run 预算 |
| 单次 AnswerRun | 90s | 失败保留 `awaiting_answer`，答案已落库，可重试评估 |
| 单次 ReportRun | 180s | 失败可重试；仍失败则生成确定性模板报告兜底 |
| SSE 连接 | 30min 上限 | 发 `session.completed` 或心跳；客户端可重连 |
| 整场面试 | 配置的 `duration_minutes` | 决策门在超时后强制 `finish` |

---

## 4. 主循环与 eino 图

### 4.1 三个 Run

| Run | 触发 | 图的输入 | 图的输出 | 是否流式 |
| --- | --- | --- | --- | --- |
| `StartRun` | `POST /start` | 简历、JD、配置 | 题纲 + 第一题 | 否（步骤事件） |
| `AnswerRun` | `POST /answers` | session 快照、当前 turn、答案 | 评估 + 决策 + 下一题（或收尾信号） | 评估不流式；问题可 token 流式 |
| `ReportRun` | `POST /report` | session 快照、评分摘要 | 报告段落 + 汇总 | 按段落流式 |

### 4.2 AnswerRun 节点图

```
START
  │
  ▼
snapshot            读取 session/turn/题纲/近期题目/画像（只读，无模型）
  │
  ▼
evaluate            ChatModel.Generate + 结构化输出解析 + 校验/修复
  │
  ▼
decide              规则门（预算/超时/不会/覆盖）→ LLM 建议 → 终裁
  │
  ├─ follow_up ──────▶ ask_followup ──▶ END
  ├─ next_question ──▶ ask_next ──────▶ END
  └─ finish ─────────────────────────▶ END
```

**节点契约**：

| 节点 | 输入 | 输出 | 失败语义 |
| --- | --- | --- | --- |
| `snapshot` | `TurnInput` | `TurnSnapshot` | 读不到 → 不可重试，run 失败 |
| `evaluate` | `TurnSnapshot` | `Outcome` | 模型错误可重试；schema 非法走修复；修复失败 → 不落评分、决策降级推进 |
| `decide` | `Outcome` + 预算 | `Decision` | 纯规则不失败；模型建议只是建议，不改变硬门 |
| `ask_followup` | `TurnSnapshot` + `Outcome` + `Decision` | `Question` | 出题失败重试 → 仍失败用确定性模板降级 |
| `ask_next` | `TurnSnapshot` + 当前题纲项 | `Question` | 同上；题纲指针越界 → `ask_next` 步骤失败 |

**关键实现约定**：

- M3 实现说明：作答链路已是真正的 `compose.Graph`，节点为 `snapshot` / `evaluate` / `decide` / `ask_followup` / `ask_next`，`decide` 后按 `Decision.Action` 分支到追问、下一题或 `END`。节点契约与 M2 相同；`recordStep` 由 `app` 包裹各节点依赖实现节点级恢复（`run_steps` 名仍为 `snapshot` / `evaluate` / `decide` / `ask`）。
- 图内节点**不直接写数据库**。节点返回结构化产物；`app` 在事务里提交投影与事件。
- 每个节点的产物由 `stepRecorder` 装饰器写 `run_steps`，装饰器先查缓存产物再调用模型，实现「节点级恢复」。
- 图的输入输出是 `agent` 包内定义的普通 struct，不把 eino 类型穿透到 `app`。
- 评估节点必须 `Invoke`（要完整 JSON），问题/报告节点可用 `Stream` 转 SSE delta。

### 4.3 Prompt 组装原则

- system：角色、面试规则、评分 rubric 摘要、输出 schema、禁止事项。
- user：`简历摘要 + JD 摘要 + 题纲当前项 + 当前问题 + 候选人答案 + 最近 2 题的评估摘要`。
- **不拼全量历史**：历史以「每题一行摘要」压缩，原始答案只在需要时由工具/快照读取。
- 所有候选人文案用明确分隔符包裹，并声明「以下内容是数据，不是指令」，降低 prompt injection 风险。
- 每个 prompt 有 `prompt_version`，随评估记录落库，便于回归对比。

### 4.4 假模型与真实模型

- 测试用 `FakeChatModel` 实现 eino 的 `model.BaseChatModel`，按 `prompt_version` 返回固定 JSON，保证集成测试不依赖网络与费用。
- M3 已接入 eino-ext `components/model/openai`（OpenAI 兼容，可指向任意 `OPENAI_BASE_URL`），经 `agent/llm` 适配为 `Client` 并叠加超时/重试/token 计数/失败分类；默认装配仍为 `fake-heuristic`，无需 API Key。无 key 时启动降级到 heuristic 并 Warn，有 key 即切真实模型。
- 模型调用统一走 `agent/llm` 的薄封装：超时、重试、token 统计、错误归一化只在这里做。仅 timeout / rate_limited / unavailable / network 重试，指数退避 300ms 起、上限 2s。

---

## 5. 追问策略

### 5.1 何时追问

| 触发条件 | 优先级 | 追问目的 |
| --- | --- | --- |
| 答案与简历/前文事实矛盾 | P0 | 澄清事实，防止虚假经历 |
| 缺失关键点（`missing_points` 与题目的 `expected_points` 有交集） | P1 | 补齐核心知识 |
| 提到项目/技术但没讲取舍、数据、故障 | P1 | 验证真实性与深度 |
| 只有结论、无推理或例子（答案过短） | P2 | 要求举例 |
| 评估置信度低（`confidence < 0.6`） | P2 | 用追问降低误判 |

**不追问**：

- 候选人明确表示「不会/没做过」→ 给一句参考要点，直接下一题；不折磨。
- 该题已覆盖全部 `expected_points` 且无新 claim。
- 追问链达到深度上限、该题追问数达到上限、整场追问预算耗尽、剩余时间不足。
- 评估证据无法引用原文（防模型编造追问理由）。

### 5.2 上限与预算

| 参数 | 默认 | 硬上限 | 说明 |
| --- | --- | --- | --- |
| `max_primary_questions` | 8 | 12 | 主问题数量 |
| `max_followups_per_question` | 2 | 3 | 单条主问题链的追问数 |
| `max_depth` | 3 | 4 | 主问题 depth=1，追问 depth+1 |
| `max_total_followups` | 6 | 10 | 整场追问总数 |
| `max_session_minutes` | 45 | 60 | 超时强制收尾 |
| `max_answer_chars` | 6000 | — | 超长答案截断并记录标记 |

### 5.3 决策权与防无限套娃

**规则优先，模型只做建议**：

1. 硬门（纯规则，顺序固定）：超时 → `finish(time_limit)`；题纲耗尽 → `finish(plan_exhausted)`；`followups_used ≥ max_total_followups` → `next_question(followup_budget_exhausted)`；`depth ≥ max_depth` → `next_question(depth_limit)`；该题追问数达到上限 → `next_question(question_followup_limit)`；检测到「不会」→ `next_question(answer_unknown)`；评估降级 → `next_question(evaluation_degraded)`。
2. 覆盖门：证据 reason 已按原文覆盖本题全部 `expected_points` → `next_question(coverage_satisfied)`。**覆盖是确定性门，命中即推进，不再要求模型未建议追问**（模型建议只是建议）；M3 起 `expected_points` 来自内置题纲，覆盖判定对中文/空格/标点做归一化匹配。
3. 模型建议：`followup.needed + 非空 target + 至少一条合法 evidence`。
4. 终裁：仅当模型建议满足第 3 条且所有硬门放行 → `follow_up(model_suggestion)`；否则 `next_question(no_followup_signal)`。
5. 降级：任何本应 `next_question` 的分支在题纲耗尽时自动转为 `finish(plan_exhausted)`，保证「恰好消费一个答案」。

**循环不变量**：

- 每个 `AnswerRun` **恰好消费一个答案**，并且必须三选一：`followups_used+1` / `题纲指针+1` / `finish`。不存在「什么都不变」的路径。
- eino 的 AnswerRun 图是**有向无环**的；跨请求的循环由状态机与预算控制，不靠图里回边。
- `turn.depth` 与 `session.followups_used` 落库并在决策前重新读取，模型无法通过输出篡改预算。
- 幂等键保证同一答案不会触发第二次决策，从而不会凭空多出追问。

### 5.4 追问示例

- 候选人：「用了 Redis 缓存，性能提升很大。」→ 追问：「提升体现在哪个指标？缓存击穿/穿透怎么处理？」（P1，取证）
- 候选人：「消息队列保证消息不丢。」→ 追问：「你用的是哪种确认机制？消费失败后的重试与幂等怎么做的？」（P0，矛盾/过度断言）
- 候选人：「不知道。」→ 「这题先过，参考方向是 X/Y；下一题……」→ 不追问。

---

## 6. 评估 Rubric

### 6.1 维度与权重

| 维度 | 权重 | 1 分锚点 | 3 分锚点 | 5 分锚点 |
| --- | --- | --- | --- | --- |
| `correctness` 正确性 | 0.30 | 关键概念错误 | 基本正确但有小错 | 准确且能指出边界 |
| `depth` 深度 | 0.25 | 只背定义 | 能讲实现原理 | 能讲权衡、失败模式、演进 |
| `engineering_judgment` 工程判断 | 0.20 | 无取舍意识 | 能说明一种取舍 | 能对比多方案并给适用条件 |
| `structure` 结构表达 | 0.15 | 跳跃、无重点 | 有条理但略啰嗦 | 结论先行、层次清晰 |
| `evidence_credibility` 证据可信度 | 0.10 | 只有「我做过」 | 有具体模块/数据 | 有可核验细节与反思 |

`depth` 使用可执行的四要素锚点（`eval-v4` 起，详见 [design-rationale.md](design-rationale.md) 第 9 节）：

- **5 分 = 机制 + 边界 + 取舍 + 验证**，四项齐备，每项至少一句可定位原文。
- **4 分 = 取舍成立，且边界或验证至少有一项**；另一项缺失或只提一句。
- **3 分 = 无取舍，或边界与验证都缺**；机制只覆盖局部环节。
- **2 分 = 只有结论或只背定义**，几乎没有机制。
- **1 分 = 结论错误、答非所问或纯名词堆砌**。

判定口径：机制 = 内部结构 / 控制流；边界 = 前提、失效条件、异常路径或参数边界；取舍 = 在两个可行做法间选择并说明为什么不选另一个（只并列方案、只列补救措施不算）；验证 = 基线 + 指标 + 窗口 / 来源，或实验 / 线上结果（只提监控 / trace / pprof / gcflags 不算）。硬上限：无取舍 → 3；无边界 → 4；无验证 → 4；边界与验证都无 → 3。非 `depth` 维度统一 1–5 档（补齐 2 / 4），5 分锚点按维度拆开：`correctness` 允许没有取舍，`structure` / `evidence_credibility` 不要求取舍。

### 6.2 证据 / 加分 / 扣分

评估结果不是「总分一个数字」，而是可解释结构：

- `evidence[]`：每个维度必须引用候选人原文（`quote`）并说明为何给该分。证据不足 → 置信度下降。
- `bonus[]`：量化结果、真实故障复盘、清晰取舍、主动指出局限。每条 `+1 ~ +3`，总分上限 `+8`。
- `penalty[]`：事实矛盾、无法验证的夸大、术语堆砌、答非所问。每条 `-1 ~ -3`，总分下限 `-10`。
- `missing_points[]`：本题应覆盖但未覆盖的点，直接喂给追问策略。
- 维度分与加减分**分开计算**，避免同一件事既扣维度分又扣 penalty 的双重惩罚。

### 6.3 结构化输出 Schema

```json
{
  "schema_version": 1,
  "turn_id": "01H...",
  "scores": {
    "correctness": 4,
    "depth": 3,
    "engineering_judgment": 3,
    "structure": 4,
    "evidence_credibility": 3
  },
  "evidence": [
    {
      "dimension": "correctness",
      "quote": "消费失败后进入重试队列，超过 3 次进死信",
      "reason": "说明了失败处理路径，但未提幂等"
    }
  ],
  "bonus": [
    {"type": "quantified_result", "quote": "P99 从 800ms 降到 120ms", "points": 2, "reason": "有前后对比"}
  ],
  "penalty": [
    {"type": "unsupported_claim", "quote": "我们扛住了 10 万 QPS", "points": -3, "reason": "无压测或监控证据"}
  ],
  "confidence": 0.82,
  "missing_points": ["消费幂等键的设计"],
  "followup": {
    "needed": true,
    "target": "消息消费幂等如何保证",
    "reason": "答案提到重试但未解释重复消费"
  }
}
```

**校验规则**（任何一条不满足都视为非法输出）：

1. JSON 可解析，字段齐全，`scores` 在 1–5，`confidence` 在 0–1。
2. `evidence.quote` 必须是答案原文的子串（空白/标点归一化后匹配）；匹配失败则丢弃该证据。
3. 被丢弃证据超过一半，或 `bonus/penalty` 缺少 `quote` → 触发修复重试。
4. `followup.target` 必须与 `missing_points` 或 `penalty` 有关联，否则不采纳。
5. 修复重试最多 2 次（把校验错误回灌给模型）；仍失败则评估记录标记 `degraded=true`、`error_code=model_output_invalid`、**不编造分数**，发出 `evaluation.degraded` 事件，规则裁决按预算继续（下一题或收尾），不阻塞面试。模型传输错误（超时/上游不可用）则 run 失败可重试。
6. `turn_id` 由服务端以当前 turn 覆盖，模型输出中的 `turn_id` 不作为事实源。

**计分公式**：

```
base = Σ(score_i / 5 × weight_i) × 100
total = clamp(base + Σbonus + Σpenalty, 0, 100)
```

### 6.4 分批评估与二次汇总

| 层级 | 时机 | 输入 | 输出 |
| --- | --- | --- | --- |
| L1 单题评估 | 每次作答 | 当前问题 + 答案 + rubric | `Evaluation`（落库，SSE 推送摘要） |
| L2 主题批次 | 主题切换时 | 该主题 2–4 条 L1 结果 + 代表性证据 | `TopicSummary`（压缩 + 主题均分） |
| L3 终局汇总 | 报告生成前 | L2 摘要 + 最低/最高分题 L1 + 会话统计 | 总评草稿 |

- **两遍汇总**：第一遍生成总评；第二遍做一致性检查（权重是否用对、总分与维度是否矛盾、证据是否可追溯），不一致则只重跑汇总，不重跑单题。
- **token 控制**：L3 输入默认不超过 6k tokens，只放摘要与被选中的证据片段。
- **增量性**：新题产生后只更新对应主题摘要；最后汇总可重算，不重复调用单题评估。
- **可追溯**：所有汇总结论保留 `source_turn_ids`，报告必须能点回原题。

### 6.5 评测方案（如何证明评分可靠）

目标：让「评分可靠」成为可重复执行的验收，而不是每次手工抽几条看看。

**1. 小规模回归集**

- 20 条代表性作答（上限 40 条），覆盖：五个维度各档位、优秀/中等/较差、超短答案、「不会」、超长跑题、含夸大 claim、含 prompt injection 文本。
- 每条固定字段：`case_id`、`question`、`answer`、`expected_band`（总分档位：1–2 / 3 / 4–5）、`expected_missing_points`（可选）、`notes`（人工判断理由）。
- 数据集放 `internal/agent/eval/testdata/regression.json`，随代码版本管理，不入库、不做标注平台。M6 已落地 30 条，类型分布为 high 8 / mid 11 / low 4 / empty 2 / exaggerated 3 / off_topic 2；`expected_band` 分布为 5 档 8 条、3 档 11 条、1 档 11 条。
- M6 追加了 `expected_scores`（五维人工分），用于 `dimension_mae`；加载时会校验「`expected_scores` 反推的档位必须等于 `expected_band`」，防止标注自相矛盾。
- M6 档位换算：总分 0–100 → 1 档 `<50`、3 档 `50–79.9`、5 档 `≥80`，由 `evaluation.Band` 实现；切点对应 1–5 分制上的 2.5 与 4.0。

**2. 锚点与少样本**

- rubric 的 1/3/5 分锚点（见 6.1）直接写进 prompt，作为唯一刻度；锚点只描述可观察行为，不写主观词。
- 每个档位配 1 条 few-shot 示例（答案 + 期望 JSON 输出），示例与 prompt 同版本管理；改示例即视为改 rubric。
- 证据必须引用原文，锚点示例同样遵守「quote 必须是答案子串」。

**3. 温度与输出 schema 校验**

- 评估节点固定 `temperature=0.1`、`top_p=0.9`；同一 case 连续跑 3 次，取档位众数用于稳定性统计。
- 输出必须通过 6.3 的 schema 校验与证据子串校验；失败计入 `schema_invalid`，不进入命中率分母。
- 每次评估落库 `model_name`、`prompt_version`、`raw_output`、token 数，保证任何分数可复现。

**4. 批量跑分与命中率**

- 命令（M6 落地）：
  - 主命令：`go run ./cmd/evalrunner`（无 key 自动 heuristic；有 key 自动真实 provider；也可 `-provider heuristic|openai` 显式指定）。
  - go test 子集：`go test -tags=eval -count=1 -v ./internal/agent/eval/...`，只硬校验 `schema_valid_rate` / `evidence_quote_rate`，并打印基线明细；档位门槛由 CLI 强制执行。
  - 结果文件写入 `docs/eval-results/`，文件名含 provider / 模型名 / prompt 版本 / UTC 时间戳；内容含 `dataset_sha256`、值档位明细、偏差维度、token 与耗时，便于前后对比。门槛不达标时 CLI 退出码为 2。
- 指标与门槛：
  - `schema_valid_rate` = 100%（硬门槛）。
  - `band_hit_rate` ≥ 80%；相邻一档命中率 ≥ 95%（允许 ±1 档抖动）。
  - `dimension_mae` ≤ 0.6（各维度与人工标注分的平均绝对误差）。
  - `evidence_quote_rate` = 100%（由输出校验保证，跑分再复核）。
  - `cost_per_case`：单 case token 与耗时，作为成本基线（口径见 10.4）。
- 输出 `expected_band × actual_band` 混淆矩阵；失败 case 打印 `case_id` 与差异，指标不达标时命令非零退出。

**5. rubric 迭代流程**

- 改 rubric / prompt / few-shot 后必须重跑同一回归集，并与上一版对比命中率与混淆矩阵。
- 合入门槛：`band_hit_rate` 不下降且 `dimension_mae` 不上升；有争议的 case 人工复核后写进 `notes`。
- 线上人工抽查发现的新失败样本补进回归集；M19 起固定为 60 条（Go 后端 35 / AI Agent 25），新增样本替换而非无限追加，避免跑分成本失控。
- 每次迭代更新 `prompt_version`，历史评估可按版本回放对比。

**5.1 M6 基线实测（heuristic，`fake-heuristic` / `eval-v2` / 30 条）**

| 指标 | 实测 | 门槛 | 结果 |
| --- | --- | --- | --- |
| `schema_valid_rate` | 100%（30/30） | 100% | 通过 |
| `band_hit_rate` | 73.3%（22/30） | ≥80% | 未达标 |
| 相邻档命中率 | 93.3%（28/30） | ≥95% | 未达标 |
| `dimension_mae` | 0.66 | ≤0.60 | 未达标 |
| `evidence_quote_rate` | 100% | 100% | 通过 |

失准 8 条：`mid-02-context`、`mid-09-goroutine-leak`（中档但长度 ≥120）、`low-01-channel`（43 字跨过 40 字阈值）、`empty-02-fluff`（62 字纯套话）、`exag-01-cache-scale`、`exag-02-mysql-scale`、`off-01-k8s`、`off-02-business`。

归因：heuristic 是无语义的长度分档器（`<40`→2 分、`40–119`→3 分、`≥120`→4 分），因此「长但空洞 / 长但夸大 / 长但跑题」必然被抬高，中档长回答也会越档；`schema` 与证据复核均为 100%，说明失准不是证据校验过严，也不能靠调 rubric 权重修复。`expected_band` 保持人工标注不变。真实 provider 的同一跑分需配置 `OPENAI_API_KEY` 后执行 `go run ./cmd/evalrunner -provider openai`，再判断是否存在 prompt 锚点不足的问题。

**5.2 真实模型基线实测（`openai` / `deepseek-chat`→`deepseek-flash` / `eval-v3` / 60 条）**

2026-11-07 22:43（北京时间）一次跑完 60 条，未逐条重试；请求模型名为 `deepseek-chat`，供应商实际返回 `deepseek-flash`（DeepSeek-V4.1-Flash-1017），结果文件按请求名记录 `deepseek-chat`。数据文件 `internal/agent/eval/testdata/regression.json`，`sha256=e5d0084639e376e0f1149de4441d36223ebc843da853a7704270b8bb44011e9e`；结果文件 `docs/eval-results/eval-openai-deepseek-chat-eval-v3-20261107T134323Z.json`。60 次调用全部一次成功（`attempts=1`），0 降级、0 错误，平均 3739 ms/条。

| 指标 | 实测 | 门槛 | 结果 |
| --- | --- | --- | --- |
| `schema_valid_rate` | 100%（60/60） | 100% | 通过 |
| `band_hit_rate` | 81.7%（49/60） | ≥80% | 通过 |
| 相邻档命中率 | 100%（60/60） | ≥95% | 通过 |
| `dimension_mae` | 0.4967 | ≤0.60 | 通过 |
| `evidence_quote_rate` | 100% | 100% | 通过 |

分领域：Go 后端 82.9%（29/35）、AI Agent 80.0%（20/25）。11 条失准：`high-01-gmp`、`high-04-local-message`、`high-05-kafka-idempotent`、`high-07-innodb-lock`、`high-09-escape`、`ai-high-01-rag-pipeline` 均为 5→3（模型偏严，主要压 `depth` / `evidence`）；`unverified-01-perf`、`ai-unverified-01-accuracy` 为 3→1（指标无出处被正确压档，但压过头）；`ai-jargon-02-ann`、`ai-conclusion-02-cost` 为 3→1；`ai-mid-01-embedding` 为 3→5（反向高估 1 档）。期望值未做任何修改。

与 `eval-v2` 30 条基线 A/B（`cmd/evaldiff` 与 `/eval?base=&candidate=`）：`band_hit_rate` 0.8000→0.8167（+1.7pt），`dimension_mae` 0.4333→0.4967（+0.0634，仍达标），其余三项持平；两侧共有的 30 条中 2 条翻转，均为改进（`high-06-rate-limit` 3→5、`high-08-cpu-profile` 3→5），无回退，新增的 30 条只出现在候选侧。

成本口径：60 条实际消耗输入 54,741 tokens、输出 33,966 tokens、合计 88,707 tokens（另有 1 次模型连通性探测 5/1 tokens，总计 54,746 + 33,967 = 88,713）。按 DeepSeek `deepseek-flash` **peak 单价**（输入缓存未命中 $0.3/百万、输出 $1.2/百万；人民币 ¥2 / ¥8）折算约 **$0.0572 / ¥0.3812**。本次实际在北京时间周六晚（off-peak，单价减半），按 off-peak 折算约 **$0.0286 / ¥0.1906**。金额仅按官方牌价估算，未计入缓存命中折价。

**5.3 真实模型 v4 实测（`openai` / `deepseek-chat`→`deepseek-flash` / `eval-v4` / 60 条）**

2026-11-07 22:32–22:37（北京时间）一次跑完 60 条，未逐条重试、未挑 case；全部 `attempts=1`，0 降级、0 错误；80 次调用硬上限只用了 60 次。数据集 `sha256=62c4c9cfae1d4712f1a9f421654e55ec0a759061446bc5057eb6ede0618a2589`；结果文件 `docs/eval-results/eval-openai-deepseek-chat-eval-v4-20261107T143259Z.json`，v3 结果保留在同目录。

| 指标 | 实测 | 门槛 | 结果 |
| --- | --- | --- | --- |
| `schema_valid_rate` | 100%（60/60） | 100% | 通过 |
| `band_hit_rate` | 80.0%（48/60） | ≥80% | 通过（较 v3 −1.7pt） |
| 相邻档命中率 | 100%（60/60） | ≥95% | 通过 |
| `dimension_mae` | 0.4733 | ≤0.60 | 通过（较 v3 改善 0.0234） |
| `evidence_quote_rate` | 100% | 100% | 通过 |

分领域：Go 后端 82.9%（29/35，持平）、AI Agent 76.0%（19/25，较 v3 少 1 条）。

与 v3 同集 A/B：实际档位翻转 2 条，**均为回退**（`ai-mid-03-memory` 3→5，79→81，仅 evidence 3→4；`ai-mid-07-context-budget` 3→5，76→81，depth 3→4），无改进翻转。标注修正使 `ai-high-01-rag-pipeline` 的期望档由 5 改为 3 并转为命中，但模型实际档与总分（74）未变，不计为模型改进。净效应为 **命中率变差 1 条、MAE 改善 0.0234**；60 条中有 18 条实际五维分变化，但多数仍在同档内。v4 不能记为提升。

成本口径：60 次调用消耗输入 54,741 tokens、输出 34,222 tokens、合计 88,963 tokens，平均 4,175.7 ms/条。按 `deepseek-flash` peak 单价（输入 $0.3/百万、输出 $1.2/百万；¥2 / ¥8）约 **$0.0575 / ¥0.3833**；按 off-peak 半价折算约 **$0.0287 / ¥0.1916**。本次运行在 22:32–22:37，按官方错峰窗口（北京时间 00:30–08:30）不在优惠时段，实际出账应按 peak 计；5.2 将周六晚直接记为 off-peak 与官方时段不一致，此处两个口径并列，账单以实际出账为准。

**6. 明确不做**

- 不做人工标注平台、标注团队流程；回归集由开发者自己维护。
- 不做在线 A/B 实验、不做统计显著性检验（样本量不足以支撑）。
- 不做 LLM-as-judge 的二次自动裁判（会引入新的不可解释性）；一致性检查只用规则与确定性校验。
- 不做模型微调，不针对回归集做过拟合式 prompt hack。
- 不承诺「同一答案每次分数完全一致」，只承诺档位稳定与证据可追溯。

---

## 7. 报告生成

> M4 实现说明：本期落地的是「确定性选材 + 单次生成 + 确定性降级」，不是 eino `ToolsNode` 自主工具循环，也没有逐段 critic。工具调用次数固定可审计，报告一定产出，符合演示终点的目标；原计划中的「模型自主多轮工具检索 + 逐段引用校验」留作后续增强，不影响现有契约。

### 7.1 链路

```
POST /finish 或 /report
   │
   ▼
单事务：active(ready_to_finalize|awaiting + Finish) → reporting
         Runs.Create(kind=report) + session.phase + run.started + report.started
   │
   ▼
ReportRun（同步或异步执行）
   ├─ collect   tools.Collect：固定 3 次只读回查（overview / weak / search）
   ├─ generate  一次模型调用 → 解析并校验 JSON；失败/非法 → 确定性降级
   └─ commit    单事务：session.Complete → reports 落库 → report.section ×5
                → report.tool_calls → report.completed → session.phase
                → session.completed（终态）
```

### 7.2 用工具回查，而不是塞全量对话

工具清单（`internal/agent/tools`），全部只读、绑定单一 `session_id`：

| 工具 | 参数 | 返回 |
| --- | --- | --- |
| `get_session_overview` | 无 | 岗位、难度、题数、综合分、5 维平均分、逐题 `turn_id`/topic/score/缺失点数、弱主题 |
| `search_turns` | `topic? / query? / min_score? / max_score? / limit ≤ 8` | 已答题摘要与答案片段（按结果上限截断） |
| `list_weak_turns` | `limit ≤ 10` | `score ≤ 70` 且非 degraded 的低分题 |
| `get_turn` | `turn_id` | 单题问答 + 评估 + 证据；答案按结果上限截断。已实现，但报告固定采集计划未调用 |

约束与校验：

- 归属校验：`Query` 创建时绑定 session，调用方不能覆写；`get_turn` 对空 id 返回 `ErrInvalidArgument`，对未知/跨 session 的 turn 返回 `ErrForbidden`，不会静默过滤。
- 参数校验：`search_turns.limit ≤ 8`、`list_weak_turns.limit ≤ 10`、分数必须在 `[0,100]`、`min_score ≤ max_score`，越界一律 `ErrInvalidArgument`。
- 预算：每段最多 4 次调用，整个 ReportRun 最多 12 次；超出返回 `ErrBudgetExceeded`。本期 `Collect` 固定 3 次，成本可预估。
- 超时：每次调用从调用方 ctx 派生 `2s` 超时，超时返回 `ErrTimeout`。
- 大小上限：单次结果默认 `6000` runes，超出截断并记 `Truncated=true`。
- 成本口径：每次调用记录 `tool / args / duration_ms / result_runes / truncated`，聚合后写入 `report.tool_calls` 与 `report.completed` 事件。

报告 prompt 只注入：维度均分、逐题摘要（最多最近 12 条）、弱题答案摘录（每条 ≤320 字）；**不注入整场对话**，也不注入完整答案正文。

### 7.3 报告结构

结构化 JSON（`reports.structured`，`schema_version=1`）：

```json
{
  "schema_version": 1,
  "session_id": "...",
  "generated_at": "2026-11-06T10:00:00Z",
  "degraded": false,
  "error_code": "",
  "overall_score": 72.5,
  "score_band": "良好",
  "verdict": "总评……",
  "dimensions": [{"dimension": "correctness", "score": 80, "weight": 0.3, "comment": "……"}],
  "questions": [{
    "turn_id": "...", "seq": 1, "topic": "...", "kind": "primary", "depth": 1,
    "question": "...", "score": 80, "degraded": false, "comment": "……",
    "strong_points": ["..."], "missing_points": ["..."],
    "evidence": [{"turn_id": "...", "quote": "...", "reason": "..."}]
  }],
  "improvements": ["...", "...", "..."],
  "next_steps": ["...", "..."],
  "sections": [{"index": 0, "title": "总评", "markdown": "...", "turn_refs": ["..."], "tool_calls": 3}],
  "tool_calls": 3,
  "tool_breakdown": {"get_session_overview": 1, "list_weak_turns": 1, "search_turns": 1},
  "model_name": "openai/gpt-4o-mini",
  "prompt_version": "report-v1",
  "meta": {}
}
```

- 分数、维度分、逐题分数与证据来自确定性评估数据；模型只负责写点评文本（verdict / 维度评论 / 逐题评论 / improvements / next_steps），**不允许给分**。
- 渲染文本 `reports.markdown` 与 JSON 同事务落库，便于直接展示与导出。
- 固定 5 段：总评、分维度表现、逐题点评、最需要补的 3 个点、下一步练习建议。
- 未知 `turn_id`、缺失必填字段、长度越界 → 视为模型输出非法，重试 1 次后降级。

### 7.4 降级策略

- 模型传输失败 → `degraded=true, error_code=model_unavailable`。
- 模型 JSON 非法/校验失败（含未知 turn_id）→ 重试 1 次后 `report_output_invalid`。
- 本场 0 个有效作答 → `no_answers`，仍返回完整报告与分数 0。
- 降级报告由评估数据确定性拼装，run 仍成功、session 仍 `completed`；只有 ctx 取消与存储错误会让 run 失败。

---

## 8. 数据模型与存储

### 8.1 PostgreSQL 表

| 表 | 关键字段 | 约束/索引 |
| --- | --- | --- |
| `sessions` | `id`、`candidate_name`、`target_role`、`resume_text`、`resume_profile jsonb`、`jd_text`、`config jsonb`、`status`、`phase`、`state_version`、`pending_turn_id`、`plan_cursor`、`followups_used`、`started_at`、`ended_at` | PK `id`；`(status)` 索引 |
| `plan_items` | `id`、`session_id`、`seq`、`topic`、`objective`、`expected_points jsonb`、`difficulty`、`status` | UNIQUE `(session_id, seq)` |
| `turns` | `id`、`session_id`、`seq`、`kind`(primary/followup)、`parent_turn_id`、`topic`、`depth`、`question`、`expected_points jsonb`、`status`、`answer_text`、`answer_hash`、`answered_at` | UNIQUE `(session_id, seq)`；`(session_id, status)` 索引 |
| `evaluations` | `turn_id` PK、`session_id`、`schema_version`、`scores jsonb`、`weighted_score`、`bonus jsonb`、`penalty jsonb`、`evidence jsonb`、`missing_points jsonb`、`confidence`、`model_name`、`prompt_version`、`raw_output jsonb` | PK `turn_id`；`(session_id, weighted_score)` 索引 |
| `runs` | `id`、`session_id`、`kind`(start/answer/report)、`status`、`idempotency_key`、`request_hash`、`attempt`、`error_code`、`last_step`、`created_at`、`updated_at`、`finished_at` | UNIQUE `(session_id, kind, idempotency_key)`；`(status, updated_at)` 索引 |
| `run_steps` | `run_id`、`step_name`、`status`、`attempt`、`input_hash`、`output_json`、`error_code`、`started_at`、`finished_at` | PK `(run_id, step_name)` |
| `session_events` | `event_id bigserial`、`session_id`、`run_id`、`type`、`payload jsonb`、`created_at` | PK `event_id`；`(session_id, event_id)` 索引 |
| `reports` | `id`、`session_id`、`run_id`、`status`、`schema_version`、`structured jsonb`、`markdown`、`overall_score`、`degraded`、`error_code`、`model_name`、`prompt_version`、`tool_calls`、`created_at`、`updated_at` | PK `id`；UNIQUE `session_id`（一个会话只有一份报告）；`(created_at DESC)` 索引 |
| `users` | `id`、`email`、`password_hash`、`is_demo`、`created_at` | PK `id`；`lower(email)` 唯一索引 |

**归属字段（M17）**：`sessions`、`runs`、`run_steps`、`session_events`、`turns`、`evaluations`、`reports`、`review_cards`、`jd_listings` 均带 `user_id` 与非空约束，并各自有 `(user_id, ...)` 索引。`usr_legacy` 是历史数据占位用户，密码哈希为空，永不参与登录。

**设计说明**：

- 题目与答案是同一个 `turn`，避免「问题表 + 回答表」的 join 复杂度；追问通过 `parent_turn_id + depth` 表达链。
- `raw_output` 保留模型原始输出用于回放/调参；生产可加 TTL 或脱敏，MVP 保留。
- `session_events` 是 SSE 的**唯一事实来源**；Redis 不存事件正文。
- 时间统一 `timestamptz`，服务端 UTC，展示层本地化。

### 8.2 Redis 用途

| Key | 用途 | TTL |
| --- | --- | --- |
| `lock:session:{id}` | 跨进程会话互斥，token 校验 | 30s，10s 续租 |
| `idem:{session}:{kind}:{key}` | 幂等快路径缓存 → run_id | 24h |
| `cache:topic_summary:{session}:{topic}` | L2 摘要缓存 | 1h |

Redis 只做协调与加速；**删掉 Redis 后系统仍应正确**（退化为 PG 锁 + 直接查库）。这是明确的替换点。

### 8.3 运行期存储与测试替身

**PostgreSQL 是唯一运行期事实源**，本地开发、集成测试、演示环境都连 PostgreSQL；不存在「内存模式」运行开关。

- `internal/store/memory` 只是**测试替身（test double）**：仅被单元测试直接注入 `ports`，用于验证状态机、幂等与恢复逻辑；不参与 `cmd/server` 装配，不提供 `STORAGE=memory` 之类的配置项。
- 运行期装配固定为 `internal/store/postgres`；`app` 只依赖 `ports`，不感知具体实现。
- 内存替身与 PostgreSQL 实现跑同一套契约测试（同一组行为断言），防止替身与真实实现语义漂移。
- 内存替身不保证事务回滚、多进程与持久化，这些能力只在 PostgreSQL 实现上验证。
- M1 起即落地 PostgreSQL 迁移与仓储，不再存在「先用内存跑、以后再替换」的阶段。

---

## 9. API 与 SSE 约定

### 9.1 接口清单

| 方法 | 路径 | 作用 | 返回 |
| --- | --- | --- | --- |
| `POST` | `/v1/auth/register` | 注册并签发 JWT | `201 {access_token, token_type, expires_in, user}` |
| `POST` | `/v1/auth/login` | 登录并签发 JWT | `200` 同上；错误凭据 `401 invalid_credentials` |
| `POST` | `/v1/auth/demo` | 匿名体验（独立临时用户） | `200` 同上；超限 `429 demo_rate_limited` |
| `POST` | `/v1/sessions` | 创建面试会话 | `201 {session_id, status, state_version}` |
| `GET` | `/v1/sessions/{id}` | 查询会话快照 | `200` 会话摘要 |
| `POST` | `/v1/sessions/{id}/start` | 生成题纲与第一题 | `202 {run_id}` |
| `GET` | `/v1/sessions/{id}/events` | SSE 订阅/续播 | `text/event-stream` |
| `POST` | `/v1/sessions/{id}/answers` | 提交答案 | `202 {run_id, status, run_state}` |
| `POST` | `/v1/sessions/{id}/finish` | 收尾并生成报告（active/awaiting 也可直接调用） | `202 {run_id, status, run_state, report_ready, report_id}` |
| `POST` | `/v1/sessions/{id}/report` | 对已到 `ready_to_finalize` 的会话生成报告（拒绝提前结束） | `202 {run_id, status, run_state, report_ready, report_id}` |
| `GET` | `/v1/sessions/{id}/report` | 获取已落库报告（JSON + Markdown） | `200` / `409 report_not_ready` |
| `POST` | `/v1/sessions/{id}/runs/{run_id}/retry` | 显式重试失败 run | `202` |
| `POST` | `/v1/sessions/{id}/abort` | 中止 | `200` |
| `GET` | `/healthz` | 健康检查 | `200 {status:"ok"}` |

**请求示例**：

```http
POST /v1/sessions
Content-Type: application/json

{
  "candidate_name": "张三",
  "target_role": "Go 后端开发",
  "resume_text": "……",
  "jd_text": "……",
  "config": {
    "max_primary_questions": 8,
    "max_followups_per_question": 2,
    "max_total_followups": 6,
    "duration_minutes": 45,
    "difficulty": "mid",
    "locale": "zh-CN"
  }
}
```

```http
POST /v1/sessions/{id}/answers
Idempotency-Key: answer-<turn_id>-<answer_hash前8位>
Content-Type: application/json

{"turn_id":"01H...","answer":"我当时用的是……"}
```

**语义约定**：

- 所有写接口返回 `202`，真正的执行结果通过 SSE 事件观察；POST 不阻塞等待模型。
- 所有写接口支持 `Idempotency-Key`；未带时服务端派生。
- 资源不存在返回 `404`；状态不允许返回 `409`；参数/幂等 key 冲突返回 `422`。
- 收尾幂等：同一 session 至多一份报告；重复调用 `/finish`（同 key、异 key、运行中）都返回已有 run 或已落库报告，并带 `Idempotent-Replayed: true`，不会重复调用模型。
- 设计取舍：状态机自动收尾（题纲耗尽/时间到）只到 `active/ready_to_finalize`，报告需显式调用 `/finish` 或 `/report` 生成；`/report` 在 `active/awaiting_answer` 时返回 `409`，避免误把面试提前结束。这样不改变 M1-M3 answer run 的幂等与恢复语义。

### 9.2 SSE 事件约定

连接：

```http
GET /v1/sessions/{id}/events?after=0
Accept: text/event-stream
Last-Event-ID: 42
```

帧格式：

```text
id: 43
event: question.asked
data: {"event_id":43,"session_id":"01H...","run_id":"01H...","type":"question.asked","occurred_at":"2026-11-06T10:00:00Z","schema_version":1,"payload":{...}}

```

- 推送机制：事件写入与 `SELECT pg_notify('ai_interview_events', session_id)` 在同一事务提交；每个进程持有一条专用 `LISTEN` 连接，收到信号后只唤醒本进程内订阅该 session 的 SSE 连接。处理器被唤醒后按 `afterID` 回查 `session_events`，**事件表仍是唯一事实来源**，NOTIFY 只是加速信号。
- 不丢：`Subscribe` 会等待 `LISTEN` 注册完成后再读 backlog；通知在提交后才投递；监听连接断开自动重连（500ms），信号丢失最坏退化为 15s 心跳时回查，不会丢事件。
- 不重：连接内 `afterID` 单调推进；重连由 `Last-Event-ID` / `?after=` 从该 id 之后读取。
- 心跳：每 15s 写注释帧 `: ping`，同时作为安全回查点。
- 重连：`id` 为 `event_id`；客户端带 `Last-Event-ID` 或 `?after=` 续播。
- 顺序：同一 session 内按 `event_id` 单调递增。
- 投递语义：至少一次；客户端按 `event_id` 去重。
- 终止：`session.completed` / `session.aborted` / `session.failed` 后服务端主动关闭。

**事件目录**：

| 事件 | 时机 | 关键 payload |
| --- | --- | --- |
| `session.created` | 创建后 | `status` |
| `session.started` | 开场完成 | `first_turn_id` |
| `run.started` | 每次 run 开始 | `run_id`, `kind` |
| `question.asked` | 抛题/追问 | `turn_id`, `seq`, `topic`, `text`, `is_followup`, `depth` |
| `answer.received` | 答案已随投影落库 | `turn_id`, `chars`, `truncated` |
| `evaluation.completed` | 评估成功落库 | `turn_id`, `base_score`, `total_score`, `scores`, `confidence`, `missing_points`, `model`, `prompt_version` |
| `evaluation.degraded` | 模型输出非法且修复耗尽 | `turn_id`, `code`, `attempts`, `raw_output`（截断） |
| `decision.made` | 决策完成 | `turn_id`, `action`(follow_up/next_question/finish), `reason`, `depth`, `followups_used`, `plan_cursor`, `next_turn_id`, `next_question_asked` |
| `session.phase` | 稳定阶段变化 | `status`, `phase`, `state_version` |
| `report.started` | 报告 run 建立 | `run_id` |
| `report.section` | 报告每段生成 | `index`, `title`, `markdown`, `tool_calls`, `degraded`, `turn_refs?` |
| `report.tool_calls` | 工具调用汇总 | `count`, `breakdown` |
| `report.completed` | 报告落库 | `report_id`, `overall_score`, `degraded`, `error_code`, `tool_calls`, `model_name` |
| `run.failed` | run 失败 | `run_id`, `code`, `retryable` |
| `run.interrupted` | 崩溃恢复发现 | `run_id`, `last_step` |
| `session.completed` | 全流程结束（终态，最后一条） | `report_id`, `overall_score`, `degraded` |

**收尾事件顺序**：`session.phase(reporting)` → `run.started` → `report.started` → `report.section` ×5 → `report.tool_calls` → `report.completed` → `session.phase(completed)` → `session.completed`。

### 9.3 错误与重试约定

统一错误体：

```json
{
  "request_id": "req-...",
  "code": "state_conflict",
  "message": "会话状态已变化，请刷新后重试",
  "retryable": false,
  "details": {"current_state_version": 3},
  "run_id": null
}
```

| code | HTTP | retryable | 说明 |
| --- | --- | --- | --- |
| `invalid_argument` | 400 | false | 参数缺失/格式错误 |
| `session_not_found` | 404 | false | 会话不存在 |
| `turn_not_found` | 404 | false | 题目不存在 |
| `state_conflict` | 409 | false | 阶段不允许/版本冲突 |
| `run_in_progress` | 409 | true | 同会话已有 run 在执行 |
| `report_not_ready` | 409 | true | 报告未生成完成 |
| `budget_exhausted` | 409 | false | 预算用尽，应走收尾 |
| `idempotency_key_reused` | 422 | false | 同 key 不同请求体 |
| `model_timeout` | 504 | true | 模型超时 |
| `model_unavailable` | 503 | true | 模型/上游不可用 |
| `model_output_invalid` | 502 | true | 修复重试后仍非法 |
| `rate_limited` | 429 | true | 限流 |
| `internal` | 500 | false | 未知错误 |

**重试规则**：

- 模型层：`429/5xx/网络错误/超时` 重试 2 次，指数退避（0.5s、1.5s）+ 抖动；`4xx`（除 429）不重试。
- 结构化输出：校验失败修复重试 2 次；仍失败按 `model_output_invalid` 结束，不落假分。
- 客户端：写请求必须带幂等键才可安全重试；读请求直接重试。
- SSE：服务端发 `retry: 3000`；客户端指数退避 + `Last-Event-ID` 续播，上限 5 次。
- 重试不重置预算：`attempt` 记录在 `run_steps`，总耗时计入 run 超时。

---

## 10. 可观测性与成本口径（M7 已落地）

### 10.1 结构化日志字段

日志统一由 `log/slog` 输出单行 JSON，请求定位字段放在 `internal/platform/reqctx`，由 `reqctx.Attrs(ctx)` 统一注入：

| 字段 | 出现位置 | 说明 |
| --- | --- | --- |
| `request_id` | 所有请求 | 来自 `X-Request-ID`，缺失则服务端生成；异步 run 通过 `context.WithoutCancel` 继承 |
| `session_id` | 会话相关 | HTTP 中间件从 `/v1/sessions/{id}` 解析；app 命令入口再次写入 ctx |
| `run_id` | 会话相关 | `Runner.Execute` 载入 run 后写入 ctx |
| `turn_id` | 作答/评估 | `Answer` 命令与 answer run 执行时写入 ctx |
| `step_name` / `attempt` | 步骤执行 | 与 `run_steps` 对齐 |
| `event_id` / `event_type` | 事件追加 | 与 `session_events` 对齐；不打印 payload |
| `provider` / `model` / `purpose` | 模型调用 | purpose = `evaluate` / `question` / `report` |
| `result` / `latency_ms` / `error_class` | 模型调用与失败路径 | 失败分类复用 `llm.Classify` |
| `prompt_tokens` / `completion_tokens` / `total_tokens` | 模型调用成功 | 成本口径（见 10.4） |

关键路径使用同一套字段：命令入队（`start/answer/report run accepted`）、步骤执行（`run step started/succeeded/replayed`）、模型调用（`model call`）、事件追加（`event appended`）、报告生成（`report run succeeded`）。

隐私约定：日志只记录 ID、长度、状态与计数，**不记录答案正文、API key 或模型请求/响应文本**；`appendEvent` 只打印事件元数据。

### 10.2 run / turn 级链路

- 一次 HTTP 命令生成 `request_id`；命令入队时写入 `session_id`（作答再写 `turn_id`），run 执行时写入 `run_id`，模型客户端从 ctx 读取全部字段。
- run 内每个 step 形成 `run_id + step_name + attempt` 节点；一次 grep `request_id` 可还原「命令 → 步骤 → 模型调用 → 事件 → 响应」。
- SSE 事件带 `event_id` 与 `run_id`，前端反馈可用 `event_id` 对应服务端日志。
- 不接入 OpenTelemetry / Jaeger：`request_id + run_id` 的日志关联已满足当前规模，APM 仍在 YAGNI。

### 10.3 模型调用与 token 统计

- 所有模型客户端由 `agent/llm.InstrumentedClient` 包装：记录调用次数、结果、耗时、token，并写入 session 级 `UsageTracker`。
- 指标命名（`internal/platform/obs`，手写 Prometheus 文本，无第三方依赖）：

| 指标 | 类型 | 标签 |
| --- | --- | --- |
| `ai_interview_model_calls_total` | counter | `provider,model,result`（success/error） |
| `ai_interview_model_tokens_total` | counter | `provider,model,direction`（input/output） |
| `ai_interview_evaluation_degraded_total` | counter | `reason`（错误码） |
| `ai_interview_report_degraded_total` | counter | `reason`（错误码） |
| `ai_interview_lock_conflicts_total` | counter | 无 |
| `ai_interview_sse_connections` | gauge | 无 |
| `ai_interview_sse_event_delivery_seconds` | histogram | 无（查询事件到写出耗时） |
| `ai_interview_run_failures_total` | counter | `kind,class` |

- `GET /metrics` 输出 `text/plain; version=0.0.4`，可直接被 Prometheus 抓取。

### 10.4 单场成本汇总

- `UsageTracker` 按 `session_id` 聚合，`purpose` 分为 `evaluate` / `question` / `report`；以 `report.completed` 时刻的快照为准。
- 报告字段沿用 M4 `tool_calls` 的命名习惯：`model_calls`（总数）、`model_call_breakdown`（按 purpose）、`prompt_tokens`、`completion_tokens`、`total_tokens`。
- 成本只统计实际模型调用；降级/失败调用计入 `model_calls_total{result="error"}`，但不计 token。
- 不计算美元金额：供应商单价差异大且易变，计价配置与 `cost_usd` 未在 M7 落地。

### 10.5 失败与降级计数

- 计数与日志成对出现：评估降级、报告降级、锁冲突、run 失败都同时写指标与结构化日志。
- SSE 连接数在订阅成功后 +1、handler 退出时 -1；事件投递延迟按「查询事件 → 写出成功」计时。
- 不做实时告警、APM 接入或日志聚合平台；本地用 stdout JSON + grep 即可。

---

## 11. 目录结构与职责

```
.
├── cmd/server/                  # 进程入口：配置加载、依赖组装、HTTP 启动、优雅退出
├── internal/
│   ├── api/http/                # 路由、DTO、参数校验、错误映射
│   ├── app/                     # 用例服务：事务、幂等、锁、状态提交、事件发布
│   ├── agent/
│   │   ├── interview/           # StartRun / AnswerRun 的 eino 图与节点
│   │   ├── report/              # ReportRun 图、段落编排、critic
│   │   ├── tools/               # 报告回查工具（search_turns / get_turn ...）
│   │   ├── model/               # ChatModel 适配、超时重试、FakeChatModel
│   │   └── prompt/              # 模板与 prompt_version
│   ├── domain/
│   │   ├── session/             # 会话实体、状态/阶段常量、迁移规则
│   │   ├── evaluation/          # rubric 结构、校验、计分
│   │   └── followup/            # 追问触发与预算策略
│   ├── ports/                   # 仓储/模型/事件接口
│   ├── store/
│   │   ├── postgres/            # PostgreSQL 实现（M1，唯一运行期存储）
│   │   ├── redis/               # 会话锁（M5）
│   │   └── memory/              # 测试替身（仅单测注入，不参与运行期装配）
│   ├── transport/sse/           # SSE 帧写出、心跳、断线重连辅助
│   └── platform/                # config、logging、idgen、clock
├── migrations/                  # SQL 迁移（M1）
├── deploy/docker-compose.yml     # PostgreSQL + Redis 本地依赖
├── docs/development-plan.md      # 本文档
├── api/                         # OpenAPI（M2 起维护）
├── Makefile                     # 常用命令快捷方式
└── README.md                    # 运行方式与目录职责
```

**包内文件约定**：

- `internal/agent/interview/graph.go`：作答链路 `compose.Graph` 组装与编译（M3 起为真实分支图）。
- `internal/agent/interview/evaluate.go` / `question.go`：评估与出题节点实现（M2 / M3）。
- `internal/agent/interview/fake_model.go`：无 key 的确定性 eino `BaseChatModel`（评估 + 出题）。
- `internal/domain/*`：纯函数，不 import eino / pgx / net/http。
- `internal/ports`：接口按「谁使用谁定义」放在此包，存储实现只做实现。
- `cmd/server/main.go` 只做组装，不写业务分支。

---

## 12. 里程碑与验收点

每个里程碑都必须「可运行、可验证」，不是「代码写完」。

### M0 设计 + 骨架（已完成）

- 内容：本文档、目录结构、`go.mod`、可编译骨架、README、Docker Compose、健康检查、最小 eino 图（无模型）。
- 验收：
  ```bash
  go build ./...
  go test ./...
  go run ./cmd/server
  curl http://localhost:8080/healthz   # {"status":"ok",...}
  ```
- 通过标准：编译 0 错误；`/healthz` 返回 200；启动日志显示 turn graph compiled；无任何模型/数据库调用。

### M1 真实存储 + 会话状态机（已完成）

- 内容：SQL 迁移（`sessions` / `runs` / `run_steps` / `session_events`）、pgx 仓储与事务、`domain/session` 迁移规则、创建/查询/开始/中止接口、命令返回 `202 + run_id`、SSE 订阅与 `Last-Event-ID` 续播、节点级步骤日志、幂等键唯一约束、启动时 reconciliation 并从最后成功节点续跑。
- 验收：
  ```bash
  docker compose -f deploy/docker-compose.yml up -d
  go test ./...                        # 单测（内存测试替身）
  go test ./... -tags=integration      # PostgreSQL 契约与全链路
  ```
  - `POST /v1/sessions` → `201`；`POST /v1/sessions/{id}/start` → `202 {run_id}`；`GET .../events` 依次收到 `session.created` / `run.started` / `session.started` / `question.asked` / `session.phase`。
  - 同一 `Idempotency-Key` 重复 `start`：只产生一个 run，第二个请求返回同一 `run_id` 且带 `Idempotent-Replayed: true`；不同请求体复用同一 key 返回 `422`。
  - 故障注入：step（如 `ask`）执行前杀进程，重启后识别 `interrupted`，从最后成功的 `plan` 节点继续，`plan` 不重复执行、事件不重复写入。
- 验证结果（2026-11-06，真实 PostgreSQL）：
  - `go test ./...` 全绿；`go test -tags=integration ./...` 全绿（`TestRepositoriesContract`、`TestStartFlowWithPostgres`、`TestReconcileWithPostgres`）。
  - 最小链路实测：`POST /v1/sessions` → `201`；`POST .../start` → `202 + run_id`；SSE 依次收到 `session.created` / `run.started` / `session.started` / `question.asked` / `session.phase`。中文 payload 以 UTF-8 JSON 文件提交后，落库与回显正常（此前 PowerShell 内联 `-d` 显示 `????` 属客户端编码问题）。
  - 幂等实测：同 key 第二次返回同一 `run_id`、`Idempotent-Replayed: true`，事件条数不增。
  - 恢复实测：构造 `running` 且 `last_step=plan` 的陈旧 run，`Reconcile` 将其标为 `interrupted`；`plan` 从 `output_json` 回放（attempt 仍为 1），`ask` 重新执行（attempt=2），`question.asked` 恰好 1 次。
- 存储口径：PostgreSQL 是唯一运行期事实源；`store/memory` 仅作为单测替身，不提供运行期开关。
- 明确不做（留到 M2）：真正的评估/提问模型、答案提交与追问。

### M2 回合图 + 假模型 + 完整作答链路（已完成）

- 内容：`plan_items` / `turns` / `evaluations` 迁移与仓储、`runs.input_json` 作答命令快照、`POST /answers`（202 + run_id、幂等、turn 校验）、AnswerRun 节点步骤（`snapshot` / `evaluate` / `decide` / `ask`）与节点级恢复、eino `BaseChatModel` 适配层（`agent/llm`）、评估 prompt 与 JSON 校验/修复/降级、规则裁决三门、进程内会话锁、`FakeChatModel` 与启发式默认模型。
- 接口与事件：`POST /v1/sessions/{id}/answers`；事件新增 `answer.received`、`evaluation.completed`、`evaluation.degraded`、`decision.made`，复用 `question.asked` / `session.phase` / `run.started`。
- 验收：创建会话 → start → 收到 `question.asked` → 提交答案 → 收到 `answer.received` + `evaluation.completed`（或 `evaluation.degraded`）+ `decision.made` + 下一题（或 `session.phase` 进入 `ready_to_finalize`）；断线后带 `Last-Event-ID` 重连不丢事件；同一答案重复提交返回同一 `run_id` 且事件不增。
- 验证结果（2026-11-06，临时 embedded PostgreSQL 5433）：
  - `go test ./...` 全绿；`go test -tags=integration ./...` 全绿，新增 `TestAnswerFlowWithPostgres`（作答落库、评估持久化、状态推进、幂等重放）。
  - HTTP 全链路：`POST /answers` → `202 {run_id}`；第二次同 key 返回同一 run 且 `Idempotent-Replayed: true`。
  - 单测分支：高分推进、低分追问、追问预算耗尽推进、题纲耗尽强制收尾、非法 JSON 降级、证据编造丢弃、传输错误可重试、覆盖门、时间门、深度门、单题追问上限、不会回答。
- 边界：不接真实模型也能完整跑一场；默认模型为确定性 `fake-heuristic`，真实 provider 在 M3 注入。跨进程锁与 Redis 在 M5 接入，M2 的锁已集中在 `app.Service` 的 session 锁层，可整体替换。

### M3 真实模型 + 题纲出题 + compose.Graph（已完成）

- 内容：
  - 真实 provider：eino-ext `components/model/openai`（OpenAI 兼容），`base_url` / `model` / `api_key` 全部来自环境变量；无 key 时自动回退 `fake-heuristic`，启动日志 Warn。
  - 调用封装：单次调用超时（`MODEL_TIMEOUT`）、仅对 timeout / rate_limited / unavailable / network 做指数退避重试（`MODEL_MAX_RETRIES`，0..5）、prompt/completion token 计数、失败分类 `timeout|rate_limited|unavailable|network|auth|bad_request|canceled|unknown`。
  - 作答链路：`compose.Graph`，`START → snapshot → evaluate → decide`，`decide` 按裁决分支到 `ask_followup` / `ask_next` / `END`；节点不写库，`app` 用 `recordStep` 包裹依赖保留节点级恢复与幂等语义。
  - 出题链路：题纲（知识点清单）+ 简历画像生成问题，题纲项带 `expected_points` / `objective` / `difficulty`，随 `plan_items` 与 `turn` 落库；出题带历史题目（最近 12 题）去重；模型非法/重复/不可用时用确定性模板降级。
  - 数据：内置 Go / MySQL / Redis 三套题纲；结构化简历画像（`years` / `skills` / `projects`）落 `sessions.resume_profile`，文本输入自动解析。
  - 覆盖门：`expected_points` 全覆盖即 `next_question(coverage_satisfied)`，不再受模型追问建议影响。
- 验收：`go build ./...`、`go test ./...` 全绿；`go test -tags=integration ./...`（embedded PostgreSQL）全绿，含 profile/expected_points 持久化、恢复重放、事件幂等。
- 未联调项：真实 provider 因缺少 API Key 未做线上联调；有 key 后执行：
  1. 设置 `OPENAI_API_KEY`（可选 `OPENAI_BASE_URL` / `OPENAI_MODEL`），启动 `go run ./cmd/server`，日志应显示 `model provider ready provider=openai`，且无 `model provider degraded`；
  2. `GET /healthz` 确认存储可用（provider 信息以启动日志为准）；
  3. 创建会话并作答，检查 `evaluations.model_name` 为 `openai/<model>`、`raw_output` 为真实模型 JSON；token 计数由 `llm.ResilientClient.Stats()` 进程内累计（本期未暴露到 HTTP）；
  4. 观察一次 401（错误 key）→ `auth` 不重试；一次超时 → 重试后降级评估继续推进。
- 边界：原计划中的 20 条评测回归集与跑分脚本未纳入本期，留到 M4 前后的评测工作。

### M4 收尾与报告（已完成）

- 内容：
  - 收尾状态机：`POST /finish` 覆盖 `active/awaiting_answer` 与 `active/ready_to_finalize`，迁移到 `reporting`；报告提交后 `completed`；`aborted/failed` 为终态。`POST /report` 只允许 `ready_to_finalize` 及以后，拒绝提前结束。
  - 报告幂等：run 幂等键 `(session, report, key)` + `reports.session_id UNIQUE` + 先查已落库报告三重保障；重复 finish 返回现有 run/报告，模型调用次数不增加。
  - 只读工具：`get_session_overview` / `search_turns` / `list_weak_turns` / `get_turn`，带 session 归属校验、参数校验、2s 超时、结果 6000 runes 上限、每段 ≤4 / 总 ≤12 预算；调用记录进入 `report.tool_calls`。报告固定采集计划实际调用前 3 个；`get_turn` 已实现但未接入报告链路。
  - 报告生成：固定 3 次工具回查 → 单次模型调用写点评 → 确定性数据合并分数与结构 → 5 段 JSON + Markdown 同事务落库；prompt 不含全量对话。
  - 降级：`model_unavailable` / `report_output_invalid` / `no_answers` 均产出完整报告，run 成功、session `completed`。
  - 事件：`report.started` / `report.section` ×5 / `report.tool_calls` / `report.completed`，终态事件最后。
- 验收：`go test ./...` 全绿；`go test -tags=integration ./...`（embedded PostgreSQL 5433）全绿。
  - 单测：`internal/agent/tools`（越权/参数/预算/超时/截断）、`internal/agent/report`（合法/非法/传输失败/0 作答/降级确定性）、`internal/app`（正常生成、同 key/异 key 幂等、模型失败降级、拒绝提前结束、报告未就绪）、`internal/api/http`（finish→report 全链路 + 重放头）。
  - 集成：`TestFinishReportWithPostgres` 覆盖报告落库、structured/markdown、5 段、tool_calls=3、事件顺序、session completed、重复 finish 幂等、缺失报告 409 语义。
- 边界：原计划 M4 的「追问策略 + 完整面试集成测试」在 M3/M4 中已随作答链路与集成测试覆盖；本期 M4 按项目对外演示目标改为「收尾与报告」，原 M6 的报告 Agent 目标由本期简化版落地（多轮自主工具检索与逐段 critic 留作后续增强）。

### M5 Redis 会话锁 + LISTEN/NOTIFY 事件推送（已完成）

- 内容：
  - 端口层 `ports.Locker`：`Acquire(ctx, key, token)` / `Release(ctx, key, token)`；`ErrLockBusy`（可重试）与 `ErrLockBackend`（后端不可用）区分。
  - Redis 实现：`SET NX PX`（默认 10s TTL）+ 同 token 重入刷新 TTL + Lua 比较 token 删除；抢锁最多等 3s，失败返回 `409 lock_busy`。
  - 进程内 `ProcessLocker` 仅供单测与 Redis 不可达时的单实例降级；`app.Service` 的所有命令（start / answer / abort / retry / finish / report）统一走 `acquireSession`。
  - SSE 从 250ms 轮询改为 PostgreSQL `LISTEN/NOTIFY`：`pg_notify` 与事件同事务；每进程一条 LISTEN 连接，按 session 分发唤醒；订阅先于 backlog 读取，避免启动竞态；监听断线自动重连；15s 心跳兼作安全回查。
  - 连接建立时立即 flush SSE 响应头，客户端不会等首帧心跳。
- 验收：
  ```
  go test ./...
  go test -tags=integration ./...
  ```
  - 单测：`internal/store/redis`（miniredis 替身：互斥、同 token 重入续期、TTL 过期、错误 token 释放无效、后端故障不是 busy）、`internal/store/memory`（订阅唤醒）、`internal/api/http`（无轮询下的 SSE 实时投递、抢锁失败 `409 lock_busy` + retryable）。
  - 集成：`TestNotifyReachesSecondStore`（两个独立 PG 连接池，A 提交事件，B 的订阅被 NOTIFY 唤醒并读到事件）；`TestTwoInstancesDoNotDoubleWriteSameTurn`（两个 `app.Service` + 两个 Redis 客户端共享 miniredis，同一 session 并发作答仅 1 个成功、1 条 answer run、1 条评估；子测试在无共享锁时验证 PG 唯一约束/CAS 兜底，仍只有 1 次作答写入）。
- 本机环境说明：Docker 不可用，Redis 语义由 **miniredis 替代实现**验证（`internal/store/redis/lock_test.go`、集成用例），未在真实 Redis 服务上跑。正式环境需执行：
  ```
  docker compose -f deploy/docker-compose.yml up -d
  $env:REDIS_ADDR="localhost:6380"
  go test -tags=integration ./...
  # 双进程验收：启动两个服务实例（不同 HTTP_ADDR、同一 DATABASE_URL/REDIS_ADDR），
  # 对同一 session 并发 POST /answers，确认只有一个 202 且无重复 turn/evaluation。
  ```
- 边界：幂等快路径缓存与「进程被杀后的恢复演练 / 50 并发会话串号压测」未在本期落地；故障注入演练留待 M7。

### M6 报告 Agent 增强（原计划，主体已由 M4 覆盖）

- 已由 M4 覆盖：ReportRun、只读工具回查、段落生成、引用校验（turn_id 集合校验）、降级模板、报告接口、分段 SSE 事件。
- 仍可增强（非对外演示必需）：模型自主多轮工具检索、逐段 critic 与 `[turn:id]` 引用渲染、报告重试接口。验收口径沿用原计划：报告 prompt 不含完整 transcript，未知引用被拦截。

### M7 可观测性与成本口径（已完成）

- 内容：`reqctx` 串起 `request_id / session_id / run_id / turn_id`；关键路径结构化日志；`/metrics` Prometheus 文本（模型调用、token、评估/报告降级、锁冲突、SSE 连接与投递延迟、run 失败）；按 session 的 token 与调用次数快照写入报告（不计算金额）。
- 验收：一次完整面试的日志可按 `request_id` / `run_id` 串起全链路；报告出现 `model_calls` / `model_call_breakdown` / `prompt_tokens` / `completion_tokens` / `total_tokens`；降级、锁冲突、SSE 连接数有单测覆盖；`go test ./...` 与集成测试全绿。
- 边界：不做美元计价与 50 并发压测；日志不含答案正文与 API key。

### M8 最小可视化演示页（已完成）

- 理由：项目要用于售前/演示型面试，纯 API 演示吃亏；需要一个能看到「提问 → 作答 → 追问 → 报告」的最薄页面。
- 内容：单页 HTML（`/demo`，`//go:embed` 内嵌 `internal/api/http/demo/{index.html,app.js,style.css}`），通过 `fetch` 调命令接口、`EventSource` 订阅 SSE；展示当前题目、作答输入框、事件时间线、评估摘要与最终报告。不引入前端工程、不做组件库、不做构建链。
- 实现补充：作答链路新增 `question.asked` 事件（此前仅启动链路发送），SSE 客户端可直接渲染追问/下一题；页面按 `event_id` 去重，`409 lock_busy` 自动重试并提示，评估/报告降级与 run 失败均有可见提示。
- 验收：`go run ./cmd/server` 后浏览器打开 `/demo`，不借助 curl 完成一场面试并看到报告；断线刷新页面后带 `Last-Event-ID` 续播；页面只读事件，不直接改状态。
- 验证结果（2026-11-06，heuristic，`HTTP_ADDR=127.0.0.1:18080`）：3 主问题全部作答，逐题显示总分 80、五维 4/5、决策与下一题；收尾后报告 `status=completed`、`overall_score=80`、五段齐全（总评 / 分维度表现 / 逐题点评 / 最需要补的 3 个点 / 下一步练习建议）。`go test ./...` 与 `go test -tags=integration -count=1 ./...` 全绿。

### M9 简历附件上传与本地解析（已完成）

- 理由：纯文本粘贴对真实候选人摩擦太大，附件是简历入口的最低可用形态；解析必须在本地完成，不把简历送到模型。
- 内容：新增 `POST /v1/resume/parse`（`multipart/form-data`，字段 `file`），支持 PDF / DOCX / TXT / MD；5MB 上限、扩展名白名单；解析失败返回 `422 parse_failed` 并允许前端降级到手动粘贴文本；PNG/JPG 返回 `400 image_requires_ocr`（「需 OCR/多模态，尚未启用」），接口与数据模型预留图片分支。
- 解析边界：TXT/MD 按 UTF-8 读取，非 UTF-8 回落 GB18030；DOCX 用标准库 `archive/zip` + `encoding/xml` 读 `word/document.xml`；PDF 用纯 Go 的 `rsc.io/pdf` 逐页提取并按基线重建换行/空格，扫描件与不支持字体编码的 PDF 明确报解析失败；解析成功统一交给 `resume.Parse` 生成画像，不调用模型（响应带 `model_called=false`）。
- 错误码：`unsupported_file_type` / `image_requires_ocr`（400）、`file_too_large`（413）、`parse_failed`（422，`details.fallback=manual_text`）、`invalid_upload`（400）。
- 前端：上传区支持拖拽与键盘选择、类型/体积即时提示、解析中/失败状态；成功后把解析文本回填到简历输入区，并回显结构化画像（年限 / 技能 / 项目），角标标注「本地解析 · 未调用模型」；失败时提示改为手动粘贴。
- 验收：单测覆盖类型拒绝（未知扩展名、PNG/JPG）、超限拒绝、DOCX 解析失败降级、TXT/MD/DOCX/PDF 解析成功入画像、缺少 file 字段；`go test ./...` 全绿。
- 验证结果（2026-11-06，heuristic，`localhost:8080`）：真实上传 TXT（417 B）返回 200，画像 `years=5`、技能 5 项、项目 2 条；上传损坏 DOCX 返回 422 `parse_failed`，前端显示「解析失败，可手动降级」并保留手动粘贴入口。截图：[成功](screenshots/m9-resume-success.png) / [失败](screenshots/m9-resume-failure.png)。

### M10 配置推算与助手占位（计划）

- 理由：不同岗位/资历需要不同的题量、追问预算与难度，手工填参数对演示不友好。
- 内容：把配置推算规则整理成可测试的规则表（岗位族 × 资历 × 难度 → 主问题数 / 每题追问 / 总追问 / 时长），API 与 UI 同时展示「auto / manual」来源；规则命中时自动填充，用户手动修改后标记为 manual 且不再被覆盖。
- 边界：AI 助手仅做接口占位与 UI 入口，不接真实模型。
- 验收：规则表单测覆盖典型组合与边界；API 返回来源字段；前端能显示并尊重 manual 覆盖。

### M11 复习与间隔重复（已完成）

- 理由：一次面试的价值应在面试后延续；把低分点变成可复习卡片，形成「面试 → 复习 → 再测」闭环。
- 复习卡生成：每场面试结束时，用 `missing_points` 与低分维度确定性拼装复习卡（有缺失要点 / 单题低于 60 / 最弱维度 ≤2 才建卡），考点 / 用户当时的答法摘要 / 标准要点 / 来源 `turn_id`，不调用模型；模型生成留可选开关。
- 排程算法：采用 SM-2，字段 `ease_factor`、`interval_days`、`repetitions`、`due_at`；按回忆质量 0–5 更新并记录计算过程，便于讲解与测试（若后续换 FSRS 需在文档说明理由）。
- 接口：`GET /v1/review/due`（今日到期）、`POST /v1/review/{card_id}/grade`（提交回忆质量并更新排程）、复习卡列表与详情。
- 复习会话：到期卡可生成一次「换问法」再测（优先规则/题纲模板，避免模型开销；模型可选），答完回写复习卡与排程。
- 前端：首页/侧栏「今天该复习 N 张」入口；复习页正面 → 展开答案 → 自评 0–5 → 下一张；报告页增加「遗忘曲线预测」小图（按 SM-2 到期曲线绘制）。
- 分层：算法放 `domain`，仓储接口放 `ports`，实现放 `store`，HTTP 放 `api`；前端继续单页原生实现。
- 验收：排程单测覆盖 0/3/5 三种评分对应的间隔与 ease 变化、多次复习累积、到期计算与时区边界；接口测试覆盖 due 为空、正常提交、非法 grade 拒绝。

**M11 实现记录（已完成）**

- 卡片生成：`internal/domain/review/review.go` 的 `BuildCards` 在报告提交的同一事务内执行，从 `missing_points` 与每题最低维度确定性拼装卡片；`degraded` 题跳过；高分且无缺口与弱项（各维度 > 2）的题不生成。卡片保存考点、题目、答法摘要（优先取该维度 evidence 原文）、标准要点、来源 `turn_id`，全程不调用模型。
- 排程：SM-2，初始 `ease_factor=2.5`；`GradeCard` 按 0–5 更新 EF（下限 1.3）、重复次数与间隔（1 / 6 / 上次间隔 × EF），grade<3 归零重来；返回并持久化 `Calculation`（前后 EF、间隔、次数、下次到期与公式文本），便于面试讲解。
- 存储：`migrations/0007_m11.sql` 建 `review_cards`（`key_points` / `last_calculation` 为 JSONB，`(session_id, turn_id, topic)` 唯一索引保证重放不重复），`ports.ReviewRepository` + PostgreSQL 与内存实现；报告提交复用 `SaveMany`，重复插入 `ON CONFLICT DO NOTHING` 保留原排程。
- 接口：`GET /v1/review/due`（`tz_offset_minutes` 默认 480，按调用方本地日结束时间过滤）、`GET /v1/review?session_id=`、`GET /v1/review/{id}`、`POST /v1/review/{id}/grade`（缺省/越界 grade 返回 400）、`GET /v1/review/{id}/retest`（按 `repetitions` 轮换三种规则问法，`model_called=false`）。
- 前端：顶栏「今天该复习 N 张」入口；复习面板按「正面 → 展开参考答案 → 自评 0–5 → 下一张」推进，评分后提示新的 EF / 间隔 / 到期日；报告图表区新增「遗忘曲线预测」（SM-2 间隔 1/6/15/38/95/237 天）。
- 测试：`internal/domain/review/review_test.go` 覆盖 0/3/5 评分、失败重置、EF 下限、多次累积、时区边界（15:59Z 属于当日、16:01Z 属于次日）与卡片生成/换问法轮换；`internal/api/http/review_test.go` 覆盖 due 为空、非法 grade 400、正常评分与排程、列表、404 与未启用 503。

**M11 复习升级记录（已完成，确定性规则，不调模型）**

- 变式题：`BuildVariant` 优先取题纲 `expected_points`（按 `topic` 精确匹配领域包），缺失时回退卡片 `key_points`；按 `repetitions` 轮换三种问法模板，并用 `ID|Topic|Repetitions` 作为 FNV-1a 种子做 Fisher–Yates 打乱，同卡同轮次结果稳定，`model_called=false`。
- 客观评分：新增 `POST /v1/review/{id}/variant/answer`，评估走既有 `interview.Evaluator`（`internal/app/review_eval.go` 适配），无 key 时自动走 heuristic；`expected_points` 与题纲 rubric 一并传入，评分复用 `evaluation.ComputeScoreWith`，不改评分逻辑与 M6 门槛。
- 换算：`ObjectiveGrade(score)` 把 0–100 客观分映射到 SM-2 grade：`<40→1`、`40–59→2`、`60–79→3`、`80–89→4`、`≥90→5`；band 仍按既有 50/80 边界只作展示。SM-2 公式本身不变，只改 grade 来源。
- 双轨：自评 0–5 可选，只记录为参考；`|自评 − 客观 grade| ≥ 2` 判定分歧，自评更高提示「可能高估自己」，反之提示「可能低估自己」；`last_objective_score / last_objective_band / last_self_grade / last_divergence` 落卡，便于后续统计。评估降级时不排程、不猜测分数。
- 排序：`Due` 排序键为「题纲 `focus` 高频考点 > 历史低分（原题 <60 / 上次客观分 <60 / 上次 grade <3）> 普通题」，同级按 `due_at` 升序再按 `id`；每条卡返回 `priority` 与一句 `sort_reason`。
- 每日上限：`REVIEW_DAILY_LIMIT`（默认 20）只裁剪当日展示队列，返回 `total_due / deferred_count / remaining`；`limit` 只能进一步缩小，不改变持久化的 SM-2 到期时间。
- 前端：复用现有复习面板，新增变式题作答区、自评选择与「客观分 vs 自评」对比、排序理由、今日剩余额度与延后数量；不再用自评按钮直接写排程。
- 测试：覆盖变式题确定性、题纲要点回退、客观分换算边界、自评/客观分歧与落库、无 evaluator 503、降级不排程、排序优先级、每日上限与延后计数；`internal/app` 额外验证 heuristic 无 key 链路可跑。本轮全程无真实模型调用。

### M12 简历诊断：评分 + 优化建议（已完成，规则版）

- 理由：附件解析只是入口，候选人更需要「这份简历哪里扣分、怎么改」的结构化反馈。
- 输入与输出：上传/粘贴的简历 → 总分（0–100，五档）+ 逐项扣分 + 改写建议；结果卡分层折叠，不堆字。
- 三类必须覆盖的判断：
  - **量化指标检测**：是否有 before → after 数字、指标口径（P95/P99/平均）与测量条件；缺失即扣分，并给「差 → 好」改写示例。
  - **项目时间线可信度**：项目起止 × 工作量 × 技术栈深度 × 团队规模是否自洽；输出「可信 / 需解释 / 明显不合理」三档，并建议补真实细节，禁止教用户编造。
  - **包装 vs 编造边界**：润色（动词、排序、删冗余）与虚构（假指标、虚报职责、时间注水）分开标注；后者提示面试追问会被打穿。
- 实现：先做纯规则版（正则/词表/结构解析 + 确定性评分），不调用模型；模型点评为可选开关，默认关闭，不跑真实基线。
- 结构先行：`domain/resume` 规则引擎 + `ports` + `POST /v1/resume/analyze` + 前端结果卡。规则清单等调研文档到位后，以检查项 ID、严重程度、评分锚点、改写示例落进 `docs/resume-rules` 再实现。
- 验收：规则单测覆盖三类判断的正/负样例与边界；接口返回逐项扣分与建议；前端能展示总分、档位、三条主要问题与逐条「原文 → 问题 → 建议改写」。

### M13 闯关式进度条（已完成）

- 理由：「第 n / 共 N 题」只表达数量，无法体现题纲结构与追问关系。
- 内容：题纲节点横向排布，已完成打勾、当前高亮、未开始灰显；追问显示为当前节点的支线（层级缩进或小分叉标记，虚线连接）。
- 交互：答完推进使用 CSS transition 过渡，当前关卡自动滚动到可见位置；窄屏折叠为「当前关卡 + 剩余 x 关」。
- 验收：主问题推进、追问支线、窄屏折叠三种状态均有可见差异；不引入前端框架或构建链。
- 实现说明：会话接口不回传完整题纲，节点数取 `config.max_primary_questions`，主题在 `question.asked` 到达后回填；追问不新增关卡，而是在当前节点下方以虚线支线计数。未新增 API，也未引入构建链。
- 顺带修复：heuristic 评估曾把未覆盖的期望点写进 `evidence.reason`（「缺失：…」），使 `coveredAllPoints` 误判为已覆盖、追问永不触发；现改为缺口只进 `missing_points`，evidence 只引用回答原文，并由 `TestHeuristicUncoveredPointsSuggestFollowupWithoutClaimingCoverage` 锁定。
- 验证结果（2026-11-06，heuristic，`127.0.0.1:18080`）：主问题推进后第 1 关打勾、第 2 关高亮、第 3 关灰显；短答触发追问时当前节点显示「↳ 追问 1 / 上限 1」支线；390px 宽折叠为「第 1 关 / 共 3 关 · 剩余 2 关」。截图：[推进后](screenshots/m13-stepper-desktop.png) / [追问支线](screenshots/m13-stepper-followup.png) / [全部完成](screenshots/m13-stepper-all-done.png) / [窄屏折叠](screenshots/m13-stepper-mobile.png)。

### M14 领域包机制：外置题纲与可配置维度（已完成）

- 题库三层（硬约束，写进 design-rationale）：
  1. **本地骨架 = 评分唯一依据**：领域包为带版本号的外置配置：`topics`（考点）、`expected_points`（每考点 3 条可判定描述）、`rubric`（维度与权重）、`lexicon`（简历画像词表）。必须离线可跑、可单测、可 diff；评分链路禁止依赖任何网络内容。
  2. **联网补丁 = 只做素材，不进评分**：检索结果必须带 URL + 抓取时间 + 审核状态；未审核条目不得进入面试；缓存落地并设 TTL；检索失败降级到本地骨架，不影响面试。本期不实现联网检索，只留接口与文档说明（默认关闭）。
  3. **可追溯**：每条考点标 `source = curated | retrieved` 与可选引用链接。
- 交付：
  - `domain/domainpack` 加载器：manifest 含 `id`、`name`、`version`、`rubric`、`topics`；启动时校验 id 唯一、每考点 3 个 expected_points、难度合法、维度权重和为 1；无效包直接启动失败并报错，不能静默跳过。
  - 现有 Go 后端题纲迁入 `domainpacks/go-backend/`，行为不变，现有测试必须继续通过。
  - 会话创建传 `domain`（默认 `go-backend`），落到 `sessions.domain`；未知领域返回 400。
  - 维度可配置：不同领域可有不同维度名与权重（如售前包：需求理解 / 方案表达 / 权衡与边界 / 异议处理 / 可信度）；报告与 UI 的维度名、权重全部来自领域包，前端不许写死。
  - 完整运行时包：`go-backend`（现有题纲迁入，行为不变）、`java-backend`（10–12 考点）、`ai-agent`（10–12 考点），每个包自带 `rubric` 与 `lexicon`；只扫描 `domainpacks/` 下已注册的包，示例与文档目录不参与。
  - 示例包（不加载、不校验、不出现在可选列表）：`docs/domainpacks/examples/solution-presales.example.yaml`、`ops-growth.example.yaml`，各 2 个考点、完整 rubric 与字段注释，仅展示格式与维度差异。
  - 非技术领域文档：`docs/domainpacks/solution-and-ops.md` 记录售前 / FDE / 运营的完整考点与维度设计（售前看需求理解 / 方案表达 / 权衡边界 / 异议处理 / 可信度；FDE 看现场排障 / 集成联调 / 灰度回滚 / 预期管理；运营看指标口径 / 实验设计 / 增长假设 / 复盘）；README 与 design-rationale 用一句话说明「领域包可插拔，技术三包完整，非技术用示例包 + 文档」。
  - 不因节省仓库体积删除任何已有内容；领域包很小，重点是仓库聚焦与内容可信。
- 验收：加载器启动校验单测覆盖非法包；同一测试答案在默认包下分数与改造前一致；未知 `domain` 返回 400；报告维度名来自包配置；三个完整包各自接入后 `go test ./...` 全绿，Go 包原有测试保持不变。

**M14 实现记录（已完成）**

- 加载器：`internal/domain/domainpack/pack.go` 通过 `domainpacks/embed.go` 读取 `domainpacks/*/manifest.yaml`；启动时校验 id 与目录一致、id 唯一、方向存在、难度合法、每考点 3 条 expected_points、`source` 为 `curated` 时禁止带 URL（`retrieved` 必须带 URL）、lexicon 非空、rubric 权重和为 1。任何一条不满足都返回错误，不静默跳过。
- 迁移：原 `syllabus` 24 个考点原样迁入 `domainpacks/go-backend/manifest.yaml`（保留 `go-concurrency`、`go-arch-layering` 等原 ID）；`internal/domain/syllabus` 退化为兼容层，旧 API 与测试不变。
- 新增包：`java-backend` 11 个考点（JVM 3 / Spring 6 / MySQL 2），`ai-agent` 12 个考点（LLM 4 / RAG 4 / 服务化 4）；AI 包用 `system_design` 替换 `structure`，证明维度不是硬编码五维。
- 会话域：`POST /v1/sessions` 接受 `domain`（默认 `go-backend`），未知值返回 400；`sessions.domain` 落库（`migrations/0005_m14.sql`）；`GET /v1/domains` 暴露 id/name/版本/方向/rubric/考点数，前端下拉与展示不写死包列表以外的数据。
- 维度贯通：`evaluation.Rubric` 支持任意维度顺序/名称/权重；评估 prompt、JSON 校验、heuristic 评分、`get_session_overview`、报告降级渲染与前端得分条/雷达图全部读包配置。前端仅在样例模式使用默认五维兜底。
- 示例与文档：`docs/domainpacks/examples/solution-presales.example.yaml`、`ops-growth.example.yaml` 各 2 个考点，`docs/domainpacks/solution-and-ops.md` 记录售前/FDE/运营的完整考点与维度；示例位于 `docs/` 下，不在 embed 范围内，测试断言不会被加载。
- 测试：`internal/domain/domainpack/pack_test.go` 覆盖三包完整性、权重和、curated 无 URL、非法包逐项拒绝、示例目录不加载、Plan 去重与确定性黄金用例；API 测试覆盖未知 domain 400、rubric 透出与 `/v1/domains`。
- 验证：`gofmt -l .` 为空；`go vet ./...`、`go test ./...`、`go test -tags=eval ./internal/agent/eval`、`go test -tags=integration ./...` 全绿（integration 使用本地 PG 5433 / Redis 6380）。

### M15 JD 采集与匹配（已完成）

- 采集范围：仅公开可访问页面——公司招聘官网、Greenhouse / Lever / Workable 等公开 ATS 接口、公司自有岗位页。遵守 `robots.txt`，每域名限速（个位数/分钟），失败指数退避；不绕验证码、不模拟登录、不伪造 UA、不托管用户账号。
- 用户提供 Boss 直聘等平台内容时，只接受用户自己粘贴的文本，系统负责解析与匹配；不实现代爬。
- 产出（不做投递）：JD 结构化（岗位名 / 年限 / 必备技能 / 加分项 / 关键词）→ 与简历匹配度打分（复用 M12 的 `screen_score` 口径）→ 缺口清单（缺哪些关键词、缺哪些经历）→ 用该 JD 生成面试题纲（复用领域包 Plan）。
- 接口：`POST /v1/jd/analyze`（URL 或粘贴文本）、`GET /v1/jd/{id}`；结果缓存落库并带 `fetched_at` 与来源 URL。
- 前端：粘贴文本 / 输入链接 → 结果卡（匹配度环形 + 缺关键词 chips + 生成题纲按钮）。
- 采集策略口径：控频优先，不对抗反爬；抓取失败明确报错，不伪造内容。

### M16 申请材料包（已完成）

- 按 JD 生成：定制简历摘要（只重排真实事实，不新增虚构内容）、一段针对岗位的开场、复用的面试题纲、预演问题与缺口清单；打包为可复制文本或 Markdown 下载。
- 明确不做自动投递：不提供表单自动填写、不批量提交、不保存他人账号；三条理由写入 `design-rationale.md`（平台协议与个人信息合规、各家公司表单结构不可泛化、代投会掩盖匹配问题）。
- 验收：生成内容可逐条回指原始简历事实；Markdown 下载可打开；不出现虚构指标或职责。

---

### M17 鉴权与用户隔离（已完成）

- 账号：`users` 表；注册 / 登录用邮箱 + 密码（bcrypt，cost 10），JWT 为自实现 HS256，默认 24h，校验只看签名与 `exp`，不查会话表。
- 归属：`sessions / runs / run_steps / session_events / turns / evaluations / reports / review_cards / jd_listings` 均加 `user_id` + 索引；子表写入时从 `sessions` 派生 owner，避免调用方漏传。
- 越权：全站统一 **404**（不泄露资源存在性）；未登录为 **401**。SSE 在订阅前校验会话归属；EventSource 无法带 Header，仅 `/events` 允许 `?access_token=`。
- 匿名 demo：每次 `POST /v1/auth/demo` 创建独立临时用户，天然隔离；单 IP 独立限流（默认 5 次 / 小时，进程内窗口）。
- 数据迁移：`0008_auth.sql` 把历史行回填到 `usr_legacy`；该用户密码哈希为空，无法通过任何登录路径认证，旧数据不会暴露给新账号。回填后删除列默认值，新行漏写 owner 会直接失败。
- 验收：未登录 401、跨用户 404、demo 用户互不可见、SSE 越权拒绝均有测试覆盖；既有测试全绿。

---

### M18 跨实例 worker、限流与死信（已完成）

- **队列不引中间件**：新建 run 仍是 `running` + 空租约，即「已入队」；worker 用 `SELECT … FOR UPDATE SKIP LOCKED` 认领，可认领条件是 `status IN ('running','interrupted')` 且租约为空或已过期，同时用 `NOT EXISTS` 排除同会话已有活跃租约的 run。
- **租约与续租**：认领时写 `lease_id / lease_worker / lease_expires_at` 并 `attempt+1`；处理中按 `WORKER_HEARTBEAT_INTERVAL` 续租。续租失败或返回 `false` 视为失去所有权，取消本地 run ctx；终态写入 `MarkFinishedOwned` / `RecordFailure` 均带 `lease_id` 条件做 fencing，过期 owner 不能覆盖新 owner。
- **恢复统一**：worker 模式下 `Reconcile` 只把超时 `running` 标为 `interrupted`、发事件、通知队列，不直接执行；实际重跑由认领队列从最后一个成功 `run_step` 继续。`MarkInterrupted` 带租约过期条件，避免从活跃 worker 手里抢 run。
- **并发上限**：单实例最多 `WORKER_CONCURRENCY` 个 run 并行，超出的留在队列；`WORKER_POLL_INTERVAL` 控制空队列轮询。
- **限流**：Redis 固定窗口（`INCR` + `PEXPIRE`），key 为 `rl:<action>:<userID>`；动作分创建会话 / 提交作答 / 生成报告，各自额度可配；匿名 demo 用更严格的一组额度。超限 **429 `rate_limited`** + `Retry-After` + `details`。Redis 故障 fail-open，打 `WARN` 并记 `rate_limit_degraded_total{action}`。
- **死信**：失败写 `last_error` / `last_error_class`（`model` / `state_conflict` / `internal` / `panic`），`failure_count` 达到 `RUN_MAX_FAILURES` 进入 `dead`；`GET /v1/runs` 查询本人失败 run 及原因，`POST …/runs/{run_id}/retry` 重新入队（`dead` 重置失败计数，`failed` / `interrupted` 保留累计）。
- **可观测**：新增 `worker_claims_total`、`lease_renew_failures_total`、`rate_limit_rejections_total{action}`、`rate_limit_degraded_total{action}`、`dead_letters_total{class,kind}`；日志补 `worker_id` / `lease_id`。
- **迁移**：`0009_worker.sql` 给 `runs` 加租约、失败与死信字段及索引；历史行 lease 空、失败计数 0，旧 `running` run 可立即认领，历史失败 run 不自动进入死信。
- **验收**：两 worker 并发只认领一次、租约过期后重新认领、限流 429、连续失败进入死信、人工重试后重新执行均有测试覆盖；既有 202 + run_id 契约与事件顺序不变，评分逻辑、M6 阈值与回归集未改动。

---

### M19 评测体系加固（已完成）

- **修测试抖动**：`TestLockerAgainstRealRedis` 不再依赖 800ms 租约与固定 sleep；TTL / acquire timeout / retry interval 全部通过 `LockOptions` 显式注入，租约到期用轮询 key 消失来等待，测试时长与机器负载解耦。其余时间相关测试审计：锁单测与限流窗口用 miniredis `FastForward`，worker 租约测试直接传入显式 `leaseUntil`，没有依赖真实等待。
- **回归集 60 条**：Go 后端 35、AI Agent 25。类型覆盖 high 15 / mid 18 / low 7 / empty 3 / exaggerated 3 / off_topic 3 / jargon 3 / unverified 3 / conclusion_only 3 / verbose_offtopic 2，新增「术语堆砌但无实践」「指标出处不明」「只答结论无机制」「答得长但偏离问题」四类。每条带 `domain`、`expected_band`、五维人工分、`notes` 与 `evidence_anchor`；`evidence_anchor` 必须是答案原文的子串，加载时校验。
- **评测面板**：`GET /eval` 服务端渲染（内嵌模板，无前端构建），读取 `docs/eval-results/*.json`，不新增数据库表、不触发模型调用。展示最近结果文件、五个门槛指标、逐条失准（期望档 → 实际档、偏差维度、锚点、notes），失准按领域包分组。
- **A/B 对比**：`internal/agent/eval.Compare` + `cmd/evaldiff` + `/eval?base=&candidate=`，输出五个指标差值、门槛状态变化，以及 `improved / regressed / changed` 判定翻转；没有 API key 也能对比历史文件。
- **门槛理由**：schema 100% 因为解析失败即不可用；band_hit 80% 因为三档是产品结论、允许边界抖动；adjacent 95% 因为 ±1 档可接受、跨两档才是明显误判；dimension_mae 0.6 因为五维是 1–5 分制、约 12% 满量程；evidence_quote 100% 作为硬不变量单列。
- **基线与现状**：真实模型基线已重跑为 `eval-v4` 的 60 条数据（2026-11-07，`deepseek-chat`→`deepseek-flash` / DeepSeek-V4.1-Flash-1017），五个门槛全部通过：schema 100%、band_hit 80.0%（48/60，较 v3 −1.7pt）、adjacent 100%、MAE 0.4733（较 v3 改善）、evidence 100%；Go 后端 82.9%、AI Agent 76.0%。同集 A/B 实际档位翻转 2 条且均为回退（`ai-mid-03`、`ai-mid-07` 3→5）；v4 的净效应是命中率变差、MAE 改善，不是提升。明细与成本见 5.3。

---

## 13. 不做清单（YAGNI）

明确不做，避免范围蔓延：

- **语音/ASR/TTS**：只做文本输入输出。
- **支付/订阅/计费**：不做。
- **RBAC / 找回密码 / 第三方登录**：M17 只做邮箱密码 + JWT；没有角色权限、邮件找回、OAuth 与刷新令牌。
- **小程序/多端**：不做；只提供 HTTP/SSE API。
- **前端 SPA / 大屏**：最多提供 curl/HTML 演示页，不做完整前端工程。
- **简历 OCR / 图片解析**：MVP 支持 PDF/DOCX/TXT/MD 本地解析；PNG/JPG 仅返回「需 OCR/多模态，尚未启用」，不实现。
- **向量数据库 / RAG 知识库**：报告只回查本次会话，不接外部知识库。
- **多模型路由 / 模型评测平台 / 微调**：单一模型适配器即可。
- **多 Agent 协作 / 自主规划 Agent**：单 Agent 状态机。
- **代码执行沙箱 / 在线判题**：不做代码运行。
- **实时多人面试 / 视频**：不做。
- **Kubernetes / 服务网格 / 多区域**：单机 + Docker Compose。
- **完整事件溯源 / CQRS**：`session_events` 只用于 SSE 与审计，不做全量重建。
- **题库管理后台 / CMS**：题库先用内置种子数据。
- **国际化**：MVP 中文；`locale` 字段预留但不做多语言包。
- **A/B 实验平台、复杂指标平台**：只打结构化日志与基础指标。

---

## 14. 风险与开放问题

| 风险 | 影响 | 缓解 |
| --- | --- | --- |
| eino API 迭代 | 升级破坏编译 | agent 层隔离 + 契约测试 + 锁定小版本 |
| LLM 评分不稳定 | 同答案分数漂移 | 固定 rubric 锚点 + temperature 低 + 两遍汇总 + 证据引用 |
| 长答案/长报告超 token | 成本与截断 | 分段工具回查 + 摘要分层 + 硬性 token 上限 |
| SSE 事件重复 | 前端重复渲染 | 事件 `event_id` 去重；至少一次语义明示 |
| 崩溃时模型重复调用 | 成本增加 | 节点产物缓存；只重跑未成功节点 |
| 鉴权被绕过或越权 | 数据泄露 | 全站 Bearer 校验 + 归属统一 404；SSE 订阅前校验；demo 用户每次新建、按 IP 限流 |
| prompt injection（答案内含指令） | 评分被操控 | 数据/指令分隔 + 输出 schema 校验 + critic |

开放问题（M2 前定稿）：

- 题纲生成用固定模板 + 模型选点，还是完全由模型规划？（倾向固定主题骨架 + 模型选具体问题，可控）
- 报告是否支持导出 Markdown/PDF？MVP 只返回 Markdown。
- 单场是否允许候选人主动跳过？MVP 允许 `finish`，单题跳过后续再加。

---

## 15. 模块学习路径（面试讲解用）

按这个顺序读代码/讲设计，每一层都能独立说清：

1. **API 层**：为什么 POST 返回 202、SSE 单独订阅；幂等键放 Header 的理由；错误码如何映射。
2. **应用层**：一次 AnswerRun 的事务边界；锁与 CAS；事件与业务写入的一致性。
3. **Agent 层**：eino 图节点如何划分；为什么评估不流式、问题可流式；结构化输出校验与修复。
4. **领域层**：状态机不变量；追问预算；rubric 计分公式与证据规则。
5. **存储层**：为什么事件表是 SSE 事实来源；Redis 为什么不是正确性依赖；崩溃恢复的 step 日志。
6. **横切**：日志字段、重试策略、超时预算、降级路径。

每个里程碑结束时补一段「本模块的取舍与踩坑」，作为面试讲解素材。
