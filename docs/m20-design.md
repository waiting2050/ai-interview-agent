# M20 设计：RAG 检索、工具自主调用、分层记忆

> 状态：**待评审**。本文件只定设计与取舍，不含实现。评审通过后再按第 9 节顺序写码，每完成一块单独汇报。
>
> 硬约束：无 key 仍能完整跑通；付费能力可关闭；不改 M6 门槛、60 条回归集期望值、评分公式；四命令全绿；**每块能力必须配可复现的数字**，否则视为没做。

---

## 0. 目标与非目标

**目标**

- 补上三类 Agent 能力：可解释的检索增强（RAG）、模型自主选择只读工具、三层会话记忆。
- 每类能力都有真实落点、降级路径和**可讲的技术数字**。
- 默认链路本地优先：本地 Ollama embedding + pgvector，零外部 embedding 费用；远程 embedding 仅作可选项。
- 不改变评分与档位的确定性：检索、工具、记忆只影响「问什么 / 写什么点评」，不影响「得多少分」。

**非目标**

- 不新增或修改评测样本、门槛、评分公式。
- 不对候选人历史回答做向量检索；不做跨用户知识库。
- 不把原生 function calling 作为唯一路径；不引入外部向量数据库、消息队列或托管记忆服务。
- 不做自动写入领域状态；记忆是派生缓存，不是事实源。
- 不在本轮擅自下载模型或安装 Ollama；先报体积与耗时，确认后再执行。

---

## 0.1 环境探测结果（2026-11-08，未下载）

| 项 | 实测 |
| --- | --- |
| Ollama | **未安装**（`Get-Command ollama` 无结果，`ollama list` 无输出） |
| CPU | Intel Core Ultra 5 125H |
| 内存 | 31.5 GB |
| GPU | NVIDIA GeForce RTX 4060（WMI 报告 AdapterRAM ≈ 4 GB，实际显存需 `nvidia-smi` 复核）；另有 Intel Arc 核显 |
| 当前可用 embedding | 无本地模型、无远程 embedding key |
| 结论 | 本轮只能跑 BM25-only；要出 hybrid 数字，需要先安装 Ollama 并拉取模型 |

**安装/下载清单（需要你确认后才执行）**

| 项 | 体积 | 说明 |
| --- | --- | --- |
| Ollama Windows 安装包 | 约 **1.47 GB**；官方要求预留 **4 GB** 磁盘 | 未实测下载耗时，取决于带宽 |
| `bge-m3` | **1.2 GB**，567M 参数，1024 维，8K 上下文 | 多语言，中文检索强 |
| `quentinz/bge-small-zh-v1.5` | **48 MB**（f16；q4_0 约 17 MB），512 维，512 上下文 | 中文小模型，第三方 namespace |
| 合计 | 约 **2.7 GB** | 31.5 GB 内存足够；CPU 可跑，RTX 4060 可加速 |

> 以上为官方/模型页公开数字；**下载耗时、单条 embed 延迟、全量入库耗时**必须在安装后实测，未实测不编造。若你不同意安装，hybrid 组数字标记为“环境不可用”，默认走 BM25-only。

---

## 0.2 数字与测量原则

- 每个数字必须带：测量命令、数据文件、运行环境（CPU/内存/GPU、模型版本、日期）。
- 检索质量用固定 query 集，不为了让 hybrid 好看而调参；如果 hybrid 不如 BM25，照实写。
- 成本口径：本地 embedding 只报耗时、磁盘与内存；远程 embedding 才报 token 与费用。
- 实现完成后，同一张数字表原样复制到 `README.md` 与 `docs/interview-qa.md` 的 M20 模块。

---

## 1. 现状与插入点

| 现有能力 | 位置 | M20 关系 |
| --- | --- | --- |
| 模型调用 | `internal/agent/llm` 的 `Client.Generate`，只接受 system/user、返回文本 | 对话协议保持 JSON 文本；新增独立 embedding 客户端（Ollama / OpenAI 兼容） |
| 出题 / 追问 | `internal/agent/question.go`、`internal/agent/prompt` 的 `QuestionInput` / `QuestionUser` | 新增可选 `<reference>` 与记忆块，不改变原有字段与输出结构 |
| 报告 | `internal/app/report.go`、`internal/agent/report`、`internal/agent/tools` | 保留确定性 `Collect`；新增可选自主工具循环与 `<standard_coverage>` |
| 会话状态 | `internal/domain/session`、`internal/app/answer_commit.go` | 完全不改 |
| 存储 | PostgreSQL + `migrations/` | 新增 `knowledge_chunks`、`session_memories`、`rag_ingest_runs` |
| 评测 | `internal/agent/eval`、`docs/eval-results/` | 评估入口不接 RAG / 工具 / 记忆 |
| 本地依赖 | `deploy/docker-compose.yml` 的 `postgres:16-alpine` | 换成 `pgvector/pgvector:pg16`，仍是 PostgreSQL |

---

## 2. 总体数据流

```text
写操作（start / answer / report）
  → app.Service
    → 读结构化状态（session / turn / evaluation）
    → 读分层记忆（最近 4 轮原文 + 早期摘要）
    → 读检索片段（BM25 / 向量 / RRF）
    → eino 图 / 报告生成
    → 结果落库、事件推送

离线
  → cmd/ragindex 扫描语料 → 语义切块 → 本地 embedding（可选）→ knowledge_chunks

评测
  → cmd/evalrunner → 评估器（不经过 RAG / 工具 / 记忆）→ 既有门槛
```

三块能力独立开关：

- `RAG_MODE=off|bm25|hybrid|auto`
- `REPORT_TOOL_MODE=deterministic|autonomous`
- `MEMORY_MODE=off|auto`

---

## 3. RAG 检索

### 3.1 选型与理由：本地优先

**默认：Ollama 本地 embedding + pgvector；远程 OpenAI 兼容 `/embeddings` 仅作可配置项。**

| 候选 | 维度 | 模型体积 | 上下文 | 中文 | 说明 |
| --- | --- | --- | --- | --- | --- |
| `bge-m3` | 1024 | 1.2 GB | 8K | 强，多语言 | 默认候选；检索质量优先 |
| `bge-small-zh-v1.5` | 512 | 48 MB（f16） | 512 | 中文专用 | 体积/速度优先；第三方 namespace |
| 远程 `text-embedding-3-small` | 1536（可请求 1024） | 无本地体积 | 8191 | 中 | 可选项；有 token 费用与网络依赖 |

- 为了统一表结构，**入库维度固定 1024**：`bge-m3` 原生 1024；`bge-small` 需要升维/投影或单独表（见 3.4 的取舍）；远程 OpenAI 通过 `dimensions=1024` 对齐。
- **为什么不用云 embedding**：成本不是唯一理由。离线可复现、无 key、无数据出网、评测环境稳定、CI 不依赖外部服务；代价是首次下载与本地算力占用。
- **为什么保留远程**：本地模型拉不下来或质量不够时，远程是逃生通道；默认关闭，不构成必需依赖。

### 3.2 可讲的技术要点（RAG）

| 选择 | 放弃了什么 | 代价 |
| --- | --- | --- |
| **BM25 与向量用 RRF 融合，k=60** | 放弃「按分数归一化加权」 | RRF 只用名次，丢掉分数强弱；k=60 让前几名差距平滑，但需要两路各取 20 条候选，计算量翻倍 |
| **中文 BM25 用应用内 bigram，不用 `zhparser`** | 放弃 PostgreSQL 原生中文分词与 `tsvector` 索引 | 每次查询加载全部块（<300）并在应用内算分；语料变大后需要缓存或倒排索引 |
| **语义切块：考点/标题为单位，不按字数硬切** | 放弃固定窗口的简单实现 | 切块器要解析 YAML/Markdown 结构；超长小节才按段落二次切，边界规则更多 |
| **pgvector HNSW：m=16，ef_construction=64，ef_search=40** | 放弃 `m=32` 的更高召回与 IVF 的低内存方案 | 100 块规模下召回足够；语料上到万级需要调大 m/ef 或换 IVF |
| **向量维度固定 1024** | 放弃「任意 embedding 模型即插即用」 | 512 维小模型需要升维或另建表；换维度必须全量重建 |
| **无 key/无 Ollama 自动 BM25-only，并在四处可见** | 放弃「有向量就一定更好」的默认假设 | 语义召回缺失；hybrid 与 BM25 的差距必须用检索集数字说话 |

### 3.3 语料与切块

| 来源 | 切块单位 | 预计块数 |
| --- | --- | --- |
| `domainpacks/*/manifest.yaml` | 一个考点一个块（title + objective + expected_points） | 约 40–60 |
| `docs/project-walkthrough.md` | 一个二级/三级标题下的完整小节 | 约 40–60 |
| `docs/design-rationale.md` | 一个小节 | 约 20–30 |
| `docs/development-plan.md` | 只取架构、可靠性、评测相关小节 | 约 15–25 |
| `docs/interview-qa.md` | 一个模块 / 一组问答 | 约 15–20 |
| `README.md` | 能力描述、工程能力、架构小节 | 约 8–12 |

- 先按语义边界切；超过 1200 字才在段落/句子边界二次切分，**不按固定字数硬切**。
- 每块保存：`source_path`、`section_key`（标题路径）、`title`、`content`、`content_hash`、`domain`、`topic_id`、`source_kind`。
- 同一 `(source_path, section_key)` 唯一；内容不变时 `content_hash` 不变，重跑不重复 embedding。
- 不索引评测结果 JSON、截图、迁移 SQL、代码文件。

### 3.4 表、索引与体积

```sql
CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE knowledge_chunks (
    id            text PRIMARY KEY,              -- sha256(source_path + section_key)
    source_path   text NOT NULL,
    section_key   text NOT NULL,
    source_kind   text NOT NULL,
    domain        text NOT NULL DEFAULT '',
    topic_id      text NOT NULL DEFAULT '',
    title         text NOT NULL,
    content       text NOT NULL,
    content_hash  text NOT NULL,
    embedding     vector(1024),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (source_path, section_key)
);

CREATE INDEX knowledge_chunks_embedding_idx
    ON knowledge_chunks USING hnsw (embedding vector_cosine_ops)
    WITH (m = 16, ef_construction = 64);

CREATE TABLE rag_ingest_runs (
    id              text PRIMARY KEY,
    started_at      timestamptz NOT NULL,
    finished_at     timestamptz,
    model           text NOT NULL,
    chunks_total    int NOT NULL DEFAULT 0,
    chunks_embedded int NOT NULL DEFAULT 0,
    prompt_tokens   bigint NOT NULL DEFAULT 0,
    estimated_cost  numeric(12, 6) NOT NULL DEFAULT 0,
    status          text NOT NULL,
    error           text NOT NULL DEFAULT ''
);
```

**体积算术（可复核）**

- 1024 维 float4 = 4096 字节/条；100 条约 **400 KB** 裸向量。
- HNSW 邻接表按 `m=16` 双向约 128 字节/条；加索引与页开销，100 条约 **1–1.5 MB**。
- 文本块按 100 块 × 800 字 ≈ 80 KB；整表（含索引）预计 **<2 MB**。
- `m=16` 是 100–300 块规模下召回/内存的折中；`m=32` 约翻倍邻接内存，当前规模收益不明显。`ef_search=40` 查询时候选 40 条，召回足够且延迟低。

**512 维小模型的取舍**：若选 `bge-small-zh-v1.5`，表仍用 `vector(1024)` 时需要零填充/升维，语义空间会被稀释；更干净的做法是另建 `knowledge_chunks_512` 或改迁移维度并重建。设计默认 **bge-m3 1024 维**；小模型作为对照实验，不进入默认表。

### 3.5 混合检索与 RRF

**词法路（BM25）**

- 分词：ASCII 按 `[a-z0-9_.+-]` 切词并小写；中文按相邻二字 bigram；去掉长度 1 的孤立汉字与常见停用词。
- 公式：`score = Σ idf(t) * tf*(k1+1)/(tf + k1*(1-b+b*dl/avgdl))`，`k1=1.2`、`b=0.75`。
- 查询时读取全部块（当前 404）计算 BM25，取前 20；完全确定性，便于单测。

**向量路（可选）**

- Ollama：`POST http://localhost:11434/api/embed`，`model=bge-m3`，`input=[query]`。
- 远程：`POST {EMBEDDING_BASE_URL}/embeddings`，请求 `dimensions=1024`。
- SQL：`ORDER BY embedding <=> $1 LIMIT 20`，余弦距离；空 embedding 不参与。

**融合**

- 两路各取前 20，按名次做加权 RRF：`score(d) = 1.2/(60 + rank_bm25(d)) + 1.0/(60 + rank_vec(d))`，取前 `RAG_TOP_K`（默认 6）。权重可用 `RAG_BM25_WEIGHT` / `RAG_VECTOR_WEIGHT` 覆盖。
- 同一 `(source_path, section_key)` 去重；无命中返回空，不凑数。
- **为什么 k=60**：RRF 原论文的经验值，压低「单路第一」的绝对优势，让两路都靠前的块排名更高；**为什么不按分数归一化加权**：BM25 分数无上界、余弦相似度有界，归一化需要按查询动态缩放，阈值不可解释且易被长文档/短查询影响；RRF 只依赖名次，跨查询稳定，便于回归。
- **权重 1.2 / 1.0 是经验取值**：2026-11-08 的 54 条评测上只试了 1.2 / 1.5 / 2.0 三个预设；1.2 让 A 类 MRR 0.8667→0.8867、总 MRR 0.7100→0.7183，同时 A hit@5 不降。但相对 BM25 的 0.7123 只高 0.0060，属于噪声范围，结论是「不低于」不是「优于」。**54 条上调参有过拟合风险，这不是最优解**；语料或 embedding 模型变化后必须重跑。

### 3.6 Embedding 配置与降级矩阵

| 配置 | 默认 | 说明 |
| --- | --- | --- |
| `EMBEDDING_PROVIDER` | `auto` | `auto`：Ollama 可用选 ollama，否则远程有 key 选 openai，否则 none |
| `EMBEDDING_BASE_URL` | `http://localhost:11434` | Ollama 用 base；远程填 OpenAI 兼容 base |
| `EMBEDDING_MODEL` | `bge-m3` | 本地默认；远程可换 |
| `EMBEDDING_DIM` | `1024` | 必须与表一致 |
| `EMBEDDING_BATCH` | `16` | 本地批大小；远程可调 32 |
| `EMBEDDING_TIMEOUT` | `30s` | 单批/单查询超时 |
| `EMBEDDING_PRICE_PER_MTOK` | `0` | 仅远程成本报告用 |
| `RAG_MODE` | `auto` | `auto` → 有 embedding 走 hybrid，否则 bm25 |

| 实际状态 | 触发条件 | API / 页面 / 日志 |
| --- | --- | --- |
| `hybrid` | Ollama/远程可用，查询向量成功 | `/healthz.rag.mode=hybrid`；SSE `retrieval_mode=hybrid` |
| `bm25` | 无 Ollama、无远程 key、模型未拉取 | 启动 `WARN` 一次；`/healthz`、SSE、`/demo` 徽标均显示 `bm25` |
| `bm25_fallback` | 当次 embed 超时/失败/维度不符 | 当次 `WARN` + `rag_embedding_failure_total`；事件 `retrieval_mode=bm25_fallback` |
| `off` | `RAG_MODE=off` 或知识表为空 | 不检索；`/healthz` 显示 `off`/`empty` |

**不允许静默**：`/healthz` 返回 `rag: {mode, chunks, embedded_chunks, last_ingest_at}`；`question.asked` / `report.completed` 事件带 `retrieval_mode` 与 `knowledge_refs` 数量；`/demo` 顶部显示模式徽标；日志统一带 `rag_mode / lexical_hits / vector_hits / fused_hits / duration_ms / top_sources`。

### 3.7 真实落点（不为 RAG 而 RAG）

**落点 A：追问方向参考**

- `QuestionInput` 增加 `References []Reference`；用「当前考点 + 上一轮回答摘要」检索，最多 3 块、总长 1200 字。
- 以 `<reference>` 注入，明确「标准讲解参考，不得原样念给候选人，不得泄露 expected_points」。
- 不改变追问门顺序与上限；无命中/RAG off 时 prompt 与现在逐字一致。

**落点 B：报告标准覆盖**

- 报告 `Collect` 后，按薄弱题 topic 检索，最多 4 块，以 `<standard_coverage>` 注入。
- 用于写「这个考点标准应该覆盖什么」和 `missing_points` 措辞；不替代 `SessionOverview` / `ListWeakTurns` / `SearchTurns`。
- 报告 JSON 增加 `knowledge_refs`（source_path + title）审计；分数仍由公式计算。

**落点 C：自主工具里的 `search_knowledge`**

- 仅在 `REPORT_TOOL_MODE=autonomous` 暴露；参数 `{query, topic?, top_k?}`，返回标题与摘要，不返回全文。

### 3.8 入库 CLI、幂等与实测项

`cmd/ragindex`：

| 命令 | 行为 |
| --- | --- |
| `--dry-run` | 扫描、切块、统计块数/字符数/预估 token 与费用；不调 embedding、不写库 |
| `--apply` | 按 `(source_path, section_key)` upsert；只对新增或 `content_hash` 变化的块 embedding |
| `--rebuild` | 清空后重建；用于换模型/维度 |
| `--prune` | 删除源文件已不存在的块（默认不删） |

- 重复执行幂等：内容不变不重嵌；失败批次不写半成品向量；`rag_ingest_runs` 记录状态与 token。
- 不做启动自动入库；启动只检查表与块数。

**待实测并写进文档的数字**：

- `bge-m3` / `bge-small-zh-v1.5` 单条 embed 延迟（P50/P95，CPU 与 GPU 各一组）。
- 全量入库耗时（100 块、batch=16）。
- 表与索引实际体积（`pg_total_relation_size`）。
- 若启用远程：实际 token 与费用。

### 3.9 检索质量评测（硬验收）

**数据文件**：`internal/agent/knowledge/testdata/retrieval_queries.json`，**54 条**，全部进仓库（`rag-eval-v2`）。

| 分组 | 条数 | 说明 |
| --- | --- | --- |
| A：原文措辞 | 25 | 直接使用文档/题纲原词，测词法基线 |
| B：改写 | 25 | 口语化、同义、指代或跨章节，必须标注 `rewrite`，不得复用文档原句 |
| 无答案 | 4 | 语料里没有的主题，单独校准拒答阈值 |

每条字段：`id`、`group`、`category`、`rewrite`（B 必填）、`query`、`answerable`、`expected`（topic_id 或 source_path+section_prefix）。

**指标定义**

- `hit@5 = 至少一个期望块进入前 5 的 query 数 / 有答案 query 数`（50 条有答案，A/B 各 25）。
- `MRR = mean(1 / 第一个期望块的名次)`；未命中记 0。
- 无答案 4 条单独报 `refusal_rate`（top1 分数 ≤ 阈值时视为拒答）与误拒率。
- **对照组**：同一份切块、同一 top-k=5，只换检索路。
  - BM25-only：只用词法路。
  - Hybrid：BM25 + 向量 + 加权 RRF（k=60，词法 1.2 / 向量 1.0）。
- **规则**：不改 query 集、不针对失败样本调参；若 hybrid 的 hit@5 或 MRR 低于 BM25，照实写进 README/面试问答。

**测试名**

- `TestLoadCommittedQuerySet`
- `TestLoadQuerySetRejectsWrongSplit`
- `TestCalibrateFindsCleanThreshold`
- `TestCalibrateReportsOverlap`
- `TestMatchesExpectationRequiresRealLabel`

**2026-11-08 实测（404 块、top_k=5、bge-m3 预热后）**

| 组 | BM25 hit@5 | BM25 MRR | Hybrid hit@5 | Hybrid MRR |
| --- | --- | --- | --- | --- |
| A（25） | 0.9600 | 0.8667 | 0.9600 | 0.8867 |
| B（25） | 0.6400 | 0.5580 | 0.7200 | 0.5500 |
| 总（50） | 0.8000 | 0.7123 | 0.8400 | 0.7183 |

> **结论边界**：总 MRR 0.7183 vs BM25 0.7123 只差 0.0060（50 条有答案 query，加 4 条无答案共 54 条），在 54 条样本上不显著。按 MRR 平均口径，单条 query 的 reciprocal rank 下降 0.30 就足以抹平差距（rank 1→2 或 1→3 都能反转），因此只能写「hybrid 不低于 BM25，差异在噪声范围内」，**不得写「优于 / 胜出 / 反超」**。

无答案拒答：BM25 4/4、Hybrid 4/4；但最佳单阈值同时误拒 9–10/50 有答案（18%–20%），两类分布重叠，因此不做硬阈值拒答（见 `docs/design-rationale.md` 第 13 节）。

延迟 P50/P95：BM25 小于 1/1 ms，Hybrid 33/43 ms（本地 Ollama，warm）。环境：RTX 4060 / Ultra 5 125H / 31.5 GB。

### 3.10 成本

- 本地：模型磁盘约 **1.25 GB**（bge-m3 + 小模型），内存/显存按模型加载；**无 token 费用**。
- 远程：全量 100 块约 8–12 万 token；按 $0.02/1M 约 **$0.002–0.003**，按 $0.10/1M 约 **$0.008–0.012**；查询约 2–3k token/场。
- `--dry-run` 会按真实切块重算；执行后以 `rag_ingest_runs` 实际值为准。

---

## 4. 工具自主调用

### 4.1 范围

只用于**报告阶段**的只读证据采集；评估链路保持单次模型调用，避免影响 60 条评测确定性。

### 4.2 协议：结构化 JSON

模型每轮只输出一个 JSON：

```json
{"action":"tool","tool":"search_turns","args":{"query":"Redis","limit":5}}
```

或

```json
{"action":"final","report":{ ...现有报告 JSON... }}
```

理由：现有 `llm.Client` 只处理文本；JSON 协议可完整约束、可修复、可单测，任意 OpenAI 兼容服务都能跑，heuristic/测试替身不用改。

### 4.3 工具目录（全部只读）

| 工具 | 参数 | 限额 |
| --- | --- | --- |
| `get_session_overview` | 无 | 1 次 |
| `list_weak_turns` | `limit` 1–10 | 2 次 |
| `search_turns` | `query?`、`topic?`、`min_score?`、`max_score?`、`limit` 1–8 | 4 次 |
| `get_turn` | `turn_id` | 4 次 |
| `search_knowledge` | `query`、`topic?`、`top_k` 1–6 | 3 次 |

所有工具强制会话边界，不能跨用户/跨会话。

### 4.4 三重上限

| 上限 | 默认 | 行为 |
| --- | --- | --- |
| `REPORT_TOOL_MAX_ROUNDS` | 6 | 模型↔工具最多 6 轮；到顶强制 final |
| `REPORT_TOOL_MAX_CALLS` | 12 | 累计调用上限；超出直接拒绝并告知模型 |
| `REPORT_TOOL_TIMEOUT` | 20s | 总预算；超时立即 deterministic fallback |
| 单次工具超时 | 2s | 单查询超时返回错误结果，不阻塞整轮 |
| 单次结果上限 | 6000 字 | 超出截断并标 `truncated=true` |

**共享实现**：deterministic 的 `Collect` 与 autonomous 的 `Execute` 都走 `internal/agent/tools` 的同一组只读工具与同一套校验；`Execute` 只是把参数解析、会话边界、单次超时、per-tool 限额和结果截断统一包成 JSON envelope。deterministic 先执行的 3 次 evidence 调用计入总调用数，因此 autonomous 模式下模型最多再用 9 次（默认 12）。

### 4.5 开关与默认

- `REPORT_TOOL_MODE=deterministic|autonomous`，默认 **deterministic**。
- 理由：60 条评测、无 key 演示、报告幂等与事件序列必须稳定；自主模式是增强能力，不是默认路径。
- `autonomous` 只在真实模型生效；heuristic 自动回落 deterministic 并打 `WARN`。
- 两种模式产出同一 `report.Record` 结构与同一套分数公式；`reports.session_id` 唯一约束不变。

### 4.6 失败路径与测试名（硬验收）

| 情况 | 处理 | 事件 | 测试名 |
| --- | --- | --- | --- |
| 未知工具 | 返回可用工具列表；计一次调用 | `report.tool_denied{reason=unknown_tool}` | `TestToolLoopRejectsUnknownTool` |
| 越权 `turn_id`（不属于本会话） | 拒绝，返回 `not_found` | 同上 | `TestToolLoopRejectsOutOfScopeTurnID` |
| 参数类型/范围非法 | 返回结构化错误；连续 3 次 fallback | `report.tool_denied{reason=invalid_args}` | `TestToolLoopRejectsInvalidArgs` |
| 单次工具超时 | 返回 `timeout=true` 空结果，模型可换工具 | `report.tool_call{status=timeout}` | `TestToolLoopSingleToolTimeout` |
| 超过最大轮次 | 停止工具，要求用已有证据 final | `report.tool_degraded{reason=max_rounds}` | `TestToolLoopMaxRoundsDegrades` |
| 超过最大调用数 | 拒绝后续调用，要求 final | `report.tool_degraded{reason=max_calls}` | `TestToolLoopMaxCallsDegrades` |
| 超过总预算 | 立即 fallback | `report.tool_degraded{reason=budget_exceeded}` | `TestToolLoopTotalBudgetDegrades` |
| 结果截断 | 标 `truncated=true`，模型仍可判断 | `report.tool_call{truncated=true}` | `TestToolLoopTruncatesLargeResult` |
| 工具返回空 | 明确告诉模型「无命中」，不编造 | `report.tool_call{status=empty}` | `TestToolLoopEmptyResultDoesNotFabricate` |
| heuristic 模型 | 自动 deterministic | `report.tool_degraded{reason=heuristic}` | `TestToolLoopHeuristicFallsBack` |
| 事件顺序 | 工具事件在 `report.section` 之前 | — | `TestToolLoopEventOrder` |

**结果截断是否影响模型判断**：用受控小上限（`MaxResultRunes=400`，生产默认 6000）复现同一条截断路径，断言 1) 返回给模型的 envelope 带 `truncated=true` 且仍是合法 JSON；2) 模型 final 仍合法（`TestToolLoopTruncatesLargeResult` 的 outcome 不 degraded、五段齐全）；3) 报告中 `ToolCallsDetail` 记录 `result_bytes` 与截断标记；4) 不因为截断导致 fallback。

### 4.7 事件与审计

- 每轮 `report.tool_round`；每次 `report.tool_call`（tool、status、duration_ms、result_bytes、truncated）；汇总保留 `report.tool_calls`，新增 `tool_mode` 与 `degraded_reason`。
- 报告 `Record` 增加 `ToolMode`、`ToolRounds`、`ToolCallsDetail`。

### 4.8 幂等

工具循环只读；报告提交仍走原 run + `reports.session_id` 唯一约束；不改变状态机、幂等键、事件前后依赖。

### 4.9 可讲的技术要点（工具自主）

| 选择 | 放弃了什么 | 代价 |
| --- | --- | --- |
| **JSON 动作协议，不用原生 function calling** | 放弃 provider 原生 tools 的便利 | 多一层解析/修复；模型可能输出非 JSON，需要降级 |
| **三轮上限（轮次/调用/总时长）** | 放弃「让模型自由查到够」 | 复杂报告可能证据不足；需要 deterministic fallback |
| **默认 deterministic，自主仅 opt-in** | 放弃默认展示自主能力 | 演示默认看不到工具编排；需要显式开关 |
| **截断到 6000 字并显式标记** | 放弃完整结果注入 | 模型可能看不到长尾细节；但 token 可控、可解释 |
| **工具全部只读且强制会话边界** | 放弃「工具能写库/跨会话」 | 能力上限低；换来幂等与安全 |

---

## 5. 分层记忆

### 5.1 三层结构

| 层 | 内容 | 存储 | 调模型 |
| --- | --- | --- | --- |
| 结构化状态 | 岗位、难度、题纲进度、已答题数、分数、最弱维度、待答题 | 现有 PG 表 | 否 |
| 最近 4 轮原文 | 最近 4 轮 Q/A（答案截断 300 字） | `turns` / `evaluations` | 否 |
| 早期摘要 | 更早轮次的覆盖点、薄弱点、上下文 | `session_memories` | 是，按阈值 |

### 5.2 表

```sql
CREATE TABLE session_memories (
    session_id      text PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
    summary         text NOT NULL DEFAULT '',
    upto_seq        int NOT NULL DEFAULT 0,
    covered_turns   int NOT NULL DEFAULT 0,
    prompt_version  text NOT NULL DEFAULT '',
    model           text NOT NULL DEFAULT '',
    prompt_tokens   int NOT NULL DEFAULT 0,
    completion_tokens int NOT NULL DEFAULT 0,
    degraded        boolean NOT NULL DEFAULT false,
    updated_at      timestamptz NOT NULL DEFAULT now()
);
```

迁移 `0014_session_memory.sql`。

### 5.3 触发阈值：按轮数，不按 token

- **阈值**：已答题数 ≥ 6，且 `(answered-1) % 5 == 0`；即第 6、11、16…题各一次，**不每轮调用**。参数为 `MEMORY_SUMMARY_EVERY=5`（默认），最近窗口 `MEMORY_RECENT_TURNS=4`。
- **为什么按轮数而不是 token**：
  - token 数依赖 provider 分词器，换模型阈值就漂移；轮数是稳定、可测的业务单位。
  - 每轮答案已截断到 6000 字（入 prompt 的最近轮次再截到 300 字），5 轮输入上限可算：旧摘要 600 字 + 5×(题 200 + 答 300) ≈ 3100 字；输出 ≤600 字。成本有上界。
  - 按 token 触发需要额外分词与计数，收益只是更省一点 token，不值得引入 provider 依赖。
- **摘要范围**：`[upto_seq+1, answered-4]`，最近 4 轮保留原文。
- **失败**：保留旧摘要，`degraded=true`，下个阈值再试；不每轮重试；摘要失败不阻断作答。
- **刷新**：`prompt_version` 变化时下次阈值重算；会话 aborted/failed 后不再生成。
- **无可用模型**：`MEMORY_MODE=auto` 仍生成摘要，但走确定性 digest（`memory-v1-fallback`），不调用模型；这样无 key 演示也能有第二层。

### 5.4 摘要 prompt 与上限

- 输入：旧摘要 + 新增 Q/A（题干 120 字、答案 200 字、分数、缺失点）。
- 输出 ≤600 字，固定字段：`covered`、`weak`、`notes`。
- 不允许写分数、改判、引用未提供的回答；版本 `memory-v1`。

### 5.5 注入

```go
MemorySummary     string   // ≤600 字
MemoryRecentTurns []string // 最近 4 轮，题 120 字 + 答 300 字
```

- `QuestionUser` 只新增 `<memory>` 块（内含「较早问答摘要」与「最近问答」），原有 `topic / objective / expected_points / history` 顺序不变。
- 记忆块由 builder 先限长：摘要 ≤600 字，每轮 ≤420 字，最多 4 轮；`QuestionUser` 再做一次同口径截断。
- 记忆在 snapshot 阶段读入并冻结进 `run_steps`，重放看到的是同一份上下文。
- 不改状态机、不改 `Answer` 幂等键；记忆读取失败等同空记忆；摘要在答案提交后 best-effort 触发，失败只记日志。

### 5.6 验证状态：已实现、未验证（2026-11-08）

**已实现**：`memory.Builder` 接入 `app.Runner`；snapshot 读入摘要 + 最近 4 轮并冻结进 `run_steps`；答案提交后按 5 轮阈值 best-effort 摘要；`QuestionUser` 注入 `<memory>` 块；摘要失败只记日志、不阻断作答。

**已验证（单测，确定性）**：

- `TestQuestionPromptCarriesLayeredMemory`：摘要与最近轮次进入 prompt 的 `<memory>` 块。
- `TestAnswerSnapshotCarriesLayeredMemory`：第 2 次作答的 snapshot 带摘要 + 最近 1 轮原文。
- `TestMemoryFailureDoesNotFailAnswerRun`：记忆存储失败时作答仍成功、面试继续。

**未验证**：真实模型下的多轮事实保留率与 token 对比。按当前排期优先级不做该实验；没有本地可用的对话模型时也不以确定性替身冒充真实结论。若要补，用例固定为：第 1 轮埋「PostgreSQL 唯一事实源、Redis 只做锁与限流」，跑到第 10 轮，断言第 10 轮 prompt 的 `<memory>` 仍包含该事实；开/关记忆各跑一次，比较引用正确率与 prompt token。届时须记录模型名、轮次、prompt token。

### 5.7 可讲的技术要点（分层记忆）

| 选择 | 放弃了什么 | 代价 |
| --- | --- | --- |
| **按轮数阈值（每 5 轮）而不是 token** | 放弃更精细的 token 预算 | 可能多花一点摘要 token；换来跨 provider 稳定 |
| **最近 4 轮原文 + 早期摘要** | 放弃全量对话注入 | 早期细节可能丢；用固定事实保留测试兜底 |
| **摘要只做派生缓存** | 放弃「记忆即事实源」 | 摘要不能用于恢复/判定；只是 prompt 上下文 |
| **摘要失败保留旧值** | 放弃「失败即重试到成功」 | 短期记忆可能滞后；不会阻塞作答 |
| **无 key 只走最近轮次** | 放弃无 key 时的长期摘要 | 长会话早期事实会丢；但产品仍完整可跑 |

---

## 6. 对现有确定性的影响

| 能力 | 影响范围 | 改分数 | 默认 |
| --- | --- | --- | --- |
| RAG | 追问题干、报告点评、`search_knowledge` | 否 | `auto`（无 key → bm25） |
| 工具自主 | 报告证据采集 | 否 | `deterministic` |
| 分层记忆 | 出题/追问 prompt | 否 | `auto`（无可用模型 → 确定性 digest） |

- 评估器、`rubric.go`、60 条回归集、`DefaultThresholds`、证据锚点零改动。
- 报告 fallback、追问 10 级门、状态机、幂等三重保障零改动。
- 三个 kill switch 关闭后，prompt 与当前版本逐字一致。

---

## 7. 迁移与目录

| 编号 | 内容 |
| --- | --- |
| `0013_rag.sql` | `CREATE EXTENSION vector`、`knowledge_chunks`、HNSW 索引、`rag_ingest_runs` |
| `0014_session_memory.sql` | `session_memories` |

`deploy/docker-compose.yml` 的 PostgreSQL 改为 `pgvector/pgvector:pg16`（基于 PG16，数据卷不变；启动执行 `CREATE EXTENSION`）。

```text
cmd/ragindex/                  # 离线入库 CLI（--dry-run / --apply / --rebuild / --prune）
cmd/memorycheck/               # 可选：真实模型多轮一致性脚本
internal/agent/rag/            # 切块、BM25、embedding 客户端、RRF、检索服务、检索评测
internal/agent/knowledge/      # 检索结果 DTO 与 prompt 片段
internal/agent/agentloop/      # 报告自主工具循环（协议、预算、降级）
internal/agent/tools/          # 新增 SearchKnowledge
internal/agent/prompt/         # QuestionInput / ReportInput 扩展
internal/app/                  # 记忆组装、工具模式选择、检索编排
internal/domain/memory/        # 摘要 DTO、阈值、截断（纯函数）
internal/store/postgres/       # knowledge / memory 仓库
internal/platform/config/      # RAG / embedding / tool / memory 配置
migrations/0013_rag.sql
migrations/0014_session_memory.sql
deploy/docker-compose.yml
```

---

## 8. 验证与数字落盘

### 8.1 不退化验证

- 现有 60 条回归集跑一次真实模型，配置与 `eval-v4` 一致；评估链路不接 RAG/工具/记忆。
- 通过线：档位 ≥ 48/60（80.0%），MAE ≤ 0.4733，schema/相邻档/证据 100%；四命令全绿。
- 任一退化 → 停手定位；不改门槛或期望值。
- 真实跑分报告实际 token；本轮 embedding 全量入库先 `--dry-run` 报预估。

### 8.2 新增单测

| 模块 | 测试名 |
| --- | --- |
| RAG 检索 | `TestRetrievalQuerySetSchema`、`TestRetrievalQualityBM25Only`、`TestRetrievalQualityHybrid`、`TestRRFFusionStable`、`TestRetrievalNoAnswerFalsePositive` |
| RAG 入库 | `TestIngestIdempotent`、`TestIngestChangedChunkOnly`、`TestIngestDryRunNoWrite`、`TestIngestFailureNoPartialVectors` |
| 工具自主 | 4.6 表中的 11 个测试名 |
| 记忆 | 5.6 表中的 7 个测试名 |
| 兼容 | `TestRAGOffPromptParity`、`TestDeterministicReportPromptParity`、`TestMemoryOffPromptParity` |

### 8.3 命令

```powershell
gofmt -l .
go vet ./...
go test ./...
go test -tags=eval ./internal/agent/eval
```

### 8.4 数字同步

实现完成后，把下列数字表原样写入 `README.md` 与 `docs/interview-qa.md` 的 M20 模块，并标注测量方式与环境：

- 检索：hit@5、MRR、无答案误召回@5，BM25-only vs Hybrid。
- Embedding：模型、维度、体积、单条 P50/P95、全量入库耗时、表/索引体积。
- 工具：最大轮次/调用/预算、截断阈值、失败测试名。
- 记忆：阈值 5 轮、最近 4 轮、摘要输入/输出上限、事实保留率。
- 成本：本地零 token 费用；远程实际 token 与费用（若启用）。

---

## 9. 落地顺序（每块单独汇报）

1. **环境与 RAG 基础设施**：安装确认 → Ollama/模型 → 迁移、compose、切块、BM25、embedding 客户端、`cmd/ragindex`、单测；先 `--dry-run` 报预估。
2. **RAG 接入**：追问 `<reference>`、报告 `<standard_coverage>`、`/healthz`/事件/徽标、指标与日志。
3. **检索质量评测**：24 条 query 集、BM25-only 与 hybrid 的 hit@5/MRR、失败样本分析。
4. **工具自主**：JSON 协议、预算、失败测试、`search_knowledge`、开关；默认 deterministic。
5. **分层记忆**：表、阈值、摘要、注入、多轮一致性测试、`cmd/memorycheck`。
6. **验证与文档**：60 条回归、四命令、README/development-plan/design-rationale/walkthrough/interview-qa 同步。

---

## 10. 需要确认的决策

1. **是否允许安装 Ollama 并下载模型**：安装包约 1.47 GB、需预留 4 GB；`bge-m3` 1.2 GB；小模型 48 MB。不确认则默认 BM25-only，hybrid 数字标注“环境不可用”。
2. **默认模式**：RAG `auto`（有本地模型走 hybrid，否则 bm25）、报告工具 `deterministic`、记忆 `auto`（有真实模型才摘要）。是否同意？
3. **pgvector 镜像**：compose 换 `pgvector/pgvector:pg16`，数据卷不变；需要重建本地容器但不用清库。是否接受？
4. **摘要频率**：每 5 个新答题轮一次（第 6/11/16…题），最近 4 轮原文。是否接受？
5. **自主工具默认关闭**：演示/评测默认 deterministic；现场展示时临时 `REPORT_TOOL_MODE=autonomous`。是否同意？
6. **小模型对照**：用 `quentinz/bge-small-zh-v1.5`（第三方 namespace，48 MB）作为 bge-m3 的对照；若你更信任官方模型，可改用其他 ≤200 MB 中文 embedding。是否接受？

> 参考：[Ollama Windows 文档](https://docs.ollama.com/windows)、[bge-m3 模型页](https://ollama.com/library/bge-m3)、[bge-small-zh-v1.5 模型页](https://ollama.com/quentinz/bge-small-zh-v1.5)。

---

## 11. M20 三臂消融（2026-11-08）

**目的**：用同一批题、同一模型比较「单次 LLM」「LLM+RAG」「当前完整方案（规则裁决 + 证据校验）」，看多花的 token 换来了什么。

**设置**：

- 数据：`internal/agent/eval/testdata/regression.json`，sha256 写入结果文件；子集 10 条（Go high-05/09/01/02、AI high-01/03、mid-01、ai-mid-03、low-01、ai-low-01）。
- 模型：本地 Ollama `qwen2.5:1.5b`，OpenAI 兼容端点 `http://127.0.0.1:11434/v1`，`temperature=0`（完整臂沿用生产 evaluator 的 0.1）。
- 调用：3 臂 × 10 题 = 30 次；实际花费 ¥0。
- 三条臂的差别：
  - 单次 LLM：只给题目 + 回答，直接让模型给 0-100 总分；band 用统一阈值映射。
  - LLM+RAG：同一 prompt 再加 BM25 top3 参考块。
  - 完整方案：维度分 → `ComputeScore` 确定性算分，输出过 schema 与证据校验。

**结果**（`docs/eval-results/ablation-m20.json`）：

| 臂 | band 命中 | MAE | 平均 prompt token | 平均 completion token | 平均耗时 |
| --- | --- | --- | --- | --- | --- |
| 单次 LLM | 50.0%（5/10） | 21.40 | 287.4 | 9.0 | 162 ms |
| LLM+RAG | 60.0%（6/10） | 20.10 | 959.5 | 8.9 | 155 ms |
| 完整方案 | 30.0%（3/10） | 18.50 | 1,247.4 | 470.7 | 2,941 ms |

**读法与限制**：

- RAG 臂比单次多约 672 prompt token，band +1 条、MAE −1.3；10 条样本上不能当显著结论。
- 完整臂 MAE 最好，但 band 命中最低：1.5b 的维度分整体偏低，high 档落在 60 分附近，再映射到 5 档就被放大；这不是「完整方案更差」，而是「小模型的维度打分不可信 + band 粒度粗」。
- token 与耗时差异是稳定的：完整臂 prompt 约为单次的 4.3 倍、耗时约 18 倍。
- 复现命令（PowerShell）：

```powershell
$env:MODEL_PROVIDER='openai'; $env:OPENAI_BASE_URL='http://127.0.0.1:11434/v1';
$env:OPENAI_API_KEY='ollama'; $env:OPENAI_MODEL='qwen2.5:1.5b'; $env:MODEL_TIMEOUT='120s';
go run ./cmd/ablation
```

## 12. run 级 token 与成本落库

**迁移**：`0015_run_usage.sql` 给 `run_steps` 与 `runs` 加 `prompt_tokens / completion_tokens / model_calls / cost_micros`。

**归属方式**：`app.runner` 在每个节点执行前后各取一次 `obs.UsageTracker.Snapshot(session_id)`，差值记到该 `run_steps` 行并累加到 `runs`；重放命中已完成步骤时差值为 0，不写库。这样不用把 step 名塞进 llm client，也不会把并发 run 的用量串行记混（同一会话的 run 本身串行）。

**成本**：`cost_micros = prompt_tokens × MODEL_PRICE_INPUT_CNY_PER_MTOK + completion_tokens × MODEL_PRICE_OUTPUT_CNY_PER_MTOK`（单位微元）；未配置价格时为 0，本地模型天然为 0。`trace_id` 已在 `runs.trace_id`，`runDTO` 也一并返回，日志、节点、模型调用可用同一个 trace 串起来。

**直接回答「这一轮多少钱、花在哪一步」**：

```sql
SELECT step_name, prompt_tokens, completion_tokens, model_calls,
       cost_micros / 1000000.0 AS cost_cny
FROM run_steps WHERE run_id = $1 ORDER BY started_at;

SELECT trace_id, prompt_tokens, completion_tokens, cost_micros / 1000000.0 AS cost_cny
FROM runs WHERE id = $1;
```

**测试**：`TestRunUsageIsPersistedPerStep` 用 1,000/200 token、2/8 元每百万的价格断言 run 与 step 都是 3,600 微元，且 `model_calls=1`。

## 13. 原生 Function Calling 与 JSON 动作协议

**选择**：保留现有的 JSON 动作协议（`{"action":"tool","tool":...}` / `{"action":"final","report":...}`），不新增原生 function-call 路径。

**为什么**：

- **可移植**：协议只要求模型输出 JSON，DeepSeek、任意 OpenAI 兼容端点、本地 Ollama 都能跑；原生 tool schema 各家的字段、并行调用、流式增量格式并不一致，换 provider 要重写调用层。
- **校验在应用层**：工具白名单、参数形状、单轮/总调用上限、越权 `turn_id` 拒绝、结果截断都由代码判定；原生 FC 的参数仍要校验，等于多了一套 provider 适配而没有减少校验责任。
- **可测**：JSON 动作是纯文本输入输出，失败路径（非法 JSON、未知工具、超预算、超时）全部能用脚本客户端复现；原生路径要模拟不同 provider 的 tool-call 结构。
- **开销可接受**：现有实测每轮动作 JSON 只花 16–19 个 completion token（见 `tool-mode-compare-v2.json` 的 `round_traces`），相对一次报告 1,330 token 不是瓶颈。

**代价与边界**：

- 原生 FC 的优势场景是「多工具并行、低时延、模型原生决定调用顺序」。当前只有 3 个只读工具、一次会话通常 2 次调用，串行 JSON 足够；若工具数增长到 6+、需要并行检索，或 provider 提供稳定的原生并行调用，应该补一条 FC 路径做 A/B，而不是继续加 JSON 轮次。
- 原生 FC 也无法替代证据校验：模型仍可能调用不存在的工具或传错参数，拦截逻辑必须保留。
- 面试表述：不是「不会 FC」，而是「当前规模下自研协议更可移植、更可测；FC 的收益在并行多工具，等工具数量或时延成为瓶颈再切换」。

## 14. 分层记忆：面试三问

**分了几层、各存什么**：

1. 结构化事实层（PostgreSQL）：`sessions` / `turns` / `evaluations` / `scores`，存题目、答案、维度分、状态；这是事实源，不压缩、不丢。
2. 摘要层（`session_memories`，迁移 `0014`）：较早轮次的压缩摘要，≤600 字，每 5 个新答题轮重算一次（第 6/11/16…题），存 `covered_turns`、`prompt_version`、`degraded`。
3. 最近原文层（prompt 内，不落库）：最近 4 个已答题轮，题 200 字 / 答 300 字截断，进 `<memory>` 块。

**怎么防止上下文无限膨胀**：

- 只保留最近 4 轮原文；更早的进摘要，摘要每次**替换**而不是追加；
- 摘要 ≤600 字、每轮 ≤420 字、记忆块总长有上界（约 2,300 字）；
- 摘要按轮数触发，不按每轮触发，失败保留旧摘要、`degraded=true`；
- snapshot 冻结进 `run_steps`，重放不会重新膨胀。

**关掉记忆后差多少**：**未验证，没有数字**。已实现并有确定性单测（摘要/最近轮次进入 prompt、存储失败不阻断作答），但真实模型下的多轮事实保留率与 token 对比按当前排期没有跑；不使用确定性替身冒充真实结论。面试时只能说「机制已接通、单测覆盖注入与降级；效果待真实模型多轮实验」，不能说「提升 X%」。
