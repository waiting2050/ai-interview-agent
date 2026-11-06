# 项目拆解读物

这份材料按「一次真实请求怎么走完」的顺序拆代码，面向想照着仓库自学的读者。行号对应写作时的 `main`（`f7d4626`），后续改动可能让行号漂移，以函数名为准。

先记住三条主线：

- **分层**：`cmd/server` 只做装配；`internal/api/http` 只做协议转换；`internal/app` 编排用例；`internal/domain` 放规则与状态；`internal/store` 落库；`internal/ports` 定义边界。
- **两种能力**：简历诊断、JD 匹配、出题回退、报告回退、复习排期都是确定性规则；模型只负责评估文字、出题文字和报告点评。
- **一次写操作 = 一个 run**：run 有幂等键，内部步骤落 `run_steps`，失败可重试，进程崩溃可对账恢复。

---

## 1. 入口与分层（`cmd` / `internal` / `ports`）

**它解决什么问题**：让 HTTP、业务规则、存储三层的依赖方向单一——handler 不直接碰数据库，存储实现可以替换（Postgres / 内存 / Redis 锁），测试不用起真库。

**关键代码在哪**：
- 装配点：[cmd/server/main.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/cmd/server/main.go#L31-L170)：`run()` 读配置（39）、开 Postgres（51）、按环境变量建模型客户端（64-99）、建指标与用量（85-86）、Redis 锁失败时回退进程锁（105-119）、组装 `app.Service`（122-132）、启动时对账（134-138）、挂路由与 HTTP Server（144-167）。
- 端口定义：[internal/ports/repository.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/ports/repository.go#L25-L146) 的 `Repositories` / `Store`，[internal/ports/lock.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/ports/lock.go#L26-L36) 的 `Locker`，[internal/ports/resume.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/ports/resume.go#L8-L16) 的 `ResumeAnalyzer`。
- 路由表：[internal/api/http/router.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/api/http/router.go#L31-L66)，从 `/healthz`、`/metrics`、`/demo` 到简历、JD、复习、会话七组接口。
- 存储实现：[internal/store/postgres](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/store/postgres/store.go#L35-L106)、[internal/store/memory](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/store/memory)、[internal/store/redis/lock.go](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/store/redis/lock.go#L45-L160)。

**为什么这么设计（含代价）**：`ports` 接口把「业务需要什么」和「Postgres 怎么实现」分开，装配只在 `main.go` 发生，测试可以注入内存实现。代价是多一层接口和两套存储实现，行为必须靠契约测试对齐；`postgres.Store.WithTx`（[store.go:65-79](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/store/postgres/store.go#L65-L79)）把同一个事务内的仓库重新绑定到 `tx`，这层包装写错时很难从 handler 看出来。

**面试官可能追问**：
1. **「为什么不直接在 handler 里用 pgx？」** 因为 handler 只该关心 HTTP 语义；直接依赖 pgx 会让参数校验、异步 run、事件流和存储细节缠在一起，也无法用内存实现跑快速测试。现在的边界是 `ports.Repositories`，handler 只调 `app.Service`。
2. **「换存储要改哪些文件？」** 只新增一个实现 `ports.Store` 的包，再在 `main.go` 换装配；`app` 与 `api` 不动。前提是契约测试覆盖得到，否则乐观锁、唯一约束这些语义容易在换实现时丢。
3. **「为什么 `main.go` 里 Redis 挂了还能起？」** 105-119 行做了显式回退：Redis `Ping` 失败就换进程内锁并打警告。代价是这时只有单实例安全，多副本部署会失去跨进程互斥。

---

## 2. 会话状态机与步骤日志

**它解决什么问题**：面试是一个有严格先后顺序的长事务——不能重复答同一题、不能在未收尾时出报告、进程挂了要能接着跑。状态机负责「什么命令在什么状态下合法」，步骤日志负责「跑到哪了、能不能重放」。

**关键代码在哪**：
- 配置默认值与硬限制：[internal/domain/session/state.go:46-133](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/domain/session/state.go#L46-L133)，题量 1-12、单题追问 0-3、深度 1-4、总追问 0-10、时长 1-60 分钟。
- 状态与转移：[state.go:135-198](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/domain/session/state.go#L135-L198) 定义 `Session` 与 `Start`；[state.go:200-281](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/domain/session/state.go#L200-L281) 是 `Finish`、`AskFollowup`、`AskNextQuestion`、`BeginReport`、`Complete`、`Fail`、`Abort`。非法转移统一返回 `TransitionError`（158-181），`Abort` 幂等（269-281）。
- run / step 模型：[internal/domain/session/run.go:5-62](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/domain/session/run.go#L5-L62)。
- 执行与步骤缓存：[internal/app/runner.go:57-87](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/app/runner.go#L57-L87) 按 `start/answer/report` 分派；[runner.go:273-334](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/app/runner.go#L273-L334) 的 `recordStep` 先查已完成步骤、命中就反序列化复用，否则 `Steps.Start` → 执行 → `Complete`。
- 恢复：[internal/app/service.go:531-563](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/app/service.go#L531-L563) 的 `Reconcile` 扫描超时 running run；[cmd/server/main.go:134-138](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/cmd/server/main.go#L134-L138) 启动时调用。
- 持久化：[internal/store/postgres/repository.go:56-74](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/store/postgres/repository.go#L56-L74) 的 `UPDATE ... WHERE state_version`；[repository.go:335-412](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/store/postgres/repository.go#L335-L412) 的步骤读写。

**为什么这么设计（含代价）**：状态机把「面试规则」变成可单测的纯函数，不依赖数据库；`state_version` 乐观锁让并发写至少有一方失败，而不是静默覆盖；步骤日志让「评估已完成、提交没完成」这种半成品可以被重放。代价是每次写操作要同时维护 session、turn、evaluation、run、step 五张表，事务边界必须很准；`recordStep` 缓存的是 JSON，结构变更后的旧步骤可能反序列化失败。

**面试官可能追问**：
1. **「两个请求同时答同一题会怎样？」** 先被 session 锁串行化（Redis 或进程锁），后到者在 `Answer` 里看到 `PendingTurnID` 已变或 turn 已回答，返回 `ErrStateConflict`；即使锁失效，`UpdateState` 的版本号比较和 turn 状态检查仍会拦住。
2. **「为什么 `Abort` 可以重复调用？」** 它要对「用户连点两次取消」幂等：已 aborted 直接成功返回（state.go:269-281），completed/failed 则拒绝，避免把已完成的会话改成 aborted。
3. **「步骤日志和事件流有什么区别？」** `run_steps` 是执行侧的内部断点，用来重放/恢复；`session_events` 是对外的只追加事实流，供 SSE 渲染和审计。两者写在同一事务里，但用途不同。

---

## 3. 一次作答的完整链路（回答 → 评估 → 规则裁决 → 出题）

**它解决什么问题**：把「收到一段回答」拆成可替换、可降级、可复现的四步：取快照、调模型评估、用规则决定追问/下一题/收尾、生成下一题并原子提交。

**关键代码在哪**：
- 接单：[internal/app/service.go:337-450](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/app/service.go#L337-L450)：校验 `turn_id`、截断超长回答、算幂等键、检查 session 为 `active/awaiting_answer` 且 turn 是 pending、创建 answer run。
- 图编排：[internal/agent/interview/graph.go:27-32](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/agent/interview/graph.go#L27-L32) 的五个节点，[graph.go:147-179](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/agent/interview/graph.go#L147-L179) 的边与 `decide` 分支。
- 节点实现：[internal/app/answer.go:154-228](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/app/answer.go#L154-L228) 把评估器、裁决、出题函数注入 `AnswerDeps`。
- 评估：[internal/agent/interview/evaluate.go:72-154](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/agent/interview/evaluate.go#L72-L154)：拼 prompt、调模型、解析 JSON、按 rubric 校验，最多 2 次修复；模型不可用或输出非法时降级为 `model_output_invalid` 继续面试。
- 规则裁决：[internal/domain/followup/decide.go:59-91](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/domain/followup/decide.go#L59-L91)：时间到 → 题纲用完 → 总追问预算 → 深度上限 → 单题追问上限 → 明确不会 → 评估降级 → 要点已覆盖 → 最后才看模型建议。上限定义在 [policy.go:14-30](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/domain/followup/policy.go#L14-L30)。
- 出题：[internal/agent/interview/question.go:76-159](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/agent/interview/question.go#L76-L159)；模型失败走 [question.go:214-...](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/agent/interview/question.go#L214) 的 `FallbackQuestion`。
- 评分公式：[internal/domain/evaluation/rubric.go:206-232](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/domain/evaluation/rubric.go#L206-L232) 的 `ComputeScoreWith`，维度分/5 × 权重 × 100，再叠加 bonus/penalty 并做上限。
- 原子提交：[internal/app/answer_commit.go:92-225](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/app/answer_commit.go#L92-L225)：更新 turn、upsert evaluation、按裁决改 session、创建下一 turn、追加事件、`MarkFinished` 全在一个事务。

**为什么这么设计（含代价）**：把评估与裁决拆开，模型只产出分数建议和文字，是否追问由确定性规则决定，避免「模型想追问就无限追问」。覆盖判定（`coveredAllPoints`，answer_commit.go:51-84）还要求证据引文真的出现在回答里，防止模型编造「已覆盖」。代价是规则会压掉一些合理的追问，且评估修复次数会放大 token 消耗；模型降级时面试还能继续，但那道题没有可信分数。

**面试官可能追问**：
1. **「模型建议追问，但候选人已经答全了，听谁的？」** 听规则。`decide.go:81-86` 把覆盖完成放在模型建议之前，直接进下一题。
2. **「模型输出不是 JSON 怎么办？」** 先抽第一个 `{` 到最后一个 `}` 再解析；失败把错误反馈给模型重试，最多 2 次；仍失败就写一条 `degraded` 评估记录，本题不计分、不追问，面试继续。
3. **「为什么评估和提交要分两个事务？」** 评估是外部慢调用，不能占着数据库事务；提交阶段只做短事务。中间崩溃时，`run_steps` 里已有评估结果，重试可跳过模型调用直接提交。

---

## 4. 报告（工具回查）

**它解决什么问题**：面试结束后，把散落在 session、turn、evaluation、plan 里的数据收成一个可解释的报告；模型只写点评，不改分数、不编事实。

**关键代码在哪**：
- 证据收集：[internal/agent/tools/tools.go:202-226](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/agent/tools/tools.go#L202-L226) 的 `Collect`；`SessionOverview`（267-394）读会话、题纲、turn、评估并聚合分数；`ListWeakTurns`（395-447）、`SearchTurns`（449-517）、`Turn`（518-565）是按条件回查。每次回查都在 `beginCall/endCall`（228-257）记录耗时、结果长度、是否截断。
- 报告生成：[internal/app/report.go:226-287](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/app/report.go#L226-L287) 先确认会话可收尾、报告未重复，再 `Collect` → `Generate`。
- 模型点评与降级：[internal/agent/report/build.go:49-133](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/agent/report/build.go#L49-L133)：先构造确定性 `Fallback` 报告，再把模型输出合并进去；0 题、模型不可用、输出校验失败都保留 fallback 并标 `degraded`。字段校验在 146-181，合并策略在 182-234。
- 落库：[internal/app/report.go:289-360](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/app/report.go#L289-L360)：补上本场用量快照、渲染 markdown、在事务里完成会话状态、写报告、生成复习卡。
- 报告结构：[internal/domain/report/report.go:72-99](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/domain/report/report.go#L72-L99) 的 `Record`；五个 section 与 markdown 在 116-199。

**为什么这么设计（含代价）**：报告的可信度来自「分数是算出来的，事实是查出来的」；模型只拿到压缩后的证据包，避免它自由发挥。代价是证据包有固定截断上限（回答摘要、弱项条数、搜索结果都会截），长回答的细节会丢；模型不可用或输出不合法时报告仍能出，但点评是模板化的。

**面试官可能追问**：
1. **「所谓工具回查，是让模型自己调工具吗？」** 不是。回查在 `Collect` 里由代码先做完，模型看到的是已经查好的 bundle；`ToolCalls` / `ToolBreakdown` 是这次回查的审计记录。
2. **「报告里的总分是谁算的？」** 确定性公式。`ComputeScoreWith` 按 rubric 权重算基础分，bonus/penalty 有上下限；模型只写 verdict、维度点评和逐题建议，且这些文字要过长度与 `turn_id` 校验。
3. **「模型挂了报告还能出吗？」** 能。`build.go:84-96` 捕获非取消类错误后直接返回 fallback，`degraded=true` 且带 `model_unavailable`；只有 context 被取消才返回错误，留给重试。

---

## 5. 复习（SM-2）

**它解决什么问题**：把报告里的弱项变成可执行的复习计划，用固定间隔算法排期，避免「看完报告就忘」。

**关键代码在哪**：
- 卡片来源：[internal/domain/review/review.go:77-125](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/domain/review/review.go#L77-L125) 的 `BuildCards`：题目有缺失点、分数低于阈值、或最弱维度 ≤2 分时建卡；卡片带 topic、原题、答案摘要、关键点、最弱维度。
- SM-2 更新：[review.go:126-200](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/domain/review/review.go#L126-L200) 的 `GradeCard`：EF 按 `0.1 - (5-grade)×(0.08+(5-grade)×0.02)` 调整，下限 `MinEase`；grade<3 重置重复次数、间隔 1 天；grade≥3 第 1 次 1 天、第 2 次 6 天、之后上次间隔 × EF。
- 到期查询：[internal/domain/review/service.go:55-79](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/domain/review/service.go#L55-L79)：按调用方传入的时区偏移算「今天结束」，默认 +480 分钟；`EndOfToday` 在 134-140。
- 自评与重测：[service.go:106-133](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/domain/review/service.go#L106-L133)；接口在 [internal/api/http/review.go:13-104](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/api/http/review.go#L13-L104)，路由在 [router.go:51-55](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/api/http/router.go#L51-L55)。
- 持久化：[internal/store/postgres/repository_m11.go:22-104](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/store/postgres/repository_m11.go#L22-L104)，迁移 `migrations/0007_m11.sql`。

**为什么这么设计（含代价）**：SM-2 是纯函数，容易测、结果可解释，且不需要模型；grade 由用户自评，系统不假装知道「你真的记住了」。代价是只有自评、没有客观回忆测试；没有每日上限、优先级和跨场次统计；时区是请求参数而不是用户档案，默认值偏向 +8。

**面试官可能追问**：
1. **「为什么 grade 小于 3 要重置？」** SM-2 的 lapse 处理：回忆失败说明之前的长间隔失效，重复次数归零、明天再来，避免把不会的卡片越推越远。
2. **「间隔为什么不是固定的 1/3/7/15？」** SM-2 用难度因子 EF 动态放大间隔；grade 越高 EF 越大，后续间隔增长越快，比固定阶梯更贴近个人记忆曲线。
3. **「这些卡片什么时候生成？」** 报告提交的同一个事务里（report.go:356），从报告的逐题 `MissingPoints` / 分数派生，模型失败不影响建卡。

---

## 6. 简历诊断

**它解决什么问题**：在不上传第三方、不调用模型的前提下，给简历一个可解释的分数、分维度扣分、逐条问题与「差 → 好」改写，并区分「机器能判」和「需要人工确认」。

**关键代码在哪**：
- 主流程：[internal/domain/resume/diagnosis.go:174-319](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/domain/resume/diagnosis.go#L174-L319)：清洗文本 → 分段 → 遍历规则 → 命中检测器 → 生成 issue/manual/redline → 算文档分、双分、band。
- 红线与封顶：262-318。命中红线会清零 A 维度、把文档分封顶 69，综合分也不得高于 69。
- 维度与分段分：[diagnosis.go:322-419](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/domain/resume/diagnosis.go#L322-L419)；band 在 428-442；问题排序 443-471。
- 双分口径：`screenScore`（610-672）看岗位关键词、量化、结构等；`techScore`（673-730）看技术栈、边界、部署证据；冲突建议 741-...。
- 检测器：[internal/domain/resume/checks.go:36-74](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/domain/resume/checks.go#L36-L74) 的注册表与 `hasDetector`，具体规则从 201 行起（R01…）。
- 规则加载：[internal/domain/resume/ruleset.go:83-129](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/domain/resume/ruleset.go#L83-L129) 的 `Check`（`auto` / `auto_manual` / `manual`、扣分、红线），加载与校验 142-248，类型画像 275-340。
- 分段与文档解析：[internal/domain/resume/segment.go:143-256](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/domain/resume/segment.go#L143-L256)；[document.go:75-243](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/domain/resume/document.go#L75-L243)。
- 接口：[internal/api/http/resume.go:30-95](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/api/http/resume.go#L30-L95)（上传解析）、[resume_analyze.go:28-...](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/api/http/resume_analyze.go#L28)。

**为什么这么设计（含代价）**：规则写在 YAML，检测器写在 Go，规则 ID、扣分、类型适用范围都能单独改；`auto_manual` 把「机器只能判一半」的规则显式标出来，避免假装全自动。代价是正则对表达变体很敏感，规则覆盖不到新写法；分段/类型识别依赖日期和标题，格式很花的简历会落到 `unknown` 并要求确认；没有 OCR，图片简历直接拒绝，扫描版 PDF 提取不到文字。

**面试官可能追问**：
1. **「为什么不用模型直接打分？」** 分数要可复现、可解释、可审计；同一份简历同一规则版本必须同分，模型做不到。模型只用于面试和报告的措辞，简历诊断的 `ModelCalled` 恒为 false。
2. **「红线命中后分数怎么算？」** A 维度直接归零、文档分封顶 69，综合分也不能超过 69（diagnosis.go:262-318）。这是「不能过筛」的硬语义，不是普通扣分。
3. **「同内容换个类型分数为什么不同？」** 规则有 `AppliesTo` 和类型画像，实习/项目/工作经历的扣分与权重不同；类型由标题、日期、关键词推断，推不出时列进 `needs_confirmation`，不硬猜。

---

## 7. JD 匹配

**它解决什么问题**：把一页岗位描述变成结构化岗位、匹配度、技能缺口、题纲和申请材料包；支持粘贴或公开 URL，且不做自动投递。

**关键代码在哪**：
- 解析：[internal/domain/jd/jd.go:103-152](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/domain/jd/jd.go#L103-L152)，公司/岗位/年限/技能/职责按行与标题切。
- 匹配：[internal/domain/jd/match.go:56-118](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/domain/jd/match.go#L56-L118)：有简历文本时复用简历诊断的 `screen_score` 口径，不是另造一套分数；缺口在 132-160，题纲在 161-180，优先复用领域包。
- 服务与缓存：[internal/domain/jd/service.go:71-131](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/domain/jd/service.go#L71-L131)：粘贴文本优先，只有没文本才抓 URL；列表落库，`Get` 按 id 取缓存。
- 抓取策略：[internal/platform/jobfetch/jobfetch.go:1-233](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/platform/jobfetch/jobfetch.go#L1-L233)：只允许公开 http(s)、遵守 robots（读不到就拒绝）、每 host 每 10 秒最多 1 次（约 6 次/分钟）、10 秒超时、1 MiB 上限、最多 3 次尝试；私网地址在生产被拒绝，不绕验证码、不模拟登录。
- 材料包：[internal/domain/jd/kit.go:48-126](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/domain/jd/kit.go#L48-L126) 与 markdown 渲染 226-...；接口 [internal/api/http/jd.go:13-70](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/api/http/jd.go#L13-L70)。

**为什么这么设计（含代价）**：匹配度复用简历诊断口径，避免「简历 77 分、JD 匹配又一套算法」互相矛盾；技能卡分必备/加分、红绿标注缺口，题纲复用领域包而不是让模型自由出题。材料包只做摘要、开场、题纲、预演问题，按钮明确「不代投」。代价是关键词/行规则对非标准 JD 会漏技能；抓取只适合静态 HTML，JS 渲染或登录墙页面抓不到，需要用户粘贴。

**面试官可能追问**：
1. **「匹配度 77 是怎么来的？」** 不是模型打分，是取简历诊断的 `screen_score`（岗位关键词、量化、结构等组件），再列已命中/缺失关键词；同一简历同一 JD 可复现。
2. **「抓 URL 会不会违规？」** 抓取器只访问公开页面，先读并遵守 robots.txt（读不到就拒绝），按 host 限速、限大小和超时，不支持登录态和验证码；生产默认禁止私网地址。
3. **「材料包会不会编造经历？」** 摘要、开场、预演问题都从已解析的简历画像和 JD 事实拼出，`KitFact` 记录来源；没有自动投递接口，也不生成不存在的项目。

---

## 8. 存储与并发（锁 + PG 约束）

**它解决什么问题**：多实例、重复点击、进程崩溃时，保证同一场会话不被并发写坏；同时让「读—改—写」有明确的冲突信号，而不是最后写入者获胜。

**关键代码在哪**：
- 会话级互斥：[internal/store/redis/lock.go:84-160](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/store/redis/lock.go#L84-L160)：`SetNX` 抢 key + token，同 token 刷新 TTL 实现重入，不同 token 有限等待，释放用 Lua 比对 token 删除；进程内回退 [internal/platform/lock/process.go:14-35](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/platform/lock/process.go#L14-L35)。
- 事务：[internal/store/postgres/store.go:65-79](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/store/postgres/store.go#L65-L79) 的 `WithTx`。
- 乐观锁：[repository.go:56-74](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/store/postgres/repository.go#L56-L74)，版本不匹配返回 `ports.ErrStateConflict`。
- 幂等 run：[repository.go:166-189](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/store/postgres/repository.go#L166-L189) 的 `ON CONFLICT (session_id, kind, idempotency_key) DO NOTHING`，约束来自 `migrations/0001_init.sql`。
- 唯一报告 / 复习卡：[repository_m4.go:20-45](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/store/postgres/repository_m4.go#L20-L45)、[repository_m11.go:22-46](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/store/postgres/repository_m11.go#L22-L46)。
- 事件：[repository.go:105-129](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/store/postgres/repository.go#L105-L129) 插入后同事务 `pg_notify`；跨实例通知在 [notifier.go:114-159](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/store/postgres/notifier.go#L114-L159)。
- 迁移：`migrations/0001_init.sql` 到 `0007_m11.sql`（会话/事件/run、turn/plan/evaluation、report、domain、jd、review）。

**为什么这么设计（含代价）**：Redis 锁把「同一会话的写」串行化，PG 约束做最后一道防线：锁 TTL 到了、实例挂了，唯一键和 `state_version` 仍能拦住重复 turn / 重复报告。事件写入同一事务并 `NOTIFY`，保证「事务提交才可见」。代价是 Redis 成为可选单点（回退进程锁后只能单实例）；锁的粒度是整场会话，长 run 会挡住同会话的其他写；没有分布式事务，跨库一致性靠「先落库、后发通知」。

**面试官可能追问**：
1. **「锁已经互斥了，为什么还要唯一约束？」** 锁可能因 TTL、网络分区或进程崩溃失效；唯一约束是数据库层的硬保证，重复请求会命中幂等键直接重放，不会写出第二个 turn/report。
2. **「`state_version` 冲突怎么处理？」** 返回 409 `state_conflict`，客户端重读会话或换幂等键重试；服务端不会覆盖别人的写，也不会自动把旧状态合并。
3. **「事件为什么要 `LISTEN/NOTIFY`，SSE 直接查库不行吗？」** 可以查，但轮询延迟高。`NOTIFY` 只负责唤醒，真正的顺序与回放仍由 `session_events` 的单调 `event_id` + `Last-Event-ID` 保证；通知丢了也只是晚一点，不会丢事件。

---

## 9. 可观测性

**它解决什么问题**：异步 run 出错时，能从一个 request 追到 session、run、turn、step 和模型调用；能看模型调用次数、token、降级和锁冲突；演示页能靠 SSE 实时出题。

**关键代码在哪**：
- 关联 ID：[internal/platform/reqctx/reqctx.go:15-87](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/platform/reqctx/reqctx.go#L15-L87) 注入 `request_id/session_id/run_id/turn_id`，`Attrs` 转 slog 字段。
- 请求日志：[internal/api/http/middleware.go:12-33](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/api/http/middleware.go#L12-L33)。
- 指标：[internal/platform/obs/metrics.go:63-210](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/platform/obs/metrics.go#L63-L210) 统计模型调用、token、评估/报告降级、锁冲突、SSE 连接数、事件投递耗时、run 失败分类；文本输出在 211-300。
- 用量：[internal/platform/obs/usage.go:38-82](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/platform/obs/usage.go#L38-L82) 按 session 聚合 purpose/prompt/completion/total。
- 模型埋点：[internal/agent/llm/instrument.go:42-63](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/agent/llm/instrument.go#L42-L63)：每次调用记 result、token，并把本次用量记到 session。
- 报告成本快照：[internal/app/report.go:295](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/app/report.go#L295)。
- SSE：[internal/api/http/handlers.go:338-425](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/api/http/handlers.go#L338-L425)，`Last-Event-ID` 解析在 435-449。
- `/metrics`：[internal/api/http/metrics.go:5-12](file:///c:/Users/waiting2050/Desktop/gotest/%E9%9D%A2%E8%AF%95/internal/api/http/metrics.go#L5-L12)。

**为什么这么设计（含代价）**：run/step 的结构化日志 + 关联 ID 让问题能定位到具体节点；指标按 provider/model/result 分桶，能看到「是哪家模型、哪种失败」；用量按 session 聚合，报告和界面能显示本场成本。代价是全部指标在进程内，重启归零，没有持久化、没有 OpenTelemetry 追踪、没有跨实例聚合；日志只到 stdout，没有集中检索。

**面试官可能追问**：
1. **「线上一次 run 失败，你怎么定位？」** 先看 `run_failure_total{kind,class}`，再按 `request_id/run_id` 查日志，`run_steps` 里看最后成功的 step 与失败 step，`session_events` 里看对用户暴露到哪一步。
2. **「为什么要按 session 记 token？」** 报告需要给出本场模型调用与 token 快照；`UsageTracker.Snapshot` 在提交报告时写入，和 `model_calls` / `prompt_tokens` 字段对齐。
3. **「指标重启就没了，能接受吗？」** 当前定位是单实例演示，能接受；要多实例长期观测，需要把 `Metrics` 换成 Prometheus 客户端或加远端写入，这是未做的部分。

---

## 10. 10 分钟现场演示脚本

前提：Postgres 与 Redis 已起，`.env` 或环境变量配好；`go build -o bin/server ./cmd/server` 后启动 `bin/server`（默认 `:8080`，也可显式 `HTTP_ADDR=:18080`），浏览器开 `http://127.0.0.1:18080/demo`。若要零依赖演示，直接开 `/demo?sample=1`，走内置样例、不调接口。

| 时间 | 打开哪一屏 | 点什么 | 说什么 | 预期看到什么 |
| --- | --- | --- | --- | --- |
| 0:00-0:40 | `/demo` 首屏 | 不点 | 「左边是输入，右边是主区；诊断和匹配的结果整宽展示在主区」 | 左侧简历/JD 表单，右侧空状态与状态徽标 |
| 0:40-2:00 | 简历诊断 | 粘贴简历 →「诊断」 | 「这一步不调用模型，规则版」 | 总分 69（3 档）、初筛/技术/文档三个分、A-E 维度、3 条问题与「差→好」折叠 |
| 2:00-3:10 | JD 匹配 | 粘贴 JD →「分析 JD」 | 「匹配度复用简历的初筛口径，技能红绿标缺口」 | 匹配度、岗位信息、必备/加分技能 chips、5 题题纲、缺口清单、材料包按钮 |
| 3:10-4:10 | 面试配置 | 选题纲/难度 →「开始面试」 | 「一场面试是一个 run，事件走 SSE」 | 状态变 active、进度条出现、第一题与待答框 |
| 4:10-6:00 | 作答 | 答第 1 题 → 提交 | 「模型只给分和点评，追不追问由规则决定」 | 评分卡、维度分、证据引文、bonus/penalty、下一步动作 |
| 6:00-7:20 | 继续作答 | 第 2、3 题，尽量触发一次追问 | 「追问有单题/总预算/深度三道闸」 | 追问标记、进度条深度变化、追问次数 |
| 7:20-8:40 | 报告 | 「收尾并生成报告」 | 「先回查数据，再让模型写点评」 | 五个 section、维度图、逐题点评、工具调用数、是否 degraded |
| 8:40-10:00 | 复习 | 打开复习面板 → 选一张卡 → 自评 | 「SM-2，grade<3 明天重来」 | 到期卡片、EF/间隔/下次日期、计算说明 |

**如果网络或模型挂了怎么演示**：
- **没 key / 模型超时**：改用 `MODEL_PROVIDER=heuristic` 启动，说明「分数仍是公式算的，出题和点评走本地模板」；报告仍能出，只是 `degraded=true`。
- **不能访问外网**：JD 不要用 URL，直接粘贴文本；粘贴优先，服务不会发起抓取。
- **Redis 挂了**：服务会回退进程内锁（main.go:105-119），演示照常，但明确「此时只有单实例安全」。
- **Postgres 挂了**：服务启动失败，没有降级存储；改用 `/demo?sample=1` 展示界面，或先把库起回来。
- **SSE 断了**：刷新页面重新拉会话；事件接口支持 `Last-Event-ID` 重放，已发生的题不会丢。

---

## 11. 已知不足

按影响从高到低。每条标明是「没做」还是「做浅了」。

1. **没有用户与鉴权（没做）**：所有接口匿名可用，会话 ID 本身就是能力凭证；复习到期查询是全局的，没有按用户隔离。单机演示够用，公网部署不可接受。
2. **简历/JD 的语义理解仍是规则（做浅了）**：正则和关键词能覆盖常见写法，但换行、同义词、隐性技能会漏判；没有 embedding/模型语义匹配。好处是可复现，坏处是边界靠人补规则。
3. **图片简历与扫描版 PDF 没有 OCR（没做）**：`document.Extract` 直接拒绝图片类型，扫描版 PDF 提不出文字；用户只能粘贴文本。
4. **报告证据包有截断、只有一轮点评（做浅了）**：回答摘要、弱项条数、搜索结果都有上限；没有跨场次趋势、没有多轮修订、没有模型自检。模型不可用或输出不合法即降级。
5. **复习只有 SM-2 + 自评（做浅了）**：没有每日上限、优先级、预测和长期统计；grade 是用户自报，系统不做客观回忆验证；时区靠请求参数，默认 +8。
6. **run 队列在进程内（没做持久队列）**：`launch` 起 goroutine，没有 worker pool、背压和跨实例消费；崩溃后靠 `Reconcile` 扫 stale run 恢复，恢复粒度是 step，不是精确断点。
7. **可观测性只在进程内（做浅了）**：指标重启归零，没有集中日志、分布式追踪、告警；多实例下各看各的。
8. **JD URL 抓取只适配静态 HTML（做浅了）**：不执行 JS、不做站点适配、不碰登录墙和验证码；抓不到就要求粘贴，这是策略也是限制。
9. **演示页是手写单文件（做浅了）**：`app.js` 体量大、没有构建链和前端单测；改 UI 靠人工回归截图。
10. **模型输出容错有限（做浅了）**：只做 JSON 抽取 + 一次修复提示，没有 schema 约束解码；复杂输出仍可能降级。
11. **没有 token 级流式输出（没做）**：SSE 推的是面试事件，不是模型逐字输出；等待评估时界面是「处理中」而不是流式文字。
12. **迁移只有向上脚本（没做回滚）**：`migrations/` 没有 down 迁移，回滚要靠备份或手工 SQL。
