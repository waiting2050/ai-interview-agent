# 参考项目与本地项目：来源与功能对比

- 参考项目：<https://github.com/wangzhongyang00/go-ai-eino-mianshiba>，`main`，HEAD `426a154`（仓库只有 1 个提交：`2026-05-13 16:19:44 +0800 开源项目 回报社区`，作者王中阳）
- 本地项目：`c:\Users\waiting2050\Desktop\gotest\面试`，`main`，HEAD `77b534b`
- 对比时间：2026-11-13
- 方法：clone 参考仓库到临时目录后做文件级阅读 + 机械比对；本地只读 `README.md`、`docs/design-rationale.md`、`docs/project-walkthrough.md`、`docs/development-plan.md` 与源码。未运行任何测试，也未启动任何服务。
- 口径：只写仓库里能看到证据的内容。看不到运行结果、外部服务状态、真实密钥的地方标「无法确认」。

---

## 0. 结论摘要

1. **参考项目是真实实现，不是空壳**：180 个 Go 文件、60,138 行 Go、43 个 TS/TSX、24 个 Next.js 页面、25 个 `_test.go`、MySQL/Redis/Milvus 接入、Stripe/PayPal、ASR、多 Agent、Docker Compose + Nginx 都在仓库里有对应代码。
2. **本地没有把参考项目的能力都实现**：参考有而本地没有的整块能力至少 14 项，集中在产品面（完整前端、预测题、校招/社招场景、用户中心、支付/订阅、OAuth/找回密码、ASR）和基础设施面（Milvus 文档导入、OSS、Redis Stream MQ、用户级模型配额、LiteLLM、CozeLoop、飞书告警、国际化）。
3. **本地在另外一些维度反超参考**：确定性追问裁决、证据可定位的五维评分、PostgreSQL 单一事实源与崩溃恢复、跨实例 worker + 租约 + 死信、60 条回归集与可复现评测结果、SM-2 间隔复习、JD 匹配、简历规则诊断。
4. **没有发现代码或大段文字的复制痕迹**：0 个连续 5 行代码块重合、0 条 Go 注释重合、0 行 Markdown 重合；18 条单行重合全部是 `truncate`、`httptest.NewServer`、`HashPassword` 这类通用样板；21 个同名类型/函数（大小写不敏感）和 19 个同名 Go 文件都是同名不同实现。
5. **但「同题同框架」是事实**：同为 AI 模拟面试、同用 `cloudwego/eino`、都有 Go 专项面试与「报告 Agent + 面试信息工具」的概念。参考项目公开发布早于本地约 6 个月。按现有证据，能支撑「同题、同框架、独立实现」，不能支撑「抄代码」；同时本地 git 历史看起来是后期重建的（52 个提交挤在 3 天内，前 5 个提交只隔 64 秒），无法用提交历史证明独立创作，也没有任何对参考项目的引用或致谢。

---

## 1. 参考项目实际实现了什么

### 1.1 技术栈与组织方式（有代码证据）

| 维度 | 实际情况 | 证据 |
| --- | --- | --- |
| 后端框架 | Go 1.25.1 + Hertz v0.10.3 + eino v0.7.28 | `backend/go.mod` L3、L9、L18-L22 |
| Agent 组织 | 两套并存：`compose.Graph` 跑面试主循环；`eino/adk` 的 `ChatModelAgent` 跑综合/专项/预测/报告/多 Agent，Agent 可被包装成工具 | `backend/internal/service/interview/engine/graph_loop.go`；`backend/internal/agents/multiagent/orchestrator.go` L16-L90 |
| 会话状态 | 进程内 `map[string]*InterviewSession`；作答通过 channel 交回图里的 `wait_answer` 节点 | `backend/internal/service/interview/engine/types.go` L10-L40；`graph_loop.go` L81-L92 |
| 数据存储 | MySQL + GORM（`internal/model/` 下 15 个模型文件）、Redis、Milvus、本地/OSS 存储、SQLite 备用 | `backend/internal/model/*.go`；`backend/internal/repository/database.go`；`backend/internal/storage/*.go` |
| 异步与稳定性 | Redis Stream 队列、生成锁、限流、`gobreaker` 熔断、token 配额、飞书告警、CozeLoop/OTel | `backend/internal/mq/*.go`；`backend/internal/alert/*.go`；`backend/internal/agents/llm/tokenquota.go` |
| 前端 | Next.js 14 + React 18 + TS + Ant Design + Zustand + TanStack Query，24 个 `page.tsx` | `frontend/src/app/**/page.tsx`；`frontend/package.json`；README L241-L250 |
| 部署 | 根目录 `docker-compose.yml`：mysql / redis / etcd / backend / frontend / nginx；另有 `docker-compose-prod.yml`、`docker-compose-example.yml`、`nginx.conf`、`litellm/docker-compose.yml` | 根目录文件；README L291-L326（Docker 启动） |
| 测试 | 25 个 `_test.go` | 文件计数 |
| 许可 | Prosperity Public License 7.0.0：允许学习/研究/非商业作品集，禁止商业盈利与付费教学 | `LICENSE` L1-L12 |

### 1.2 功能模块（逐项找入口）

| 模块 | 是否实现 | 证据与说明 |
| --- | --- | --- |
| 注册/登录/JWT | 有 | `internal/service/user/impl/user_impl.go`；JWT 依赖见 `go.mod` L22 |
| GitHub/Google OAuth | 有 | `user_impl.go` L370 起 `GitHubLogin`；前端 `app/github/callback`、`app/google/callback` |
| 微信 OAuth | 有 | `user_impl.go` L206 拼 `api.weixin.qq.com/sns/oauth2/access_token` |
| 找回密码/邮件 | 有代码 | 前端 `app/reset-password`；后端依赖 `gomail.v2`（`go.mod` L38）。真实发信无法确认 |
| 简历上传/解析 | 有 | `agents/resume/resume.go`、`agents/tools/pdfParserTool.go`、`service/resume/impl/*.go` |
| AI 预测题 | 有 | `agents/prediction/prediction_agent.go`：简历拆 5 个方向，每方向 1 题，输出 `question/content/focus/thinking_path/reference_answer/follow_up` |
| 综合面试（校招/社招） | 有 | `agents/interview/comprehensive/school_comprehensive_agent.go`、`social_comprehensive_agent.go`；前端 `interview/campus`、`interview/social` |
| 专项面试 | 有 | `agents/interview/specialized/`：Go、Java、MySQL、Redis、MQ 五个 agent |
| 多轮面试引擎 | 有 | `graph_loop.go`：start → question → wait_answer → evaluate → branch → deepen/continue/lower/switch → end；上限 10 题、30 分钟、15 秒心跳（`engine.go` L45-L49） |
| 单题评分 | 有 | `agents/evaluation/types.go` L18-L34：4 维 1-10，权重 40/25/20/15 |
| 话题覆盖/推荐下一话题 | 有 | `agents/evaluation/topic/tracker.go`、`topic_tree.go` |
| 复盘报告 | 有 | `agents/evaluation/record_evaluation_agent.go`：报告 agent + `GetInterviewInfoTool` |
| 多 Agent 协作 | 有 | `multiagent/orchestrator.go`：Host + 技术面试官 + 项目面试官，Agent-as-Tool，`MaxIterations: 30` |
| ASR 语音输入 | 有 | `service/interview/asr/`：SiliconFlow provider、`Transcribe`、guard 限流、modifier 转写修正、capability 降级 |
| RAG/向量检索 | 有 | `internal/milvus/`：embedding、indexer、splitter、retriever；`retrieval/hybrid_search.go`、`reranker.go`（Jaccard 重排）；`feishu` 导入 |
| 支付/订阅 | 有 | `internal/payment/`：Stripe/PayPal、webhook `handlers.go`、订阅事件、幂等测试 |
| 用户中心/历史 | 有 | 前端 `user/center`、`user/interviews`、`user/notes`、`user/press`、`user/pay` |
| 评测 | 部分 | `backend/scripts/evaluation/`：dataset 只有 4 条；`trulens_report.json` 只有 `dimensions` 和一句「安装 trulens 并设置 OPENAI_API_KEY 后可得到真实分数」，**没有任何分数**。因此「有脚本」可确认，「有真实评测结果」不能确认 |
| 前端可构建性 | 无法确认 | 仓库带 `frontend/tsc_output.txt`，前 8 行是 TS1005/TS1381 语法错误；可能是旧日志，也可能是当前状态。未运行 `npm build` |

### 1.3 参考项目的真实短板（避免把它当成「完整答案」）

- 评测没有可复核数据：4 条数据集 + 占位报告，不能证明评分可靠。
- 面试会话在内存里：进程重启/崩溃会丢进行中的会话；对话只在 evaluate 后快照、结束或取消时落库。
- 未发现间隔复习、JD 匹配评分、简历诊断改写，也未发现面试链路的幂等 run / 崩溃恢复 / 跨实例 worker（按目录、模型与前端页面检索）。
- 前端仓库内留有 `tsc_output.txt`、`lint.log`、`remove-i18n.js`，工程清洁度一般；当前能否构建无法确认。
- 许可证禁止商业与付费教学，二次使用边界比 MIT 严。

---

## 2. 本地项目实际实现了什么

来源：`README.md` L3、L39-L50、`docs/development-plan.md` 的里程碑与不做清单（L1254-L1272）、`docs/project-walkthrough.md` L1-L365、`docs/design-rationale.md` 以及源码文件。

- 主链路：简历诊断 → JD 匹配 → 多轮面试 → 复盘报告 → 间隔复习（`README.md` L3）。
- 面试：回合制短图 `snapshot → evaluate → decide → ask_followup/ask_next`（`internal/agent/interview/graph.go` L5-L7）；五维 rubric（维度输入 1-5 分，按权重折算到 0-100，再加/扣分），权重 30/25/20/15/10（`internal/domain/evaluation/rubric.go` L15-L45、L201-L232）；10 级门（8 道硬门 + 模型建议 + 兜底推进）+ 强弱话题补偿的确定性追问（`internal/domain/followup/decide.go` L65-L109，`README.md` L42）。
- 工程：`202 + run_id` 控制面、SSE 数据面、`Idempotency-Key`、Redis 锁、`runs/run_steps` 恢复、PostgreSQL `SKIP LOCKED` worker + 租约 + 死信、限流、Prometheus、`trace_id`。
- 存储：PostgreSQL 16 + pgvector（`migrations/0013_rag.sql` 建 `vector(1024)` 与 HNSW 索引）+ Redis；15 个迁移；内存实现只做测试替身。
- RAG：BM25 + 向量检索融合（`internal/agent/knowledge/bm25.go`、`retriever.go`），有离线检索评测数据。
- 评测：60 条回归集（`internal/agent/eval/testdata/regression.json`，实测 `cases=60`），`docs/eval-results/` 有 23 份结果 JSON，含 `ablation-m20.json`、`memory-gap.json`、`rag-eval-*.json`；`/eval` 面板与 `cmd/evaldiff`。
- 扩展：3 个领域包 `go-backend` / `java-backend` / `ai-agent`（`domainpacks/*/manifest.yaml`）；复习 SM-2；JD 别名匹配；申请材料包；简历规则诊断。
- 前端：无 SPA，只有 `//go:embed` 的单页 demo（`internal/api/http/demo/`，3 个文件）。
- 部署：`deploy/docker-compose.yml` 只起 PostgreSQL + Redis；无 Dockerfile、无 nginx.conf，启动方式是 `go run ./cmd/server` 或本地二进制。
- 文档一致性：`docs/development-plan.md` L1262 的「不做清单」写着「向量数据库 / RAG 知识库」不做，但 README 与 `internal/agent/knowledge/` 已有 M20 RAG；L1256-L1266 的其余不做项（ASR、支付、RBAC/找回密码/OAuth、前端 SPA、多 Agent、OCR）与实现一致。答辩时若引用 L1262 会与实现冲突，应改引用 README 的 M20 章节。

---

## 3. 逐条对照表

> 「是否相同」指实现方式，不指功能名字。「本地更好」只写有证据的相对优势；「本地不如/缺失」不为本地找补。

| 能力点 | 参考项目 | 本地项目 | 实现方式是否相同 | 本地更好的地方 | 本地明显不如或缺失的地方 |
| --- | --- | --- | --- | --- | --- |
| 产品闭环 | 有：注册→简历→预测/面试→报告→支付 | 有：简历→JD→面试→报告→复习 | 不同 | 本地多 JD 匹配与间隔复习，训练链路更长 | 缺预测题、支付、完整用户中心；闭环只由 demo 单页承载 |
| 简历上传解析 | 有：PDF 工具 + 异步服务 | 有：`/v1/resume/parse`，txt/md/docx/pdf | 不同 | 本地同步解析、格式与大小校验明确，离线可测 | 无 OSS、无异步解析；无 OCR（两边都没有可确认的 OCR） |
| 简历诊断/改写 | 无独立能力（只用于预测出题） | 有：规则版总分、A-E 维度、差→好改写 | 不适用 | 本地独有，且明说规则版边界 | 规则语义会漏判隐性技能（`project-walkthrough.md` L358） |
| JD 匹配/缺口 | 部分：面试配置有岗位/公司，无匹配分 | 有：`/v1/jd/analyze`、别名归一、缺口清单 | 不同 | 本地独有，可审计别名表 | JD 抓取只适配静态 HTML，不做 JS/登录墙 |
| AI 预测题 | 有：5 方向 × 1 题，adk agent | 无 | — | — | 整块能力缺失；本地只有 JD 复用题纲 |
| 综合面试校招/社招 | 有：两套 agent + 两套页面 | 部分：只有难度 + 领域包 | 不同 | 本地规则可复现、可单测 | 缺校招/社招的产品级策略与文案分层 |
| 专项面试 | 有：Go/Java/MySQL/Redis/MQ 五 agent，prompt 驱动 | 有：Go/Java/AI Agent 三包，manifest 结构化题纲 | 概念同、实现不同 | 本地维度/题纲/权重可配置、启动强校验、AI Agent 包更贴当前岗位 | 技术栈少三个（MySQL/Redis/MQ）；没有独立专项对话 agent |
| 追问决策 | LLM 评分→deepen/continue/lower/switch，阈值 8/4、连续 2 次 | 10 级门（8 道硬门：时间/题纲/追问总预算/深度/单题追问/不会/评分降级/覆盖；再加模型建议与兜底）+ 强弱话题补偿 | 不同 | 本地确定性、可回放、预算与覆盖门完整 | 灵活度低；弱项补偿是固定规则，遇到特殊候选可能僵化 |
| 单题评分 | 4 维 1-10，40/25/20/15 | 5 维 1-5，30/25/20/15/10 + 证据/加减分/缺失点 | 不同 | 本地证据可定位、缺失点独立、领域包可换维度 | 1-5 粒度粗；本地自己承认档位边界对 ±1 分敏感（`project-walkthrough.md` L350） |
| 话题覆盖/自适应 | TopicTracker + 话题树 + SuggestNextTopic | 题纲 + topic history + expected_points 覆盖门 | 不同 | 本地覆盖判定可解释、题纲进度可见 | 无话题树与全局「下一话题」推荐 |
| 会话持久化 | 内存 map，崩溃丢进行中会话 | PostgreSQL 单一事实源，turn/run/run_step 落库 | 不同 | 本地崩溃恢复、跨实例、可审计 | 表更多、写入更重；实现复杂度明显更高 |
| 复盘报告 | 报告 agent + 面试信息工具 | 3 个只读工具 + 确定性分数 + 模型点评 + 降级 | 思路相近、实现不同 | 本地分数不由模型决定、工具白名单、失败降级、token 快照 | 无跨场趋势、多轮 critic；参考的报告 agent 自主性更强 |
| 间隔复习 | 无 | 有：SM-2、变式题、客观/自评双轨、每日上限 | — | 本地独有 | 客观性依赖真实模型；heuristic 只作长度参考 |
| 记忆/上下文压缩 | 只保留最近 2 轮 | 分层记忆：summary + recent turns，持久化 | 不同 | 本地有压缩、持久化与对比评测 | 压缩质量只有本地自评，缺人工标注 |
| RAG | Milvus + splitter/embedding/retriever + Jaccard 重排 | pgvector HNSW + BM25 + 融合权重 | 不同 | 本地少一个中间件，有离线检索评测与权重消融 | 无语料导入后台/飞书导入；语料规模小 |
| 多 Agent 协作 | 有：Host + 技术 + 项目，Agent-as-Tool | 无：单 Agent + 确定性规则 | — | 单 Agent 规则链更可控、可回放（能力不对等） | 缺多角色协作、自主规划与 Agent 间交接 |
| ASR 语音 | 有：capability/transcribe + 限流降级 | 无（明确不做） | — | — | 缺语音输入，演示差异化少一项 |
| 鉴权 | JWT + GitHub/Google/微信 OAuth + 找回密码 | 邮箱密码 bcrypt + HS256 JWT + 匿名 demo | 部分相同（都用 JWT） | 本地 demo 通道限流、用户隔离测试 | 无 OAuth、找回密码、RBAC、刷新令牌 |
| 支付/订阅 | 有：Stripe/PayPal + webhook + 订阅事件 | 无（明确不做） | — | — | 缺商业化闭环；这是参考的产品优势 |
| 用户中心/历史 | 有：10 个页面 + 订单/记录模型 | 部分：session/report/review API，无列表页与前端 | 不同 | 本地有复习到期队列 | 无用户中心、历史列表、报告归档页面 |
| 前端 | Next.js 24 页，AntD/Zustand/TanStack | 内嵌单页 demo（HTML/CSS/JS，3 个文件，约 172 KiB） | 不同 | 本地零构建、embed、离线可演示 | 产品展示力、交互完整度差距大；面试官看不到完整界面 |
| 部署 | 全栈 compose + nginx + prod compose + LiteLLM | compose 仅 PG/Redis，`go run` | 不同 | 本地依赖少、启动快 | 无 Dockerfile/nginx/生产镜像；「怎么上线」会被追问 |
| 数据存储 | MySQL+GORM（15 个模型文件）+ Redis + Milvus + OSS/SQLite | PostgreSQL+pgvector + Redis，15 个迁移 | 不同 | 本地单一事实源、迁移可审计、SQL 显式 | 无对象存储；不展示 MySQL 生态经验 |
| 异步/队列 | Redis Stream MQ + 生成锁 | PG `SKIP LOCKED` + 租约 + 死信 | 不同 | 本地 fencing/租约/死信闭环，少一个中间件 | 无广播/延迟队列；空队列靠轮询 |
| 幂等/恢复 | 支付幂等；面试无 run 级恢复 | 三重幂等 + `run_steps` 恢复 | 不同 | 本地独有且覆盖面试主链路 | 恢复粒度到 step，不是精确断点 |
| 限流/熔断 | 内存/Redis 限流 + gobreaker + token 配额 | Redis 固定窗口 + fail-open + 指标 | 部分相同 | 本地按用户+动作、demo 更严、指标可查 | 无熔断器、无用户级模型配额 |
| 可观测性 | CozeLoop + OTel + 飞书告警 | Prometheus + trace_id/request_id + 可选 OTLP JSON | 部分相同 | 本地指标覆盖 run/worker/锁/限流/死信 | 无集中存储、采样、告警、跨实例聚合 |
| 评测体系 | Ragas/TruLens 脚本 + 4 条数据；报告无分数 | 60 条回归 + 5 门槛 + 23 份结果 + `/eval` + 消融对比 | 不同 | 本地有可复核结果与门槛，参考只有占位报告 | 本地缺 Ragas/TruLens 语义指标；只跑单次 |
| 测试 | 25 个 `_test.go` | 59 个 `_test.go` | 不同 | 本地测试文件更多，覆盖状态机/恢复/幂等（按文件推断） | 未发现 E2E 测试；未运行，不能比较通过率 |
| 模型接入 | OpenAI/Ark/Gemini/Ollama + LiteLLM + 用户 BYOK + 配额 | OpenAI 兼容 + heuristic 回退 | 不同 | 本地无 key 可完整演示，测试不依赖外部服务 | 无多模型路由、BYOK、配额 |
| 国际化 | 有 i18n 目录与脚本，实际状态无法确认 | 无 | — | — | 缺多语言 |
| 许可 | Prosperity Public License，禁商业与付费教学 | MIT | 不同 | 本地许可更宽松 | 若借鉴参考代码/文案，参考许可会限制商业与付费教学用途 |

---

## 4. 两个问题的直接回答

### 4.1 是否存在高度雷同、可能被认定为抄袭的痕迹

**结论：没有发现可被认定为「直接复制代码/大段文字」的证据；但「同题 + 同框架 + 同概念」成立，且本地无法用 git 历史自证独立创作。**

#### 4.1.1 机械比对结果（可复现）

| 检测项 | 结果 | 说明 |
| --- | --- | --- |
| 连续 5 行代码块重合（去空白、去注释/import/package 后比较，窗口 ≥120 字符） | **0** | 在本地 181 个 Go 文件、36,708 行中未命中参考 180 个 Go 文件、60,138 行 |
| 单行代码重合（≥40 字符，去注释/import/package） | **18** | 全部是通用样板，见 4.1.2 |
| Go 注释重合（以 `//` 开头、≥16 字符） | **0** | 参考大量中文注释，本地没有一条相同 |
| Markdown 行重合（≥25 字符） | **0** | README/docs 没有整行搬运 |
| 类型/函数同名（大小写不敏感） | **21 / 1078** | `clampScore, Config, contains, containsAny, DimensionScores, EmbeddingConfig, evaluateNode, Evaluator, HashPassword, init, main, NewEvaluator, providerConfig, RateLimitConfig, Register, Registry, Response, service, tokenize, truncate, usage`。注意：`DimensionScores` 实际匹配的是本地未导出的 `dimensionScores` 辅助函数，不是同名类型；`main/init/service/usage` 是通用名 |
| Go 文件 basename 重合 | **19** | `client.go, config.go, embedding.go, errors.go, handlers.go, main.go, middleware.go, password.go, prompt.go, provider.go, ratelimit.go, registry.go, response.go, resume.go, retriever.go, router.go, service.go, types.go, user.go`。属于同名不同实现：参考的 `resume.go`/`prompt.go`/`retriever.go`/`embedding.go` 与本地对应文件内容不对应，21 个同名符号里也没有 `Retrieve`/`Embed`/`Parse` 这类业务函数 |

#### 4.1.2 18 条单行重合的具体位置（全部可核对）

- `cmd/ablation/main.go:354`、`cmd/memorycompare/main.go:346`、`internal/agent/knowledge/embedding.go:212`、`internal/agent/memory/memory.go:292`、`internal/domain/resume/profile.go:232`：`func truncate(s string, max int) string {`
- `internal/agent/knowledge/embedding_test.go:15,63`、`retriever_test.go:77,117,155,214,240`：`server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {`
- `internal/api/http/auth_flow_test.go:39`：``AccessToken string `json:"access_token"` ``
- `internal/auth/password.go:18`：`func HashPassword(password string) (string, error) {`
- `internal/domain/review/service.go:505`：`func clampScore(score float64) float64 {`
- `internal/platform/trace/trace.go:155`、`internal/store/redis/lock_integration_test.go:34,36`：`context.WithTimeout` / `client.Ping` 样板

这些行都不包含业务语义，且同名函数实现不同：参考的 `clampScore` 把分数夹到 1-10（`single_answer_evaluator.go` L133-L142），本地把 0-100 分数夹到 0-100 并四舍五入（`internal/domain/review/service.go` L505-L513）。

#### 4.1.3 文件级对比（关键模块逐组看）

| 对比组 | 参考项目 | 本地项目 | 判断 |
| --- | --- | --- | --- |
| 评分契约 | `agents/evaluation/types.go`：`DimensionScores{Correctness, Depth, Completeness, Practicality}`，1-10，40/25/20/15；`NextAction` 四值 `continue/deepen/switch/lower`（L6-L34） | `domain/evaluation/rubric.go`：`Dimension` 五值 `correctness/depth/engineering_judgment/structure/evidence_credibility`，1-5，30/25/20/15/10；`Result/Score/Record/Evidence/Adjustment`（L15-L45、L201-L232 与同包类型） | 结构、字段、分值、权重、枚举都不同 |
| 追问裁决 | `engine/graph_loop.go`：图内 `evaluate → branch`，分支节点 `deepen/continue/lower/switch`；`ScoreHistory` 连续判断；阈值 8/4、单话题 5 题、连续 2 次（`types.go` L54-L74） | `domain/followup/decide.go`：纯函数 `Decide(Input) Decision`，10 级门 + 强弱话题补偿，输出只有 `follow_up/next_question/finish`（L11-L109） | 算法不同：LLM 动作分支 vs 规则门链 |
| 会话与状态 | `InterviewState` 放 `Session *InterviewSession`、`ScoreHistory`、`TopicTracker`、`AnswerChan`；会话在内存 map（`engine/types.go` L10-L40） | `AnswerState{In, Snapshot, Outcome, Question}` 单回合值；状态来自 PostgreSQL 快照，节点不写业务状态（`agent/interview/graph.go` L116-L120） | 数据结构不同：长驻会话对象 vs 单回合快照 |
| Agent 组织 | `adk.NewChatModelAgent` + `NewAgentTool`，Host 调度两个子 Agent，`MaxIterations: 30`（`multiagent/orchestrator.go` L46-L90） | 单 Agent；`compose.Graph` 固定 5 节点；无 adk、无 Agent-as-Tool | 组织方式不同 |
| RAG | Milvus + `splitter/embedding/indexer/retriever`，`hybrid_search.go` 做向量召回，`reranker.go` L112 用 `tokenize` 做 Jaccard 重排 | pgvector HNSW + 自写 BM25 + 融合权重；`knowledge/bm25.go`、`retriever.go` | 「hybrid」名字相同，融合对象不同：参考是向量+Jaccard，本地是 BM25+向量 |
| 报告 | `adk` 报告 Agent + `GetInterviewInfoTool`，模型主导（`record_evaluation_agent.go` L14-L37） | 3 个只读工具取数，分数确定性计算，模型只写点评（`README.md` L43） | 控制权不同：模型主导 vs 规则主导 |
| 目录 | `internal/agents/{evaluation,interview,llm,prediction,resume,tools,multiagent}` | `internal/agent/{eval,interview,llm,knowledge,memory,prompt,report,tools}` | 有 4 个包名相近（evaluation/eval、interview、llm、tools），另有 19 个同名 Go 文件（见 4.1.1）；但目录层级、其余包与文件内容都不对应，属于 eino 项目的常规分层 |

#### 4.1.4 命名层面的「像」与「不像」

- 像的地方：`evaluateNode`、`clampScore`、`truncate`、`tokenize` 两边都有；都按 eino 惯例把图节点命名为动词。
- 不像的地方：参考的核心类型是 `InterviewState/InterviewSession/DimensionScores/EvaluationResult/ScoreHistory/TopicTracker`；本地是 `AnswerState/TurnSnapshot/Score/Result/Record/TopicHistory`。参考的核心常量是 `ActionDeepen/ActionLower`；本地是 `ReasonTimeLimit/ReasonCoverageSatisfied` 这类可回放原因码。这些是「同一领域自然会出现的词」，不是逐字对应的命名体系。
- 另一个需要如实说明的巧合：两边 Go 文件数 180 vs 181、总行数 60k vs 37k，且有 19 个同名 Go 文件（`embedding.go`、`retriever.go`、`prompt.go`、`resume.go` 等）。同名不代表同内容，但面试官只看目录时可能产生联想；解释时应直接给出模块差异，而不是回避。

#### 4.1.5 时间线与作者

- 参考：单个提交，2026-05-13，作者王中阳。
- 本地：52 个提交，2026-11-06 15:30:07 至 2026-11-08，作者 waiting2050；前 5 个提交分别在 15:30:07、15:30:19、15:30:29、15:31:01、15:31:11。
- 结论：参考早于本地约 6 个月，本地不可能是参考的来源；但本地提交节奏不像自然开发（3 天 52 个提交、前 5 个 64 秒内完成），**git 历史不能证明本地是独立从零写的**。这属于「来源可信度风险」，不是「抄代码」的证据。
- 本地 README/docs 没有出现参考项目名、作者名或链接，也没有「参考了某某项目」的致谢。如果确实参考过其产品设计，这一点在答辩时会被追问。

#### 4.1.6 最终判断

- 以「代码/文字复制」为标准：**不成立**。0 个 5 行块、0 条注释、0 行 Markdown、18 条通用单行，且 19 个同名文件内容不对应，达不到抄袭认定门槛。
- 以「同题同框架」为标准：**成立**。同做 AI 模拟面试、同用 eino、都有专项面试与报告 Agent 概念；参考公开发布更早。
- 以「面试官观感」为标准：**有风险**。风险不来自代码，而来自：题目相同、框架相同、本地没有引用说明、git 历史像重建。被问到时，最稳妥的说法是承认同题与同框架，然后用 4.1.3 的文件级差异说明自己的取舍，而不是声称「完全没看过」。

### 4.2 参考有而本地没有的能力：全量清单与取舍

#### 值得补（按性价比排序）

| 能力 | 一句理由 |
| --- | --- |
| 完整前端（至少面试页 + 报告页） | 当前只有 embed demo，面试官看不到产品闭环；这是参考最明显的展示优势 |
| AI 预测题 | 可复用现有简历解析 + 题纲/LLM 管线，补齐「面试前」环节，工作量可控 |
| Dockerfile + nginx/单命令部署 | 现在只有依赖 compose，无法回答「怎么上线」；补一个多阶段镜像即可 |
| 找回密码/邮箱验证 | 当前鉴权自述为最小实现；真实产品最低配，成本低 |
| 用户级模型配置/配额 | 多用户演示时避免共用一个 key，参考已验证可行；本地只需加一张表和配额检查 |
| 校招/社招场景化 | 本地只有难度与领域包，缺产品层场景；可用领域包 + prompt 变体实现，不必照搬 agent |
| 面试历史列表/报告归档 | API 已有 session/report，缺列表接口与页面；补列表即可形成「历史复盘」 |
| ASR 语音输入（可选） | 是产品差异点，但涉及第三方服务与计费；优先级低于前七项 |

#### 不必补

| 能力 | 一句理由 |
| --- | --- |
| 支付/订阅 | 与面试训练核心无关，合规、对账、webhook 联调成本高，且参考的许可是非商业 |
| 多 Agent 协作（adk Agent-as-Tool） | 与本地「规则优先、可回放」的定位冲突；硬加会削弱现有确定性叙事 |
| Milvus 独立向量库 | pgvector 已满足当前语料规模，换库只增加运维与部署复杂度 |
| Redis Stream MQ / CozeLoop / 飞书告警 | 单机与课程演示规模用不上；现有 PG 队列 + Prometheus 足够 |
| 国际化 | 非目标；参考自己的 i18n 状态也无法确认 |
| 微信 OAuth/小程序 | 非目标，合规与审核成本高 |
| OCR/图片简历 | 成本高、收益低；本地已返回明确错误，比硬做更诚实 |
| Ragas/TruLens | 参考只是占位报告，没有真实分数；本地 60 条回归更可信。若要补，补语义指标即可，不必换框架 |
| LiteLLM 模型网关 | 单模型 + OpenAI 兼容已够用；多模型路由是平台级需求 |

---

## 5. 是否存在直接复制：明确答复

**未发现直接复制该仓库代码或大段文字的痕迹，因此没有需要列出的「被复制文件 + 行号范围」。**

支撑这一结论的最小证据集：

1. 5 行连续代码块：0 命中（方法见 4.1.1）。
2. Go 注释：0 命中；参考的中文注释风格（如 `graph_loop.go` L30 的 `InterviewState 面试状态，在 Graph 各节点间传递`）在本地没有对应文本。
3. Markdown：0 行命中；两边 README 的结构、章节名、示例命令都不同。
4. 18 条单行命中全部是通用样板，且同名函数实现不同（`clampScore` 1-10 vs 0-100）。
5. 评分、追问、会话、Agent、RAG、报告六组核心文件在数据结构与算法上都不同（4.1.3）。

需要同时承认的两点：

- 这些检测只能排除「逐字/逐块复制」，不能排除「看过产品后独立重写」或「参考了未入库的草稿」。仓库里没有这类证据，也没有反证。
- 参考项目使用 Prosperity Public License 7.0.0，禁止商业盈利与付费教学。即使没有代码复制，如果本地把它当作付费课程/商业产品的一部分，仍应先确认许可边界；这与抄袭判断是两件事。

---

## 6. 方法与限制

- 参考仓库通过 `git clone --depth 50` 获取，只有一个压缩提交，无法做提交级溯源；所有行号以 `426a154` 为准。
- 本地只读，没有修改任何功能代码；本文是唯一新增文件。
- 未执行 `go test`、`go build`、`npm build`、`docker compose up`；因此「实现质量/能否跑通」只按代码与文档判断，不按运行结果判断。
- 「无法确认」的典型项：参考的 OAuth/支付/ASR 真实联调、Milvus 集合内容、前端当前能否构建、Ragas/TruLens 真实分数；本地的真实模型评分稳定性、60 条回归之外的表现、demo 页在多浏览器下的一致性。
- 机械比对脚本口径：Go 代码按行去首尾空白、压缩连续空白；排除 `//`、`/*`、`*`、`package`、`import` 开头行；5 行窗口拼接后长度 ≥120 才计入。Markdown 按 ≥25 字符整行精确匹配。符号按 `^type X` / `^func X(` 提取，大小写不敏感，排除 `.git/bin/tmp`。

---

## 7. 补齐计划：只补能撑住 3 分钟追问的能力

筛选口径只有两条，不按数量：

1. 补上之后，技术面能围绕它连续追问 3 分钟以上（有实现细节、有失败路径、有可验证结果）。
2. 不依赖付费服务、外部账号或必须联网才能运行的组件。

时间按「寒假实习投递前的可用周」估算，单位是人日。

### 7.1 判定表

| 缺失能力 | 面试价值 | 工作量 | 补/不补 | 一句话理由（可直接说出口） |
| --- | --- | --- | --- | --- |
| AI 预测题 | 高 | 0.5 人日 | **补** | 复用正式提问的同一生成器，降级、去重、长度校验只有一份实现，预测题不会变成第二套模型链路。 |
| 面试历史列表 / 报告归档入口 | 中高 | 0.4 人日 | **补** | 已有 `sessions(user_id, created_at DESC)` 索引，补 keyset 分页和归属过滤就能讲清「多租户列表怎么做」。 |
| Dockerfile + 单命令镜像 | 中 | 0.3 人日 | **补** | 多阶段构建 + 静态二进制 + 非 root，能在 CI 里真实构建一次，比口头说「能部署」可信。 |
| 完整前端（面试页 + 报告页） | 高 | 5–8 人日 | 不补 | 前端只影响演示观感，不影响被追问的深度；半成品前端比没有更减分。 |
| 找回密码 / 邮箱验证 | 低 | 0.5 人日 | 不补 | 找回密码要先接邮件服务商，那是运维配置，不是这个项目要证明的工程能力。 |
| 用户级模型配额（token 预算） | 中高 | 0.5 人日 | **补** | 限额与花费都落在同一套账本语义里，用户级先于会话级判定；拒绝能落到 SQL、错误码和指标三层证据，这是多租户最常被追问的一层。 |
| 校招 / 社招场景化 | 低 | 0.5 人日 | 不补 | 校招社招是 prompt 内容差异，难度和领域包已经覆盖了同一层。 |
| ASR 语音输入 | 低 | 1 人日+ | 不补 | 语音输入依赖第三方计费接口，我优先保证评分链路离线可复现。 |
| 支付 / 订阅 | 低 | 2 人日+ | 不补 | 没有支付是因为它不是产品，重点是让评分可复核。 |
| 多 Agent 协作（adk Agent-as-Tool） | 低 | 2 人日+ | 不补 | 多 Agent 会让追问路径不可回放，我用确定性门链换可解释。 |
| Milvus 独立向量库 | 低 | 1 人日 | 不补 | pgvector 和业务表同库同事务，语料规模没到需要独立向量库。 |
| Redis Stream / MQ | 低 | 1 人日 | 不补 | PG 队列 + 租约能给出 `SKIP LOCKED` 和 fencing token 的证据，Redis Stream 反而说不清重复消费。 |
| CozeLoop / 飞书告警 | 低 | 0.5 人日 | 不补 | 告警是部署环境的事，本地用 Prometheus 指标已经能定位。 |
| 国际化 | 低 | 1 人日 | 不补 | 单语言不是缺陷，是范围；我没有把没做完的国际化包装成已完成。 |
| 微信 OAuth / 小程序 | 低 | 1 人日 | 不补 | 微信登录需要企业主体和审核，个人项目接不了。 |
| OCR / 图片简历 | 低 | 1 人日 | 不补 | OCR 成本高、准确率不可控；我宁可明确返回 `image_requires_ocr`。 |
| Ragas / TruLens | 低 | 0.5 人日 | 不补 | 框架不产生分数；我的 60 条回归是可复现的，比占位报告更可信。 |
| LiteLLM 模型网关 | 低 | 0.5 人日 | 不补 | 单模型 + OpenAI 兼容协议已经够用，多模型路由是平台需求。 |

主项三项合计约 1.2 人日，仍低于 2 人日上限；追加的配额、告警、观测映射与列表字段另计约 1.1 人日，来自后续需求，同样没有新增付费依赖或外部账号。

### 7.2 三处「选了不同方案」：把另一种取舍讲清楚

这三处不是「没做」，是「在同一个问题上选了另一条路」。面试时先给参考方案，再给自己的约束和代价。

| 问题 | 参考做法 | 本地做法 | 一句话口径 |
| --- | --- | --- | --- |
| 向量库 | Milvus 独立服务 + 自建 splitter/embedding/indexer/retriever | PostgreSQL + pgvector（HNSW），BM25 与向量融合 | 「Milvus 适合亿级向量和独立扩缩容；我的语料在万级，pgvector 让知识入库和会话数据同库同事务，少一个故障域。」 |
| 异步队列 | Redis Stream 消费组 | PG `FOR UPDATE SKIP LOCKED` + 租约 + `lease_id` fencing + 死信 | 「Stream 的消费组解决分发，解决不了执行到一半崩了谁接手；我用租约到期、fencing token 和失败计数进死信，重复消费由幂等键兜底。」 |
| 多 Agent | eino/adk，Host 调度两个子 Agent（Agent-as-Tool） | 单 Agent，固定 5 节点图 + 10 级确定性追问门 | 「多 Agent 适合开放式任务，面试追问需要可回放；我把决策做成纯函数，同样输入同样输出，报告里每个分数都能追到具体回合。」 |

补充代价，避免只说优点：

- pgvector 的边界是向量规模到千万级后，索引构建和内存会先成为瓶颈，届时再拆 Milvus 才是合理时机。
- PG 队列的代价是轮询延迟（默认秒级），换来的是队列状态可直接用 SQL 查、能和业务状态同事务提交。
- 单 Agent 的代价是复杂任务的探索能力弱，换来的是每条追问路径都能在 `run_steps` 里复盘。

### 7.3 补齐项与验收证据

#### A. AI 预测题

- **接口**：`POST /v1/predictions`。请求字段 `target_role`（必填）、`difficulty`、`jd_text`、`domain`、`resume_text` 或 `resume_profile`（至少一个）、`count`（默认 5，夹取上限 10）；响应 `{domain, generator, model_called, degraded, questions[]}`，每题含 `topic/question/objective/expected_points/degraded`。
- **复用而非另起链路**：`internal/agent/interview.Predictor` 直接调用正式提问用的 `QuestionGenerator`，降级、去重、长度校验只有一份实现；没有领域包时用简历技能/项目派生大纲。
- **测试**：`TestPredictionReturnsDeterministicQuestions`、`TestPredictionValidation`、`TestPredictDegradesWhenModelFails`、`TestPredictClampsCount`、`TestPredictIsDeterministicAndDeduplicated`。
- **命令**：`go test ./internal/agent/interview ./internal/api/http -run 'Predict|Prediction' -count=1`。

#### B. 面试历史列表 + `latest_report`

- **接口**：`GET /v1/sessions?limit=&before=&before_id=`；响应 `{sessions[], next_before, next_before_id}`，会话上新增可选字段 `latest_report: {total_score, score_band, generated_at}`。游标仍是 `(created_at, id)` 元组降序，语义未变；没有报告时字段缺省，不补 0。
- **单次查询**：`LEFT JOIN LATERAL (SELECT overall_score, created_at FROM reports WHERE session_id = s.id ORDER BY created_at DESC LIMIT 1) rep ON true`，不逐条回查。
- **测试**：`TestListSessionsIsOwnerScopedAndPaginated`、`TestListSessionsValidatesCursor`（HTTP 层，含无报告为 nil）；`TestSessionListByUserKeysetPagination`、`TestSessionListIncludesLatestReport`（集成层，覆盖有报告/无报告/多份报告取最新）。
- **命令**：`go test ./internal/api/http -run ListSessions -count=1`；`DATABASE_URL=... go test -tags=integration ./internal/store/postgres -run 'TestSessionList' -count=1`。

#### C. Dockerfile

- **实现**：`golang:1.25.1-alpine` 构建、`CGO_ENABLED=0` 静态二进制、`alpine:3.20` 运行、非 root `uid 10001`、`EXPOSE 8080`；`.dockerignore` 排除 `.git`、`bin`、`tmp`、测试与文档。
- **证据**：CI 新增 `docker` job，真实执行 `docker build -t ai-interview-agent:ci .`。本机 Docker daemon 未运行，因此以 CI 构建结果为准，不把「本地没跑」写成「已验证」。

#### D. 用户级模型配额（追加）

- **迁移**：`0016_model_budget.sql` 新建 `model_budget_limits(scope, scope_id, limit_tokens, updated_at)`，`scope ∈ {user, session}`；花费仍从 `runs` 账本聚合，不双写。
- **判定顺序**：先查用户级总额，命中即拒绝并返回 `429 user_budget_exceeded`；用户级通过后再查会话级，超限返回 `429 session_budget_exceeded`；两者都通过才创建 run。未配置限额视为不限；读限额失败按 fail-closed 处理。
- **指标**：`ai_interview_model_budget_rejections_total{scope}`，同一请求只计一次。
- **测试**：`TestBudgetRejectsExhaustedUserEvenWhenSessionHasRoom`、`TestBudgetRejectsExhaustedSessionEvenWhenUserHasRoom`、`TestBudgetUserScopeWinsWhenBothAreExhausted`、`TestBudgetWithoutLimitsAllowsStart`、`TestStartRejectsExceededBudgetWith429`、`TestBudgetRepositoryReadsRunsLedger`。
- **命令**：`go test ./internal/app ./internal/api/http -run Budget -count=1`。

#### E. 轻量告警（追加）

- **规则**（进程内，启动即生效，无外部依赖）：`run_dead_letter`（死信 delta ≥1，10 分钟冷却）、`model_budget_rejection`（预算拒绝 delta ≥5，15 分钟冷却）、`stale_run_detected`（stale run delta ≥1，10 分钟冷却）。
- **触发与抑制**：按采样窗口对累计计数器取 delta；首次采样只 seed，重启不重放历史；同一规则在冷却期内只报一次；计数器重置按 0 delta 处理。
- **出口**：结构化日志 `alert fired`，字段 `rule/severity/signal/value/message`。
- **测试**：`TestFirstObservationOnlySeeds`、`TestDeadLetterFiresOncePerCooldown`、`TestBudgetRejectionNeedsThreshold`、`TestStaleRunFires`、`TestCounterResetDoesNotFire`。
- **命令**：`go test ./internal/platform/alert -count=1`。

#### F. 可观测性映射（追加）

- 映射表见 `docs/design-rationale.md` §8.1，覆盖状态机迁移、模型调用耗时与失败率、降级、预算拒绝、死信、stale run、锁、限流、SSE、告警、报告列表。
- 本轮只补两个确实缺失的指标：`ai_interview_session_transitions_total{status,phase}`、`ai_interview_model_call_seconds{provider,model,result}`；其余信号沿用已有指标或日志/事件字段，没有新增空指标。
- **测试**：`TestMetricsCountersAndRendering`、`TestInstrumentedClientRecordsCallsTokensAndSessionUsage`、`TestSessionTransitionMetricTracksSuccessfulStateWrites`。
- **命令**：`go test ./internal/platform/obs ./internal/agent/llm ./internal/app -count=1`。

**CI**：run `43831813730`（commit `c143817`）三个 job 全绿：`unit`（fmt/vet/单测/heuristic 回归）、`integration`（pgvector pg16 + redis 7）、`docker`（真实 `docker build`）。
