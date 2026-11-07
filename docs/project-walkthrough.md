# 项目拆解读物

这份材料按「一次真实请求怎么走完」的顺序拆代码，面向想照着仓库自学的读者。每个模块固定四段：**这块解决什么问题 / 关键实现 / 当时怎么选的 / 被追问时怎么答**。

行号会漂移，本文只写**文件与函数名**，不写死行号；找代码时以符号名为准。最后更新：2026-11-07。

先记住五条主线：

- **分层**：`cmd/server` 只装配；`internal/api/http` 只做协议转换；`internal/app` 编排用例；`internal/agent` 是 eino 图与模型；`internal/domain` 放规则；`internal/store` 落库；`internal/ports` 定义边界。
- **确定性优先**：简历诊断、JD 匹配、追问裁决、评分公式、出题回退、报告回退、复习排期都是规则；模型只负责评估文字、出题文字和报告点评。
- **一次写操作 = 一个 run**：run 有幂等键，内部步骤落 `run_steps`，失败可重试，进程崩溃可对账恢复。
- **每个资源都有 owner**：请求经 JWT 得到 `user_id`，所有查询按 `user_id` 过滤，跨账号统一 404。
- **正确性靠 PostgreSQL，Redis 只做加速**：锁和限流在 Redis，丢了可以回退或 fail-open；会话、回合、run、事件、报告、复习卡都以 PG 为唯一事实源。

---

## 1. 入口、分层与中间件

**这块解决什么问题**：把 HTTP 协议、业务编排、Agent 决策、领域规则和存储实现拆开，让依赖方向单一，测试可以替换存储与模型，不必起真库、不必调真模型。

**关键实现**：
- 装配点：[cmd/server/main.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/cmd/server/main.go) 的 `run()`：读配置 → 开 PostgreSQL → 按环境变量建模型客户端（无 key 回退 `heuristic`）→ 建指标 / 用量 / tracer → Redis 不可用时回退进程锁 → 组装 `app.Service` 与 worker → 执行迁移 → 启动时 `Reconcile` → 挂路由、HTTP Server、优雅退出。
- 端口边界：[internal/ports/repository.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/ports/repository.go) 的 `Repositories` / `Store`，以及 `Locker`、`RateLimiter`、`Model`、`Events` 等接口。
- 路由表：[internal/api/http/router.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/api/http/router.go) 的 `NewRouter`，从 `/healthz`、`/metrics`、`/demo`、`/eval` 到鉴权、简历、JD、会话、run、复习。
- 中间件顺序：[internal/api/http/middleware.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/api/http/middleware.go) 的 `withRequestID`（生成 `request_id` / `trace_id`、开 span）→ `withAuth`（校验 JWT、注入身份）→ handler 内的 `allowRateLimit`（按动作限流）。
- 存储：`internal/store/postgres` 是生产实现，`internal/store/memory` 是契约测试替身，`internal/store/redis` 放锁与固定窗口计数。

**当时怎么选的**：用 `ports` 接口把「业务需要什么」和「Postgres 怎么实现」分开，装配只在 `main.go` 发生；不引 DI 框架，因为依赖数量还在人工可读范围内。代价是多一层接口和两套存储实现，行为必须靠契约测试对齐；`postgres.Store.WithTx` 把同一事务内的仓库重新绑定到 `tx`，这层包装写错时很难从 handler 看出来。

**被追问时怎么答**：
1. **「为什么不直接在 handler 里用 pgx？」** handler 只该关心 HTTP 语义；直接依赖 pgx 会让参数校验、异步 run、事件流和存储细节缠在一起，也无法用内存实现跑快速测试。边界是 `ports.Repositories`，handler 只调 `app.Service`。
2. **「换存储要改哪些文件？」** 新增一个实现 `ports.Store` 的包，再在 `main.go` 换装配；`app` 与 `api` 不动。前提是契约测试覆盖到乐观锁、唯一约束这些语义。
3. **「为什么 Redis 挂了服务还能起？」** `main.go` 做了显式回退：Redis `Ping` 失败就换进程内锁并打 `WARN`。代价是这时只有单实例安全；限流则是 fail-open，不阻断业务。

---

## 2. 鉴权与用户隔离（含 demo 匿名通道与旧数据回填）

**这块解决什么问题**：从「谁都能读写同一批会话」变成「每个账号只能看到自己的资源」；同时保留零摩擦的匿名演示，并让历史数据不暴露给新用户。

**关键实现**：
- 账号与令牌：[internal/auth/service.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/auth/service.go) 的 `Register` / `Login` / `Demo`；密码哈希在 [internal/auth/password.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/auth/password.go)（bcrypt），HS256 JWT 在 [internal/auth/token.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/auth/token.go)。
- 请求鉴权：[internal/api/http/auth.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/api/http/auth.go) 的 `withAuth`：除 `/healthz`、`/metrics`、`/demo` 与三个 `/v1/auth/*` 外，所有 `/v1` 接口要求 `Authorization: Bearer`；缺失或过期返回 `401 unauthorized`。
- 归属校验：`requireSessionOwner` / `requireSessionOwnership` 先查资源，再比对 `OwnerID`，不匹配统一转 `ports.ErrNotFound`（HTTP 404）。列表接口（如 `/v1/review?session_id=`）走同一个检查，避免从 query 绕过。
- SSE 例外：`EventSource` 不能自定义 Header，因此 `GET /v1/sessions/{id}/events` 额外接受 `?access_token=`；其他路径不接受查询参数令牌。
- demo 匿名：`auth.Service.Demo` 每次调用都创建新用户 `demo_<ULID>`，JWT 里带这个 `user_id`；`demoLogin` 只按客户端 IP 做进程内滑动窗口限流（`demoLimiter`），业务动作（建会话 / 作答 / 报告）另走 Redis 的严格额度。
- 旧数据回填：[migrations/0008_auth.sql](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/migrations/0008_auth.sql) 给相关表加 `user_id`，把历史行回填到占位用户 `usr_legacy`；该用户密码哈希为空，任何登录路径都无法认证为它。迁移后新行列去掉默认值，漏写 owner 会直接失败。

**当时怎么选的**：用无状态 JWT 换掉服务端 session 表，减少一张表和一次查询；用 bcrypt 而不是自造哈希；用「跨账号 404」而不是 403，避免泄露资源是否存在。代价是只有单 token，没有刷新令牌、找回密码、RBAC 和第三方登录；demo 登录次数仍是进程内计数，多副本要统一到 Redis 才能严格。

**被追问时怎么答**：
1. **「demo 是匿名的，为什么还能隔离？」** 匿名不等于共享账号。每次 demo 登录都生成一个独立 `user_id`，所有资源带 owner、所有查询按 owner 过滤，跨账号 404，所以两个匿名访客互相看不到。
2. **「历史数据会不会被新注册账号看到？」** 不会。历史行归到 `usr_legacy`，该账号没有可用密码哈希，登录路径无法认证为它；新用户拿到的 JWT 里是自己的 id，查询自然过滤掉旧行。
3. **「为什么用 JWT，不用服务端 session？」** 当前只有一个 API 进程，无状态令牌省掉共享 session 存储；代价是无法即时吊销单个令牌。要做得更严格，需要加 token 版本号或短期 access + refresh。
4. **「JWT_SECRET 没配置会怎样？」** 启动时生成随机临时值并打 `WARN`，重启后旧 token 全部失效；生产必须显式设置。
5. **「跨账号为什么返回 404 而不是 403？」** 403 等于承认「这个 id 存在但不属于你」；404 把「不存在」和「无权」合并，少一个枚举面。

---

## 3. 会话状态机与步骤日志

**这块解决什么问题**：面试是有严格先后顺序的长事务——不能重复答同一题、不能在未收尾时出报告、进程挂了要能接着跑。状态机负责「什么命令在什么状态下合法」，步骤日志负责「跑到哪了、能不能重放」。

**关键实现**：
- 配置与硬限制：[internal/domain/session/state.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/domain/session/state.go) 的默认值与校验：题量 1–12、单题追问 0–3、深度 1–4、总追问 0–10、时长 1–60 分钟。
- 状态转移：同一文件的 `Start`、`Finish`、`AskFollowup`、`AskNextQuestion`、`BeginReport`、`Complete`、`Fail`、`Abort`；非法转移统一返回 `TransitionError`，`Abort` 对已中止会话幂等。
- run / step 模型：[internal/domain/session/run.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/domain/session/run.go) 定义 `Run`、`RunStep`、状态与 kind。
- 执行与步骤缓存：[internal/app/runner.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/app/runner.go) 的 `Execute` 按 `start / answer / report` 分派；`recordStep` 先查已完成步骤，命中就反序列化复用，否则 `Steps.Start` → 执行 → `Complete`。
- 恢复：[internal/app/service.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/app/service.go) 的 `Reconcile` 扫描超时 `running` run，标记 `interrupted` 并追加事件；worker 模式下只做标记并唤醒队列，执行仍由 `Claim` 接管。
- 持久化：[internal/store/postgres/repository.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/store/postgres/repository.go) 的 `UpdateState` 用 `WHERE state_version` 做乐观锁；步骤读写也在同一仓库。

**当时怎么选的**：状态机写成不依赖数据库的纯函数，规则可以单测；`state_version` 让并发写至少有一方失败，而不是静默覆盖；步骤日志让「评估已完成、提交没完成」这种半成品可以重放，比框架 checkpoint 更贴近业务事务。代价是每次写要同时维护 session、turn、evaluation、run、step 多张表，事务边界必须很准；`recordStep` 缓存的是 JSON，结构变更后的旧步骤可能反序列化失败。

**被追问时怎么答**：
1. **「两个请求同时答同一题会怎样？」** 先被 session 锁串行化；后到者在 `Answer` 里看到 `PendingTurnID` 已变或 turn 已回答，返回 `ErrStateConflict`；即使锁失效，`UpdateState` 的版本号比较和 turn 状态检查仍会拦住。
2. **「为什么 `Abort` 可以重复调用？」** 它要对「用户连点两次取消」幂等：已 aborted 直接成功返回，completed/failed 则拒绝，避免把已完成的会话改成 aborted。
3. **「步骤日志和事件流有什么区别？」** `run_steps` 是执行侧的内部断点，用来重放/恢复；`session_events` 是对外的只追加事实流，供 SSE 渲染和审计。两者可以写在同一事务里，但用途不同。
4. **「崩溃恢复到什么粒度？」** 到「最后一个成功的 run step」这一级，不是精确到某一行指令。重试会重跑当前节点；模型调用可能重复，但业务写入有事务、幂等键和 CAS 兜底。

---

## 4. 一次作答链路与自适应追问

**这块解决什么问题**：把「收到一段回答」拆成可替换、可降级、可复现的四步：取快照、调模型评估、用规则决定追问/下一题/收尾、生成下一题并原子提交。

**关键实现**：
- 接单：[internal/app/service.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/app/service.go) 的 `Answer`：校验 `turn_id`、截断超长回答、算幂等键、检查会话为 `active/awaiting_answer` 且 turn 是 pending、创建 answer run。
- 图编排：[internal/agent/interview/graph.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/agent/interview/graph.go) 的 `compose.Graph`：`snapshot → evaluate → decide`，`decide` 分支到 `ask_followup` / `ask_next` / `END`。
- 节点注入：[internal/app/answer.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/app/answer.go) 把评估器、裁决、出题函数注入 `AnswerDeps`。
- 评估：[internal/agent/interview/evaluate.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/agent/interview/evaluate.go) 的 `Evaluator.Evaluate`：拼 prompt、调模型、解析 JSON、按 rubric 校验，最多 2 次修复；模型不可用或输出非法时降级为 `model_output_invalid` 继续面试。
- 裁决：[internal/domain/followup/decide.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/domain/followup/decide.go) 的 `Decide`，门槛顺序是：时间到 → 题纲耗尽 → 总追问预算 → 深度上限 → 单题追问上限 → 「不会」识别 → 评估降级 → 要点已覆盖 → 模型建议 → 兜底推进。上限在 `policy.go`。
- 出题：[internal/agent/interview/question.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/agent/interview/question.go)；模型失败走 `FallbackQuestion`，保证下一题一定能出。
- 评分公式：[internal/domain/evaluation/rubric.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/domain/evaluation/rubric.go) 的 `ComputeScoreWith`：维度分/5 × 权重 × 100，再叠加 bonus/penalty 并做上限。
- 原子提交：[internal/app/answer_commit.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/app/answer_commit.go) 在同一个事务里更新 turn、upsert evaluation、按裁决改 session、创建下一 turn、追加事件、`MarkFinishedOwned`。

**当时怎么选的**：把评估与裁决拆开，模型只产出受约束的维度建议和文字，是否追问由确定性规则决定，避免「模型想追问就无限追问」。覆盖判定还要求证据引文真的出现在回答里，防止模型编造「已覆盖」。代价是规则会压掉一些合理的追问，评估修复会放大 token 消耗；模型降级时面试还能继续，但那道题没有可信分数。

**被追问时怎么答**：
1. **「模型建议追问，但候选人已经答全了，听谁的？」** 听规则。覆盖完成排在模型建议之前，直接进下一题。
2. **「模型输出不是 JSON 怎么办？」** 先抽第一个 `{` 到最后一个 `}` 再解析；失败把错误反馈给模型重试，最多 2 次；仍失败就写一条降级评估，本题不计分、不追问，面试继续。
3. **「为什么评估和提交要分两个事务？」** 评估是外部慢调用，不能占着数据库事务；提交阶段只做短事务。中间崩溃时，`run_steps` 里已有评估结果，重试可跳过模型调用直接提交。
4. **「10 级门槛是不是过度设计？」** 每一级都对应一个真实的失控方式：时间、题纲、预算、深度、单题上限防无限追问；「不会」识别防追问已放弃的人；评估降级防拿坏分数继续追问；覆盖门防重复问已答全的点；模型建议只是最后一层偏好。

---

## 5. 报告（工具回查）

**这块解决什么问题**：面试结束后，把散落在 session、turn、evaluation、plan 里的数据收成一个可解释的报告；模型只写点评，不改分数、不编事实。

**关键实现**：
- 证据收集：[internal/agent/tools/tools.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/agent/tools/tools.go) 的 `Collect`：`SessionOverview` 读会话、题纲、turn、评估并聚合分数；`ListWeakTurns`、`SearchTurns`、`Turn` 按条件回查。每次回查都记录耗时、结果长度、是否截断。
- 报告生成：[internal/app/report.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/app/report.go) 的 `Finish` / `Report` / `startReport`：先确认会话可收尾、报告未重复，再 `Collect` → `Generate`。
- 模型点评与降级：[internal/agent/report/build.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/agent/report/build.go) 的 `Generate`：先构造确定性 `Fallback` 报告，再把模型输出合并进去；0 题、模型不可用、输出校验失败都保留 fallback 并标 `degraded`。
- 落库：[internal/app/report.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/app/report.go) 的 `Runner.commitReport`：补本场用量快照、渲染 markdown、在事务里完成会话状态、写报告、生成复习卡。
- 报告结构：[internal/domain/report/report.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/domain/report/report.go) 的 `Record` 与五个 section。

**当时怎么选的**：报告的可信度来自「分数是算出来的，事实是查出来的」；模型只拿到压缩后的证据包，避免自由发挥。回查不是让模型自主调工具，而是代码先按固定采集计划查完，`ToolCalls` 只是审计记录。代价是证据包有固定截断上限（回答摘要、弱项条数、搜索结果都会截），长回答的细节会丢；模型不可用时报告仍能出，但点评是模板化的。

**被追问时怎么答**：
1. **「所谓工具回查，是让模型自己调工具吗？」** 不是。回查在 `Collect` 里由代码先做完，模型看到的是已经查好的 bundle；`get_turn` 已实现，但当前固定采集计划没有调用它。
2. **「报告里的总分是谁算的？」** 确定性公式。`ComputeScoreWith` 按 rubric 权重算基础分，bonus/penalty 有上下限；模型只写 verdict、维度点评和逐题建议，且这些文字要过长度与 `turn_id` 校验。
3. **「模型挂了报告还能出吗？」** 能。非取消类错误直接返回 fallback，`degraded=true` 且带 `model_unavailable`；只有 context 被取消才返回错误，留给重试。
4. **「为什么不让模型自由写总评？」** 自由写会引入不可复算的分数和无法引用的事实；当前做法牺牲一点文采，换取每次报告都能用同一套规则复算。

---

## 6. 复习（SM-2 与三种 scoring_mode）

**这块解决什么问题**：把报告里的弱项变成可执行的复习计划，用固定间隔算法排期；同时明确「系统到底是按客观分排程，还是按用户自评排程」，不把 heuristic 的长度估算包装成客观评分。

**关键实现**：
- 卡片来源：[internal/domain/review/review.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/domain/review/review.go) 的 `BuildCards`：题目有缺失点、分数低于阈值、或最弱维度 ≤2 分时建卡。
- SM-2：[internal/domain/review/review.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/domain/review/review.go) 的 `GradeCard`：EF 按 `0.1 - (5-grade)×(0.08+(5-grade)×0.02)` 调整，下限 `MinEase`；grade<3 重置重复次数、间隔 1 天；grade≥3 第 1 次 1 天、第 2 次 6 天、之后上次间隔 × EF。
- 三种评分来源：[internal/domain/review/service.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/domain/review/service.go) 的 `AnswerVariant`：
  - `objective`：真实模型给出客观分，客观 grade 驱动 SM-2；
  - `length_heuristic`：无 key 时的长度估算，只作参考分，排程按自评，不覆盖历史客观字段；
  - `self_only`：没有接评估链路，只按自评排程。
- 适配器：[internal/app/review_eval.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/app/review_eval.go) 的 `ReviewEvaluator`：`objective` 只在底层是真实模型时为 true；heuristic 强制标 `length_heuristic`。
- 每日上限与优先级：[internal/domain/review/service.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/domain/review/service.go) 的 `Due`：按「高频考点 > 历史低分 > 普通题 > 到期时间」排序；`REVIEW_DAILY_LIMIT`（默认 20）只裁剪展示队列，不改 SM-2 的 `due_at`。
- 变式题：[internal/domain/review/review.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/domain/review/review.go) 的 `BuildVariant`：题纲 `expected_points` + 确定性打乱，不调模型。

**当时怎么选的**：SM-2 是纯函数，容易测、结果可解释、不需要模型；但「用户真的记住了吗」只有客观评估才能回答，因此把评分来源显式分级。没有 key 时宁可按自评排程，也不把长度估算说成客观分。代价是 `length_heuristic` 的分歧只能提示「差异较大」，不能下「高估/低估」结论；每日上限也只是展示层，不改变卡片真实到期时间。

**被追问时怎么答**：
1. **「为什么 grade 小于 3 要重置？」** SM-2 的 lapse 处理：回忆失败说明之前的长间隔失效，重复次数归零、明天再来，避免把不会的卡片越推越远。
2. **「没有 key 时复习评分还有效吗？」** 有效的是排程与自评闭环，不是语义客观性。`length_heuristic` 只作参考、排程按自评，接口与面板都明确标注，不冒充 `objective`。
3. **「每日上限会改到期时间吗？」** 不会。它只决定今天展示多少张；未展示的卡仍按原 `due_at` 留在队列里，`deferred_count` / `remaining` 如实返回。
4. **「三种模式怎么选？」** 由装配时底层客户端决定：真实模型 → `objective`；heuristic → `length_heuristic`；没接评估器 → `self_only`。不是每次请求临时猜。

---

## 7. 简历诊断

**这块解决什么问题**：在不上传第三方、不调用模型的前提下，给简历一个可解释的分数、分维度扣分、逐条问题与「差 → 好」改写，并区分「机器能判」和「需要人工确认」。

**关键实现**：
- 主流程：[internal/domain/resume/diagnosis.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/domain/resume/diagnosis.go) 的 `RuleEngine.Analyze`：清洗文本 → 分段 → 遍历规则 → 命中检测器 → 生成 issue/manual/redline → 算文档分、双分、band。
- 红线与封顶：同一文件的红线处理：命中后清零 A 维度、文档分封顶 69，综合分也不得高于 69。
- 双分口径：`screenScore` 看岗位关键词、量化、结构等；`techScore` 看技术栈、边界、部署证据。
- 检测器：[internal/domain/resume/checks.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/domain/resume/checks.go) 的注册表；规则加载在 [internal/domain/resume/ruleset.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/domain/resume/ruleset.go) 的 `Check`（`auto` / `auto_manual` / `manual`、扣分、红线）。
- 附件解析：[internal/domain/resume/document.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/domain/resume/document.go) 的 `Extract`；接口在 `internal/api/http/resume.go` / `resume_analyze.go`。

**当时怎么选的**：规则写在 YAML，检测器写在 Go，规则 ID、扣分、类型适用范围都能单独改；`auto_manual` 把「机器只能判一半」显式标出来，避免假装全自动。代价是正则对表达变体很敏感，规则覆盖不到新写法；分段/类型识别依赖日期和标题，格式很花的简历会落到 `unknown` 并要求确认；没有 OCR，图片简历直接拒绝。

**被追问时怎么答**：
1. **「为什么不用模型直接打分？」** 分数要可复现、可解释、可审计；同一份简历同一规则版本必须同分，模型做不到。简历诊断的 `ModelCalled` 恒为 false。
2. **「红线命中后分数怎么算？」** A 维度直接归零、文档分封顶 69，综合分也不能超过 69。这是「不能过筛」的硬语义，不是普通扣分。
3. **「同内容换个类型分数为什么不同？」** 规则有 `AppliesTo` 和类型画像，实习/项目/工作经历的扣分与权重不同；类型推不出时列进 `needs_confirmation`，不硬猜。
4. **「为什么不做 OCR？」** 图片与扫描件需要 OCR 或多模态，属于另一条成本线；当前明确返回 `image_requires_ocr`，让用户改粘贴，比静默失败诚实。

---

## 8. JD 匹配与技能别名归一

**这块解决什么问题**：把一页岗位描述变成结构化岗位、匹配度、技能缺口、题纲和申请材料包；同时让「Kubernetes / k8s / 容器编排」这类同义写法能被可解释地合并，而不是靠向量相似度猜。

**关键实现**：
- 解析与匹配：[internal/domain/jd/jd.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/domain/jd/jd.go) 的 `Parse`；[internal/domain/jd/match.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/domain/jd/match.go) 的 `AnalyzeListing` / `matchResume`：有简历文本时复用简历诊断的 `screenScore` 口径，缺口与题纲同文件。
- 别名表：[internal/domain/skills/registry.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/domain/skills/registry.go) 的 `Check` / `Covered` / `Normalize`。四级匹配按顺序：
  1. `exact`：原文精确命中；已知 ASCII 技能按 token 边界匹配，避免 `redis` 命中 `redisson`；
  2. `alias`：同义写法双向命中（JD 写 k8s、简历写 Kubernetes 也算）；
  3. `child`：简历里的下位技能覆盖上位要求（如写了 Kafka，可覆盖「消息队列」）；
  4. `parent` / `sibling` / `related`：只作弱提示，永不计分；
  5. `fuzzy`：编辑距离兜底，只在 ≥6 字符、同首字母、长度差 ≤1、距离 ≤1 且不属于其他技能时给弱提示。
- 材料包：[internal/domain/jd/kit.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/domain/jd/kit.go)：摘要、开场、复用题纲、预演问题与缺口清单，明确「不代投」。
- 抓取：[internal/platform/jobfetch/jobfetch.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/platform/jobfetch/jobfetch.go)：只允许公开 http(s)，遵守 robots，按 host 限速，超时与大小上限，生产拒绝私网地址。

**当时怎么选的**：技能词表是几十个后端 / Agent 规范名，规模小且封闭，别名可以逐条枚举、评审和回归；embedding 要额外维护语料、索引与阈值，还解释不了「为什么算覆盖」。关系分层写进表里：同义合并计覆盖，上下位只允许「下位覆盖上位」，同族/相关只提示。代价是词表外的隐性技能仍会漏判，语义相近但表述完全不同的能力只能靠人工确认；没有向量检索是刻意取舍。

**被追问时怎么答**：
1. **「为什么不上向量库？」** 词表小、封闭、必须可解释；每个命中都要能回答「命中的是 alias、child 还是 related」。向量相似度给不出可复核依据，还要额外维护索引和阈值。
2. **「写了 Kafka，JD 要消息队列，算覆盖吗？」** 算。`child` 规则允许下位覆盖上位；反过来 JD 要 Kafka、简历只写「消息队列」不算，因为上位不覆盖下位。
3. **「Redis 和 Redisson 会被合并吗？」** 不会。已知 ASCII 技能按 token 边界匹配，`redis` 不会命中 `redisson`；编辑距离兜底也不会计分。
4. **「抓 URL 会不会违规？」** 抓取器只访问公开页面，先读并遵守 robots.txt（读不到就拒绝），按 host 限速、限大小和超时，不支持登录态和验证码；生产默认禁止私网地址。
5. **「材料包会不会编造经历？」** 摘要、开场、预演问题都从已解析的简历画像和 JD 事实拼出，`KitFact` 记录来源；没有自动投递接口，也不生成不存在的项目。

---

## 9. 存储、并发与会话锁

**这块解决什么问题**：多实例、重复点击、进程崩溃时，保证同一场会话不被并发写坏；同时让「读—改—写」有明确的冲突信号，而不是最后写入者获胜。

**关键实现**：
- 会话级互斥：[internal/store/redis/lock.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/store/redis/lock.go) 的 `Acquire` / `Refresh` / `Release`：`SET NX PX` 抢 key + token，同 token 重入刷新 TTL，释放用 Lua 比对 token 删除；进程内回退在 `internal/platform/lock/process.go`。
- 锁守卫：[internal/app/service.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/app/service.go) 的 `acquireSession`：所有命令统一走它；同步内联模式（`synchronous`）下按 `LOCK_TTL/3` 起 watchdog 调 `Refresh`，避免长 run 执行到一半锁过期。
- 生产 worker 模式：锁只覆盖命令临界区，不跨整个 run；run 的并发由 PG `Claim` 的「同会话只允许一个活跃租约」约束 + `state_version` CAS 兜底。
- 事务：[internal/store/postgres/store.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/store/postgres/store.go) 的 `WithTx`。
- 乐观锁：[internal/store/postgres/repository.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/store/postgres/repository.go) 的 `UpdateState`，版本不匹配返回 `ports.ErrStateConflict`。
- 幂等 run：同一仓库的 `runRepository.Create` 用 `ON CONFLICT (session_id, kind, idempotency_key) DO NOTHING`，唯一约束来自 `migrations/0001_init.sql`。
- 唯一报告 / 复习卡：`repository_m4.go` / `repository_m11.go`，数据库唯一键做最后一道防线。
- 事件：[internal/store/postgres/repository.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/store/postgres/repository.go) 插入后同事务 `pg_notify`；跨实例通知在 `notifier.go`。

**当时怎么选的**：Redis 锁把「同一会话的写」串行化，PG 约束做最后一道防线：锁 TTL 到了、实例挂了，唯一键和 `state_version` 仍能拦住重复 turn / 重复报告。事件写入同一事务并 `NOTIFY`，保证「事务提交才可见」。代价是 Redis 成为可选单点（回退进程锁后只能单实例）；锁的粒度是整场会话；没有分布式事务，跨库一致性靠「先落库、后发通知」。

**被追问时怎么答**：
1. **「锁已经互斥了，为什么还要唯一约束？」** 锁可能因 TTL、网络分区或进程崩溃失效；唯一约束是数据库层的硬保证，重复请求会命中幂等键直接重放。
2. **「锁 TTL 10s，长任务不会中途丢锁吗？」** 分两种装配。同步内联模式下锁跨整个 run，已加 `Refresh` watchdog 按 `LOCK_TTL/3` 续期；生产 worker 模式下锁只包住命令临界区，长 run 不持锁，靠 `Claim` 的同会话租约互斥和 `state_version` CAS 兜底。
3. **「`state_version` 冲突怎么处理？」** 返回 409 `state_conflict`，客户端重读会话或换幂等键重试；服务端不会覆盖别人的写，也不会自动合并旧状态。
4. **「事件为什么要 `LISTEN/NOTIFY`？」** `NOTIFY` 只负责唤醒，真正的顺序与回放仍由 `session_events` 的单调 `event_id` + `Last-Event-ID` 保证；通知丢了只是晚一点，不会丢事件。

---

## 10. 跨实例 worker、租约 fencing 与死信重试

**这块解决什么问题**：让 HTTP 命令只负责入队，后台 worker 在多实例间安全消费；进程崩溃后租约过期可被接管，重复执行不会写坏业务；连续失败的 run 进入死信，可查询、可人工重试。

**关键实现**：
- 消费池：[internal/app/worker.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/app/worker.go) 的 `WorkerPool.claimOne` / `runClaimed` / `renewLease`：每个槽位循环 `Claim`，拿到后写 `worker_id + lease_id`，执行期间按 `HeartbeatInterval` 续租；续租明确返回「租约已丢失」时立即 `cancel()` 本地执行，避免过期 owner 继续写业务；传输错误只记指标，下一轮继续尝试。
- 认领 SQL：[internal/store/postgres/repository.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/store/postgres/repository.go) 的 `Claim`：`WITH candidates … FOR UPDATE SKIP LOCKED`；只挑 `running/interrupted` 且租约为空或已过期的行，并用 `NOT EXISTS` 排除同会话已有活跃租约的 run。
- fencing：`MarkFinishedOwned` / `RecordFailure` / `RenewLease` / `ReleaseLease` 都带 `lease_id` 条件；旧 owner 即使醒来，也写不进新 owner 的终态。
- 失败与死信：`RecordFailure` 原子累加 `failure_count`，达到 `RUN_MAX_FAILURES`（默认 3）置为 `dead` 并写 `dead_at`；未达到则 `failed`，保留错误码、错误分类和最后错误。
- 查询与重试：[internal/app/service.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/app/service.go) 的 `ListRuns` / `Retry`；`Retry` 对 `dead` 重置失败计数并重新入队，对 `failed/interrupted` 保留计数继续累计，避免「点一次重试就无限复活」。
- 对账：[internal/app/service.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/app/service.go) 的 `Reconcile` 只把超时 `running` 标为 `interrupted` 并追加事件，然后唤醒队列；不在对账路径里另起一套执行逻辑。

**当时怎么选的**：队列直接放 PostgreSQL，用 `SKIP LOCKED` + 租约，不引入额外消息中间件；恢复与正常消费共用一条路径，`Reconcile` 与 worker claim 不会互相打架。代价是空队列靠 `PollInterval` 轮询（跨实例唤醒未接 `LISTEN/NOTIFY`），没有优先级、延迟队列和指数退避；恢复粒度是 step；同一会话同时只允许一个活跃 run。

**被追问时怎么答**：
1. **「为什么不用 Asynq？」** 项目已经有 PG 这个事实源，把队列、run 状态、run_steps、租约放同一张库，恢复和业务写入能共享事务与观测；Asynq 会把队列状态挪到 Redis，多一套依赖和一套状态，还要处理「消息已消费、业务未提交」的分裂。当前规模下 `SKIP LOCKED` 足够。
2. **「两个 worker 同时抢同一个 run，会重复执行吗？」** 不会。认领是一条带 `FOR UPDATE SKIP LOCKED` 的 `UPDATE … RETURNING`，两个事务要么只有一个拿到行，要么另一个跳过；后续终态写入以 `lease_id` 做条件，旧 owner 写不进去。
3. **「租约过期后接管，会不会重复写 turn？」** 不会写两次业务终态。重跑从最后一个成功 `run_step` 继续，提交阶段有幂等键、唯一约束和 `state_version` CAS；可能重复的是模型调用这类外部慢操作。
4. **「死信之后怎么办？」** `GET /v1/runs?status=dead` 能查到原因和最后错误；`POST /v1/sessions/{id}/runs/{run_id}/retry` 由人显式重试，`dead` 获得全新失败预算。
5. **「为什么不用 Redis 队列？」** Redis 在这里是可选依赖；把它变成必须的队列会改变故障模型。当前设计允许 Redis 挂掉时锁回退、限流 fail-open，但队列和恢复不受影响。

---

## 11. 限流与 fail-open 取舍

**这块解决什么问题**：防止单个用户或匿名 demo 刷爆建会话、提交作答、生成报告这三类昂贵动作；同时不让限流组件自己变成整站单点。

**关键实现**：
- 配置：[internal/api/http/ratelimit.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/api/http/ratelimit.go) 的 `RateLimitConfig`：窗口、普通用户三类额度、demo 三类更严格额度。
- 执行：同一文件的 `allowRateLimit`：按 `用户 + 动作` 生成 key，调 `RateLimiter.Allow`；超限返回 429 `rate_limited`，带 `Retry-After` 和动作、额度、窗口、剩余等待秒数。
- 计数：[internal/store/redis/ratelimit.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/store/redis/ratelimit.go) 固定窗口计数。
- 降级：Redis 不可用时 fail-open（放行），打 `WARN`，累加 `rate_limit_degraded_total{action}`；不静默吞掉，也不因为限流后端故障拒绝整站。
- demo 登录：`auth.go` 的 `demoLimiter` 是进程内滑动窗口，只管「拿匿名 token」这一件事；建会话 / 作答 / 报告仍走上面的 Redis 额度。

**当时怎么选的**：固定窗口实现简单、语义直观，额度按动作拆开，避免「建会话太多」把「提交作答」也一起限死；匿名 demo 生命周期短、滥用成本低，所以额度更紧。fail-open 是明确的取舍：限流是保护措施，不是正确性依赖，后端故障时宁可暂时失去保护，也不让所有用户不可用。代价是固定窗口在边界处会有双倍突发；Redis 故障期间没有限流保护；demo 登录次数仍是单进程计数。

**被追问时怎么答**：
1. **「为什么 fail-open，不怕被打爆吗？」** 限流后端不是正确性依赖，拒绝整站比暂时失去限流更糟；降级有 `WARN` 和指标，能被发现。要更严格可以改成 fail-closed，但那是另一个可用性取舍。
2. **「为什么按动作拆三个额度？」** 建会话、提交作答、生成报告的成本和滥用方式不同；合并成一个总池会让正常面试被「建会话」额度拖累。
3. **「固定窗口和滑动窗口差在哪？」** 固定窗口实现简单，但窗口边界可能放过约两倍突发；滑动窗口/令牌桶更平滑，代价是更多状态和实现复杂度。当前规模接受前者。
4. **「匿名 demo 怎么防刷？」** 登录入口按 IP 做进程内限流，业务动作按 demo 用户的更严格额度走 Redis；每次 demo 都是新用户，所以不能靠「同一用户额度」自然限制。

---

## 12. 可观测性：trace_id、request_id 与指标

**这块解决什么问题**：异步 run 出错时，能从一个请求追到 session、run、turn、step、模型调用和工具调用；能区分「这一次 HTTP 请求」和「这一整条业务链路」；能看模型失败分类、token、降级、锁冲突、worker 租约和限流。

**关键实现**：
- 入口 ID：[internal/api/http/middleware.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/api/http/middleware.go) 的 `withRequestID`：`request_id` 取 `X-Request-ID` 或生成；`trace_id` 取 `X-Trace-Id`，缺失时**等于本次 `request_id`**；响应头同时回写两个 ID。
- 上下文：[internal/platform/reqctx/reqctx.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/platform/reqctx/reqctx.go) 注入 `trace_id / request_id / session_id / run_id / turn_id`，worker 路径再补 `worker_id / lease_id`，`Attrs` 转 slog 字段。
- 跨进程恢复：[migrations/0012_trace.sql](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/migrations/0012_trace.sql) 给 `runs` 加 `trace_id`；命令入队时写入，worker `runClaimed` 从 `run.TraceID` 恢复，重试沿用同一值。
- 可选 OTLP：[internal/platform/trace/trace.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/platform/trace/trace.go) 的 `Tracer.Start`：只有配置 `OTLP_ENDPOINT` 才启用，直接发 OTLP/HTTP JSON，无 OpenTelemetry SDK、无 collector；没配置时零网络、零依赖。
- 指标：[internal/platform/obs/metrics.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/platform/obs/metrics.go) 统计模型调用与失败分类、token、评估/报告降级、锁争抢、SSE 连接与投递、run 失败、worker 认领 / 续租失败、限流拒绝 / 降级、死信。
- 用量：[internal/platform/obs/usage.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/platform/obs/usage.go) 按 session 聚合 purpose/prompt/completion/total；报告里写 token 与调用次数快照（不计算金额）。
- 评测面板：[internal/api/http/evalpage.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/api/http/evalpage.go) 读取 `docs/eval-results/*.json`；A/B 逻辑在 [internal/agent/eval/compare.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/agent/eval/compare.go)。

**当时怎么选的**：日志是主要追踪面，`grep trace_id=<id>` 能覆盖「HTTP 命令 → 入队 → run 节点 → 模型/工具 → 事件写入」；`grep request_id=<id>` 只覆盖其中一次 HTTP 调用。先不引 OTel SDK / collector：按 trace_id 查日志已经能还原链路，额外组件换不来当前规模下的新信息；需要标准出口时配 `OTLP_ENDPOINT` 即可，不配置时保持零依赖。代价是指标仍在进程内，重启归零；日志只到 stdout；没有集中存储、采样、告警与跨实例聚合。

**被追问时怎么答**：
1. **「trace_id 和 request_id 有什么区别？」** `trace_id` 是端到端根，跨多次 HTTP、后台 run、模型/工具调用；`request_id` 只标识单次 HTTP 请求。客户端不带 trace 头时两者相同，显式透传 `X-Trace-Id` 时是父子关系。
2. **「worker 重启后 trace 不会断吗？」** 不会。`trace_id` 随 `runs.trace_id` 落库，claim 和重试都从库里恢复；日志里继续带同一个值。
3. **「为什么不用 OpenTelemetry SDK？」** SDK + collector 增加运行依赖、部署组件和版本升级面；当前用结构化日志 + 可选 OTLP/HTTP JSON 已经够定位问题。导出失败只记 Debug，不影响业务。
4. **「线上一次 run 失败，你怎么定位？」** 先看 `run_failure_total{kind,class}`，再按 `trace_id` / `run_id` 查日志，`run_steps` 看最后成功与失败 step，`session_events` 看对用户暴露到哪一步。
5. **「指标重启就没了，能接受吗？」** 当前定位是单实例/小集群，能接受；要多实例长期观测，需要把 `Metrics` 换成 Prometheus 客户端或加远端写入。这是未做的部分。

---

## 13. 评测体系：60 条回归集与五门槛

**这块解决什么问题**：让「评分可靠」成为可重复执行的验收，而不是每次手工抽几条看看；同时能解释「这次改动到底让模型变好还是变差」。

**关键实现**：
- 数据集：[internal/agent/eval/testdata/regression.json](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/agent/eval/testdata/regression.json)，60 条（Go 后端 35 / AI Agent 25）。类型覆盖 high 15、mid 18、low 7、empty 3、exaggerated 3、off_topic 3、jargon 3、unverified 3、conclusion_only 3、verbose_offtopic 2。每条带 `expected_band`、五维人工分、`notes` 和必须能在答案原文定位的 `evidence_anchor`；加载时校验锚点、档位与分数自洽。
- 加载与校验：[internal/agent/eval/dataset.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/agent/eval/dataset.go)。
- 跑分：[internal/agent/eval/runner.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/agent/eval/runner.go) 的 `Run`：逐条评估、统计指标、检查门槛、输出 JSON；`cmd/evalrunner` 是命令行入口。
- 四条比率/误差门槛 + 一条硬不变量：[internal/agent/eval/runner.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/agent/eval/runner.go) 的 `DefaultThresholds`：
  - `schema_valid_rate = 100%`（硬门槛）；
  - `band_hit_rate ≥ 80%`；
  - `adjacent_band_rate ≥ 95%`；
  - `dimension_mae ≤ 0.6`；
  - `evidence_quote_rate = 100%`（硬不变量，跑分再复核）。
- A/B：[internal/agent/eval/compare.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/agent/eval/compare.go) 的 `Compare` 对比两份结果文件的指标差值与档位翻转；`cmd/evaldiff` 与 `/eval?base=&candidate=` 只读历史文件，不调模型。
- 真实基线：[docs/eval-results/eval-openai-deepseek-chat-eval-v4-20261107T143259Z.json](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/docs/eval-results/eval-openai-deepseek-chat-eval-v4-20261107T143259Z.json)（v4，60 条，2026-11-07）。schema 100%、档位 80.0%（48/60）、相邻档 100%、MAE 0.4733、证据 100%，五个门槛全部通过；分领域 Go 82.9%（29/35）、AI Agent 76.0%（19/25）。

**v3 → v4 的净效应是变差**：与同集 v3 的 A/B 中，档位命中率 **81.7% → 80.0%（−1 条）**，维度 MAE **0.4967 → 0.4733（改善）**，其余三项持平；实际档位翻转 2 条且**均为回退**（`ai-mid-03-memory`、`ai-mid-07-context-budget`，3→5）。标注修正使 `ai-high-01-rag-pipeline` 由期望 5 档改为 3 档并转为命中，但模型实际总分（74）未变，**不计为模型改进**。因此 v4 不能记为提升：命中率净效应变差 1 条，MAE 改善主要来自标注下调。真实基线贴着 80% 门槛，48/60 再少 2 条就会失败；5 档边界对单维度 ±1 分很敏感（`ai-high-01` 79 分距 80 只差 1 分）。

**当时怎么选的**：四个比率/误差门槛分别对应「结构化可用」「档位结论可信」「允许 ±1 档抖动」「五维误差可接受」；证据引用是硬不变量，单独列出，因为它不是统计指标，解析或引用失败等于不可用。数据集固定 60 条、新增样本替换而非无限追加，控制跑分成本。代价是 60 条仍小，单次跑分不足以证明稳定性；没有同一 case 多次取档位众数；基线与门槛距离很近，改动容易在门槛附近翻转。

**被追问时怎么答**：
1. **「v4 比 v3 好吗？」** 不能简单说好。档位命中率 81.7%→80.0%，净效应变差 1 条；MAE 0.4967→0.4733 改善，但主要来自 4 条标注下调，不是模型打分变准。两条实际翻转都是回退。
2. **「80% 门槛是不是太松？」** 三档是产品直接展示的结论，允许少量边界抖动；但真实基线 80.0% 正好贴线，说明余量很小，这是当前的主要风险。
3. **「为什么 5 档边界这么敏感？」** 三档切点是 50 / 80；五维 1–5 分加权后，任何一个维度 ±1 分都可能把总分推过 80，`ai-high-01` 79 分就是 1 分之差。
4. **「没有 key 怎么跑评测？」** 可以跑 heuristic，但它只验证 schema 与证据等确定性约束，档位门槛会失败（`eval-v2` 就是 73.3% / 93.3% / MAE 0.66）。真实基线必须用真实模型跑，才谈得上语义评分有效。
5. **「A/B 会不会偷偷调模型？」** 不会。`evaldiff` 与 `/eval` 只读已有结果文件，0 次模型调用、0 费用。

---

## 14. 10 分钟现场演示脚本

前提：Postgres 与 Redis 已起，`.env` 或环境变量配好；`go build -o bin/server ./cmd/server` 后启动 `bin/server`（默认 `:8080`，也可显式 `HTTP_ADDR=:18080`），浏览器开 `http://127.0.0.1:18080/demo`。若要零依赖演示，直接开 `/demo?sample=1`，走内置样例、不调接口。

| 时间 | 打开哪一屏 | 点什么 | 说什么 | 预期看到什么 |
| --- | --- | --- | --- | --- |
| 0:00-0:40 | `/demo` 首屏 | 不点 | 「左边是输入，右边是主区；诊断和匹配的结果整宽展示在主区」 | 左侧简历/JD 表单，右侧空状态与状态徽标 |
| 0:40-2:00 | 简历诊断 | 粘贴简历 →「诊断」 | 「这一步不调用模型，规则版」 | 总分 69（3 档）、初筛/技术/文档三个分、A-E 维度、问题与「差→好」折叠 |
| 2:00-3:10 | JD 匹配 | 粘贴 JD →「分析 JD」 | 「匹配度复用简历的初筛口径，技能按别名表可解释地合并」 | 匹配度、岗位信息、必备/加分技能 chips、5 题题纲、缺口清单、材料包按钮 |
| 3:10-4:10 | 面试配置 | 选题纲/难度 →「开始面试」 | 「一场面试是一个 run，事件走 SSE」 | 状态变 active、进度条出现、第一题与待答框 |
| 4:10-6:00 | 作答 | 答第 1 题 → 提交 | 「模型只给维度建议，总分是公式算的；追不追问由规则决定」 | 评分卡、维度分、证据引文、bonus/penalty、下一步动作 |
| 6:00-7:20 | 继续作答 | 第 2、3 题，尽量触发一次追问 | 「追问有单题/总预算/深度三道闸」 | 追问标记、进度条深度变化、追问次数 |
| 7:20-8:40 | 报告 | 「收尾并生成报告」 | 「先回查数据，再让模型写点评」 | 五个 section、维度图、逐题点评、工具调用数、是否 degraded |
| 8:40-10:00 | 复习 | 打开复习面板 → 选一张卡 → 自评 | 「SM-2；只有真实模型客观分才驱动排程，heuristic 只作参考」 | 到期卡片、优先级、EF/间隔/下次日期、`scoring_mode` 与计算说明 |

**如果网络或模型挂了怎么演示**：
- **没 key / 模型超时**：改用 `MODEL_PROVIDER=heuristic` 启动，说明「分数仍是公式算的，出题和点评走本地模板」；报告仍能出，只是 `degraded=true`。
- **不能访问外网**：JD 不要用 URL，直接粘贴文本；粘贴优先，服务不会发起抓取。
- **Redis 挂了**：锁回退进程内锁、限流 fail-open，演示照常；明确「此时只有单实例锁安全，限流暂时失去保护」。
- **Postgres 挂了**：服务启动失败，没有降级存储；改用 `/demo?sample=1` 展示界面，或先把库起回来。
- **SSE 断了**：刷新页面重新拉会话；事件接口支持 `Last-Event-ID` 重放，已发生的题不会丢。

---

## 15. 已知不足

按影响从高到低。每条标明是「没做」还是「做浅了」；已经修好的不再列。

1. **真实模型基线贴着 80% 门槛（风险）**：`eval-v4` 档位命中 80.0%（48/60），再少 2 条就不达标；v3→v4 净效应还是变差 1 条。当前没有门槛余量告警，也没有「低于 82% 就阻止合并」的策略。
2. **5 档边界对单维度 ±1 分敏感（做浅了）**：三档切点 50 / 80，五维加权后任一维度 ±1 分都可能跨档；`ai-high-01` 79 分就是 1 分之差。没有边界样本的专项稳定性统计。
3. **评测只跑单次，没有多次取众数（没做）**：结果是单次 60 条，不能区分「模型稳定」与「这次恰好」。计划中的同 case 连续跑 3 次取档位众数尚未落地；60 条样本也偏小。
4. **鉴权是最小实现（做浅了）**：邮箱密码 + bcrypt + HS256 JWT，没有 RBAC、刷新令牌、找回密码、第三方登录与即时吊销；匿名 demo 的业务动作限流已走 Redis，但**登录次数仍是单进程 IP 计数**，多副本需统一到 Redis。
5. **复习的客观性有边界（做浅了）**：只有 `objective`（真实模型）由客观分驱动 SM-2；`length_heuristic` 只作参考、排程按自评，且不覆盖历史客观字段；`self_only` 仅按自评。每日上限只裁剪展示队列，不改 `due_at`；时区靠请求参数，默认 +8；没有跨用户分歧报表。
6. **worker 队列是「够用」而非「完整」（做浅了）**：空队列靠轮询（跨实例唤醒未接 `LISTEN/NOTIFY`），没有优先级、延迟队列、指数退避；恢复粒度是 step，不是精确断点；同一会话同时只允许一个活跃 run。
7. **限流有边界（做浅了）**：固定窗口在边界处可能有双倍突发；Redis 故障时 fail-open，期间没有保护；只覆盖建会话 / 提交作答 / 生成报告三类动作；demo 登录仍是进程内计数。
8. **死信只有人工重试（做浅了）**：达到 `RUN_MAX_FAILURES` 进 `dead` 后需要人显式调 retry，没有自动重试、退避或死信看板；重试单位是整个 run，不是单 step。
9. **可观测性只在进程内（做浅了）**：`trace_id` 已贯穿并支持可选 OTLP/HTTP JSON，但指标仍进程内、重启归零；日志只到 stdout；没有 collector、集中存储、采样、告警与跨实例聚合。
10. **简历 / JD 仍是规则语义（做浅了）**：正则、关键词与别名表可复现，但隐性技能、复杂排版仍会漏判；同义写法能收敛，语义相近但表述完全不同的能力仍靠人工确认。**没有向量检索是刻意取舍**，不是遗漏。
11. **图片简历与扫描版 PDF 没有 OCR（没做）**：附件只支持 txt / md / docx / pdf；PNG / JPG 返回 `image_requires_ocr`，扫描版 PDF 提不出文字。
12. **报告证据包有截断、只有一轮点评（做浅了）**：回答摘要、弱项条数、搜索结果都有上限；没有跨场趋势、多轮修订与逐段 critic；模型不可用或输出不合法即降级。
13. **JD URL 抓取只适配静态 HTML（做浅了）**：不执行 JS、不做站点适配、不碰登录墙和验证码；抓不到就要求粘贴。
14. **模型输出容错有限（做浅了）**：JSON 抽取 + 一次修复提示，没有 schema 约束解码；复杂输出仍可能降级。
15. **没有 token 级流式输出（没做）**：SSE 推的是面试事件，不是模型逐字输出；等待评估时界面是「处理中」而不是流式文字。
16. **迁移只有向上脚本（没做回滚）**：`migrations/` 没有 down 迁移，回滚要靠备份或手工 SQL。

另有明确边界：JD 抓取不代投；语音 / ASR、支付、RBAC / 找回密码 / OAuth、多端小程序不在范围内。
