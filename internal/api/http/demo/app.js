(function () {
  "use strict";

  const $ = (id) => document.getElementById(id);
  // Fallback rubric for sample mode and before a session is created. Real
  // sessions replace it with the domain pack rubric returned by the server.
  const DEFAULT_RUBRIC = {
    dimensions: [
      { key: "correctness", name: "正确性", weight: 0.30 },
      { key: "depth", name: "深度", weight: 0.25 },
      { key: "engineering_judgment", name: "工程判断", weight: 0.20 },
      { key: "structure", name: "表达结构", weight: 0.15 },
      { key: "evidence_credibility", name: "证据可信度", weight: 0.10 },
    ],
  };
  const BONUS_CAP = 8;
  const PENALTY_CAP = -10;

  function rubricDims() {
    const dims = state.rubric && state.rubric.dimensions;
    return Array.isArray(dims) && dims.length > 0 ? dims : DEFAULT_RUBRIC.dimensions;
  }

  function rubricPairs() {
    return rubricDims().map((d) => [d.key, d.name]);
  }

  function dimLabel(key) {
    const dim = rubricDims().find((d) => d.key === key);
    return dim ? dim.name : key;
  }

  function dimWeight(key) {
    const dim = rubricDims().find((d) => d.key === key);
    return dim ? Number(dim.weight) : 0;
  }
  const EVENT_TYPES = [
    "session.created", "session.started", "session.phase", "session.aborted", "session.completed",
    "session.failed", "question.asked", "answer.received", "evaluation.completed",
    "evaluation.degraded", "decision.made", "run.started", "run.succeeded", "run.failed",
    "run.interrupted", "report.started", "report.section", "report.tool_calls", "report.completed",
  ];

  // ---------- built-in sample data ----------
  // Deterministic, hand-written data for screenshots and the "sample=1" mode.
  // It never touches the API and never calls a model. Scores follow the same
  // arithmetic as the real evaluator: base = Σ(维度分/5 × 权重) × 100,
  // total = base + bonus + penalty.
  const SAMPLE_QUESTIONS = [
    {
      turn_id: "sample-turn-1",
      seq: 1,
      topic: "订单幂等与最终一致性",
      kind: "primary",
      depth: 1,
      question: "你在简历里提到用本地消息表保证订单与库存的最终一致性。请讲讲这条链路的完整设计，以及如何避免重复扣减？",
      score: 82,
      scores: { correctness: 5, depth: 4, engineering_judgment: 4, structure: 3, evidence_credibility: 3 },
      base_score: 81,
      bonus: [{ type: "量化结果", quote: "P99 从 320ms 降到 180ms，重复扣减率从 0.8% 降到 0.02%", points: 1, reason: "有上线前后的指标对比" }],
      penalty: [],
      comment: "链路讲得完整，幂等键与本地消息表的关系清楚；但补偿失败后的告警与人工介入流程只提了一句。",
      strong_points: ["幂等键设计明确", "能解释最终一致性窗口"],
      missing_points: ["补偿失败后的兜底流程", "消息堆积时的限流策略"],
      evidence: [
        { turn_id: "sample-turn-1", dimension: "correctness", quote: "用订单号 + 业务类型做唯一幂等键，先落本地消息表再投递 MQ", reason: "核心机制表述准确" },
        { turn_id: "sample-turn-1", dimension: "evidence_credibility", quote: "上线后重复扣减率从 0.8% 降到 0.02%", reason: "有线上指标支撑" },
      ],
    },
    {
      turn_id: "sample-turn-2",
      seq: 2,
      topic: "缓存与数据库一致性",
      kind: "primary",
      depth: 1,
      question: "库存查询接入 Redis 缓存后，如何保证缓存与数据库的一致性？请对比你考虑过的几种方案。",
      score: 68,
      scores: { correctness: 4, depth: 3, engineering_judgment: 3, structure: 3, evidence_credibility: 3 },
      base_score: 66,
      bonus: [{ type: "方案对比", quote: "对比了先删缓存再更新库、延迟双删与订阅 binlog 三种方案", points: 2, reason: "主动给出取舍" }],
      penalty: [],
      comment: "方案对比有意识，但删除缓存失败后的重试与并发读写窗口没有讲清，深度不足。",
      strong_points: ["能列出三种常见方案"],
      missing_points: ["删除失败的重试机制", "并发读写下的时序分析"],
      evidence: [
        { turn_id: "sample-turn-2", dimension: "engineering_judgment", quote: "延迟双删对业务侵入小，但一致性窗口依赖延迟估计", reason: "指出了方案代价" },
        { turn_id: "sample-turn-2", dimension: "depth", quote: "binlog 订阅可以做到准实时，但组件成本更高", reason: "有成本视角" },
      ],
    },
    {
      turn_id: "sample-turn-3",
      seq: 3,
      topic: "线上超时排查与可观测性",
      kind: "primary",
      depth: 1,
      question: "如果线上订单链路突然出现大量超时，你会按什么顺序排查？如何用指标定位到具体环节？",
      score: 74,
      scores: { correctness: 4, depth: 4, engineering_judgment: 4, structure: 2, evidence_credibility: 4 },
      base_score: 74,
      bonus: [],
      penalty: [],
      comment: "排查顺序基本正确，指标也说得具体；但回答发散，先讲了三天前的故障复盘才回到方法，结论不够靠前。",
      strong_points: ["排查顺序合理", "能给出关键指标"],
      missing_points: ["缺少结论先行的表达结构"],
      evidence: [
        { turn_id: "sample-turn-3", dimension: "correctness", quote: "先看入口 QPS 与错误率，再按网关、订单、库存逐层下钻", reason: "排查路径可执行" },
        { turn_id: "sample-turn-3", dimension: "structure", quote: "先讲了一次历史故障的来龙去脉，两分钟后才给出排查顺序", reason: "结论没有先行" },
      ],
    },
  ];

  const SAMPLE_REPORT = {
    schema_version: 1,
    session_id: "sample-demo",
    generated_at: "2026-11-06T12:40:00Z",
    degraded: false,
    overall_score: 74.7,
    plan_items: 3,
    score_band: "良好",
    verdict: "本次完成 3 道主问题，综合 74.7 分。正确性稳定（86.7），表达结构（53.3）与证据可信度（66.7）是主要短板：第 3 题排查思路正确，但结论没有先行；第 2 题的方案对比缺少失败分支与量化验证。",
    dimensions: [
      { dimension: "correctness", score: 86.7, weight: 0.3, comment: "三次作答结论都正确，核心机制表述准确。" },
      { dimension: "depth", score: 73.3, weight: 0.25, comment: "能讲到实现细节，但异常分支与边界条件展开不足。" },
      { dimension: "engineering_judgment", score: 73.3, weight: 0.2, comment: "有方案对比意识，成本与回滚常被略过。" },
      { dimension: "structure", score: 53.3, weight: 0.15, comment: "第 3 题发散，缺少「结论先行」的表达结构。" },
      { dimension: "evidence_credibility", score: 66.7, weight: 0.1, comment: "部分判断有线上数据支撑，个别仍是主观经验。" },
    ],
    questions: SAMPLE_QUESTIONS,
    improvements: [
      "回答先给结论，再展开依据，避免用故障复盘开场",
      "补上补偿失败、删缓存失败等异常分支的兜底设计",
      "为性能优化补齐压测方法与回滚方案",
    ],
    next_steps: [
      "用「结论-依据-取舍-数据」四段式重答第 3 题并录音",
      "整理一份缓存一致性方案对比表，标注每种方案的失败处理",
      "准备一次线上超时排查演练，明确每一步看哪些指标",
    ],
    sections: [
      {
        index: 0,
        title: "总评",
        markdown: "**总评：良好（74.7 分）**\n\n本次完成 3 道主问题。正确性稳定（86.7），深度与工程判断在 73 分左右；表达结构只有 53.3，是拉开分差的主要维度。第 2、3 题都把结论埋在了背景叙述后面，导致信息密度下降。",
      },
      {
        index: 1,
        title: "分维度表现",
        markdown: "| 维度 | 得分 | 说明 |\n| --- | --- | --- |\n| 正确性 | 86.7 | 结论准确，机制表述完整 |\n| 深度 | 73.3 | 有实现细节，异常分支不足 |\n| 工程判断 | 73.3 | 能对比方案，缺少成本量化 |\n| 表达结构 | 53.3 | 结论后置，叙述发散 |\n| 证据可信度 | 66.7 | 有数据，也有主观经验 |",
      },
      {
        index: 2,
        title: "逐题点评",
        markdown: "三道题的得分分别为 82.0、68.0、74.0，逐题卡片刻在下方。",
      },
      {
        index: 3,
        title: "最需要补的 3 个点",
        markdown: "1. 回答先给结论，再展开依据，避免用故障复盘开场\n2. 补上补偿失败、删缓存失败等异常分支的兜底设计\n3. 为性能优化补齐压测方法与回滚方案",
      },
      {
        index: 4,
        title: "下一步练习建议",
        markdown: "1. 用「结论-依据-取舍-数据」四段式重答第 3 题并录音\n2. 整理一份缓存一致性方案对比表，标注每种方案的失败处理\n3. 准备一次线上超时排查演练，明确每一步看哪些指标",
      },
    ],
  };


  const state = {
    sessionId: null,
    pendingTurn: null,
    seen: new Set(),
    es: null,
    busy: false,
    streamClosed: false,
    reportLoaded: false,
    canFinish: false,
    config: null,
    rubric: null,
    role: "",
    startedAt: null,
    primaryAsked: 0,
    followupsUsed: 0,
    timer: null,
    lastEvalCard: null,
    eventCount: 0,
    historyCount: 0,
    reportSectionsOpen: {},
    reviewOpen: {},
    reportCost: null,
    sampleMode: false,
    // M13 staged progress. topics/followups are indexed by plan item (0-based).
    stepper: { total: 0, done: 0, current: 0, topics: [], followups: [], lastCurrent: 0 },
  };
  let stepperSig = "";

  const noticesEl = $("notices");
  const emptyState = $("empty-state");
  const currentQuestionEl = $("current-question");
  const resultSlotEl = $("result-slot");
  const answerBox = $("answer-box");
  const answerInput = $("answer-input");
  const submitBtn = $("submit-answer");
  const finishBar = $("finish-bar");
  const finishBtn = $("finish-btn");
  const historyBlock = $("history-block");
  const historyBody = $("history-body");
  const historyCountEl = $("history-count");
  const eventDetails = $("event-details");
  const eventBody = $("event-body");
  const eventCountEl = $("event-count");
  const reportPanel = $("report-panel");
  const reportStatus = $("report-status");
  const reportMeta = $("report-meta");
  const reportCharts = $("report-charts");
  const reportSections = $("report-sections");
  const runInfo = $("run-info");
  const runInfoBody = $("run-info-body");
  const stepperEl = $("stepper");
  const stepperTrack = $("stepper-track");
  const stepperCompact = $("stepper-compact");
  const stepperHint = $("stepper-hint");
  const reviewPanel = $("review-panel");
  const reviewBody = $("review-body");
  const reviewStatus = $("review-status");
  const reviewEntry = $("review-entry");
  const reviewDueCount = $("review-due-count");

  let uid = 0;

  // ---------- generic helpers ----------

  function sleep(ms) {
    return new Promise((resolve) => setTimeout(resolve, ms));
  }

  function escapeHtml(value) {
    return String(value == null ? "" : value)
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;")
      .replace(/'/g, "&#39;");
  }

  function truncateText(value, max) {
    const chars = Array.from(String(value == null ? "" : value));
    return chars.length > max ? chars.slice(0, max).join("") + "…" : chars.join("");
  }

  function inline(text) {
    return escapeHtml(text)
      .replace(/\*\*(.+?)\*\*/g, "<strong>$1</strong>")
      .replace(/`([^`]+)`/g, "<code>$1</code>");
  }

  function isSpecial(line) {
    return /^(#{2,3}\s|\s*>\s?|\s*[-*]\s+|\d+\.\s+|\s*\|)/.test(line);
  }

  function renderTable(rows) {
    const body = rows.filter((row) => !/^\s*\|[\s:|-]+\|\s*$/.test(row));
    if (body.length === 0) {
      return "";
    }
    const cells = (row) => row.trim().replace(/^\|/, "").replace(/\|$/, "").split("|").map((c) => c.trim());
    const head = cells(body[0]).map((c) => `<th>${inline(c)}</th>`).join("");
    const rest = body.slice(1).map((row) => {
      return `<tr>${cells(row).map((c) => `<td>${inline(c)}</td>`).join("")}</tr>`;
    }).join("");
    return `<table><thead><tr>${head}</tr></thead><tbody>${rest}</tbody></table>`;
  }

  // Small Markdown subset: headings, bold, code, lists, quotes and tables.
  function renderMarkdown(markdown) {
    const lines = String(markdown || "").replace(/\r/g, "").split("\n");
    const out = [];
    let i = 0;
    while (i < lines.length) {
      const line = lines[i];

      if (/^\s*\|/.test(line)) {
        const rows = [];
        while (i < lines.length && /^\s*\|/.test(lines[i])) {
          rows.push(lines[i]);
          i++;
        }
        out.push(renderTable(rows));
        continue;
      }
      if (/^###\s+/.test(line)) {
        out.push(`<h4>${inline(line.replace(/^###\s+/, ""))}</h4>`);
        i++;
        continue;
      }
      if (/^##\s+/.test(line)) {
        out.push(`<h3>${inline(line.replace(/^##\s+/, ""))}</h3>`);
        i++;
        continue;
      }
      if (/^>\s?/.test(line)) {
        const parts = [];
        while (i < lines.length && /^>\s?/.test(lines[i])) {
          parts.push(inline(lines[i].replace(/^>\s?/, "")));
          i++;
        }
        out.push(`<blockquote>${parts.join("<br>")}</blockquote>`);
        continue;
      }
      if (/^\s*[-*]\s+/.test(line)) {
        const items = [];
        while (i < lines.length && /^\s*[-*]\s+/.test(lines[i])) {
          items.push(`<li>${inline(lines[i].replace(/^\s*[-*]\s+/, ""))}</li>`);
          i++;
        }
        out.push(`<ul>${items.join("")}</ul>`);
        continue;
      }
      if (/^\d+\.\s+/.test(line)) {
        const items = [];
        while (i < lines.length && /^\d+\.\s+/.test(lines[i])) {
          items.push(`<li>${inline(lines[i].replace(/^\d+\.\s+/, ""))}</li>`);
          i++;
        }
        out.push(`<ol>${items.join("")}</ol>`);
        continue;
      }
      if (line.trim() === "") {
        i++;
        continue;
      }

      const parts = [];
      while (i < lines.length && lines[i].trim() !== "" && !isSpecial(lines[i])) {
        parts.push(inline(lines[i]));
        i++;
      }
      out.push(`<p>${parts.join("<br>")}</p>`);
    }
    return out.join("");
  }

  function card(kind) {
    const el = document.createElement("article");
    el.className = "card " + kind;
    return el;
  }

  function cardHead(title, chips) {
    const head = document.createElement("div");
    head.className = "card-head";
    const h = document.createElement("h3");
    h.className = "card-title";
    h.textContent = title;
    head.appendChild(h);
    (chips || []).forEach((chip) => {
      const c = document.createElement("span");
      c.className = "chip " + (chip.kind || "");
      c.textContent = chip.text;
      head.appendChild(c);
    });
    return head;
  }

  function hideEmptyState() {
    if (emptyState) {
      emptyState.hidden = true;
    }
  }

  // 诊断 / JD 结果固定显示在主面板，同一时间只展示一类结果。
  function showAnalysisResult(kind) {
    $("diagnose-result").hidden = kind !== "diagnosis";
    $("jd-result").hidden = kind !== "jd";
    hideEmptyState();
  }

  function hideAnalysisResults() {
    $("diagnose-result").hidden = true;
    $("jd-result").hidden = true;
  }

  function refreshAnalysisEmptyState() {
    if (state.sessionId || state.sampleMode) {
      return;
    }
    if ($("diagnose-result").hidden && $("jd-result").hidden) {
      emptyState.hidden = false;
    }
  }

  function appendEventCard(el) {
    eventDetails.hidden = false;
    eventBody.appendChild(el);
    state.eventCount += 1;
    eventCountEl.textContent = String(state.eventCount);
  }

  function appendResult(el) {
    resultSlotEl.appendChild(el);
    refreshClamps();
  }

  function clampHtml(text, lines) {
    const raw = String(text == null ? "" : text);
    const id = `clamp-${++uid}`;
    return `<div class="clamp" style="--clamp-lines:${lines || 3}" id="${id}">${escapeHtml(raw)}</div>` +
      (raw.length > 40 ? `<button type="button" class="expand-btn" data-expand="${id}" hidden>展开全文</button>` : "");
  }

  // Reveal "展开全文" only when the clamped block actually overflows. Closed
  // <details> report zero height, so this also runs on every toggle.
  function refreshClamps() {
    requestAnimationFrame(() => {
      document.querySelectorAll(".clamp:not(.is-expanded)").forEach((el) => {
        const btn = document.querySelector(`[data-expand="${el.id}"]`);
        if (btn && el.scrollHeight > el.clientHeight + 2) {
          btn.hidden = false;
        }
      });
    });
  }

  function archiveFocus() {
    const hasQuestion = currentQuestionEl.firstChild;
    const hasResult = resultSlotEl.firstChild;
    if (!hasQuestion && !hasResult) {
      return;
    }
    const wrap = document.createElement("div");
    wrap.className = "history-item";
    while (currentQuestionEl.firstChild) {
      wrap.appendChild(currentQuestionEl.firstChild);
    }
    while (resultSlotEl.firstChild) {
      wrap.appendChild(resultSlotEl.firstChild);
    }
    historyBody.appendChild(wrap);
    historyBlock.hidden = false;
    state.historyCount += 1;
    historyCountEl.textContent = String(state.historyCount);
    currentQuestionEl.hidden = true;
    currentQuestionEl.innerHTML = "";
    resultSlotEl.innerHTML = "";
  }

  function showNotice(kind, text, options) {
    const opts = options || {};
    if (opts.key) {
      noticesEl.querySelectorAll(`.notice[data-key="${opts.key}"]`).forEach((n) => n.remove());
    }
    const box = document.createElement("div");
    box.className = "notice " + kind;
    if (opts.key) {
      box.dataset.key = opts.key;
    }
    const body = document.createElement("div");
    body.className = "body";
    body.innerHTML = text;
    box.appendChild(body);
    if (opts.actionLabel && opts.onAction) {
      const btn = document.createElement("button");
      btn.type = "button";
      btn.textContent = opts.actionLabel;
      btn.addEventListener("click", opts.onAction);
      box.appendChild(btn);
    }
    noticesEl.appendChild(box);
    if (opts.timeout) {
      setTimeout(() => box.remove(), opts.timeout);
    }
    return box;
  }

  function setStatus(text, kind) {
    const badge = $("status-badge");
    badge.textContent = text;
    badge.className = "badge " + (kind || "idle");
  }

  function setSSE(text, kind) {
    const badge = $("sse-badge");
    badge.textContent = text;
    badge.className = "pill " + (kind || "idle");
  }

  function phaseLabel(phase) {
    switch (phase) {
      case "awaiting_answer": return "等待作答";
      case "ready_to_ask": return "准备出题";
      case "ready_to_finalize": return "题纲完成，可收尾";
      default: return phase || "";
    }
  }

  function statusLabel(status) {
    switch (status) {
      case "created": return "已创建";
      case "active": return "面试进行中";
      case "reporting": return "正在生成报告";
      case "completed": return "面试已完成";
      case "aborted": return "已中止";
      case "failed": return "失败";
      default: return status || "未开始";
    }
  }

  function bandLabel(score) {
    if (score >= 85) return "优秀";
    if (score >= 70) return "良好";
    if (score >= 60) return "及格";
    return "待提升";
  }

  function formatDuration(totalSeconds) {
    const seconds = Math.max(0, Math.floor(totalSeconds));
    const h = Math.floor(seconds / 3600);
    const m = Math.floor((seconds % 3600) / 60);
    const s = seconds % 60;
    const pad = (n) => String(n).padStart(2, "0");
    return h > 0 ? `${pad(h)}:${pad(m)}:${pad(s)}` : `${pad(m)}:${pad(s)}`;
  }

  function setProgress() {
    const cfg = state.config || {};
    $("prog-question").textContent = state.primaryAsked;
    $("prog-total").textContent = cfg.max_primary_questions || 0;
    $("prog-followups").textContent = state.followupsUsed;
    $("prog-followup-limit").textContent = cfg.max_total_followups || 0;
    if (state.startedAt) {
      $("prog-elapsed").textContent = formatDuration((Date.now() - new Date(state.startedAt).getTime()) / 1000);
    }
    renderStepper();
  }

  // ---------- M13 staged progress stepper ----------

  function stepperTotal() {
    const cfg = state.config || {};
    return Number(cfg.max_primary_questions || 0) || Number(state.stepper.total || 0);
  }

  function resetStepper(total) {
    const count = Number(total || 0);
    state.stepper = {
      total: count,
      done: 0,
      current: count > 0 ? 1 : 0,
      topics: [],
      followups: [],
      lastCurrent: 0,
    };
    stepperSig = "";
  }

  function completeStepper() {
    const total = stepperTotal();
    if (total <= 0) {
      return;
    }
    state.stepper.total = total;
    state.stepper.done = total;
    state.stepper.current = 0;
  }

  function notePrimaryQuestion(index, topic) {
    const s = state.stepper;
    s.total = stepperTotal() || s.total;
    s.done = Math.max(s.done, index - 1);
    s.current = index;
    if (topic) {
      s.topics[index - 1] = topic;
    }
  }

  function noteFollowupQuestion(index) {
    const s = state.stepper;
    if (!index || index < 1) {
      return;
    }
    s.followups[index - 1] = (s.followups[index - 1] || 0) + 1;
  }

  // Renders one node per plan item. Done nodes get a check, the current node is
  // highlighted, planned nodes stay grey. Follow-ups hang off the current node
  // as a dashed branch instead of becoming their own level.
  function renderStepper() {
    const total = stepperTotal();
    if (!(state.sessionId || state.sampleMode) || total <= 0) {
      stepperEl.hidden = true;
      return;
    }
    stepperEl.hidden = false;

    const s = state.stepper;
    const done = Math.min(s.done, total);
    const allDone = done >= total;
    const current = allDone ? 0 : Math.min(Math.max(s.current, 1), total);
    const sig = [total, done, current, s.topics.join("|"), s.followups.join(",")].join("#");
    if (sig === stepperSig) {
      return;
    }
    stepperSig = sig;

    const followupCap = (state.config && state.config.max_followups_per_question) || 1;
    stepperHint.textContent = `已完成 ${done} / ${total} 关`;

    stepperTrack.innerHTML = "";
    let focusNode = null;
    for (let i = 1; i <= total; i++) {
      const isDone = i <= done;
      const isCurrent = !allDone && i === current;
      const node = document.createElement("div");
      node.className = "step-node " + (isDone ? "done" : isCurrent ? "active" : "todo");
      node.setAttribute("role", "listitem");

      const main = document.createElement("div");
      main.className = "step-main";
      const dot = document.createElement("span");
      dot.className = "step-dot";
      dot.setAttribute("aria-hidden", "true");
      dot.textContent = isDone ? "✓" : String(i);
      const copy = document.createElement("span");
      copy.className = "step-copy";
      const topic = document.createElement("b");
      topic.className = "step-topic";
      const topicText = s.topics[i - 1] || "";
      topic.textContent = topicText ? truncateText(topicText, 12) : `第 ${i} 关`;
      topic.title = topicText || `第 ${i} 关`;
      const stateText = document.createElement("span");
      stateText.className = "step-state";
      stateText.textContent = isDone ? "已完成" : isCurrent ? "作答中" : "未开始";
      copy.appendChild(topic);
      copy.appendChild(stateText);
      main.appendChild(dot);
      main.appendChild(copy);
      node.appendChild(main);

      const used = s.followups[i - 1] || 0;
      if (isCurrent && used > 0) {
        const branch = document.createElement("div");
        branch.className = "step-branch";
        branch.textContent = `↳ 追问 ${used} / 上限 ${followupCap}`;
        node.appendChild(branch);
        stateText.textContent = "追问中";
      }

      stepperTrack.appendChild(node);
      if (isCurrent) {
        focusNode = node;
      }
    }

    const remaining = allDone ? 0 : Math.max(0, total - Math.max(current, done));
    stepperCompact.innerHTML = allDone
      ? `<span class="compact-main">全部 ${total} 关已完成</span>`
      : `<span class="compact-main">第 ${current} 关 / 共 ${total} 关</span>` +
        `<span class="compact-rest">剩余 ${remaining} 关</span>`;

    if (focusNode && current !== s.lastCurrent) {
      s.lastCurrent = current;
      requestAnimationFrame(() => {
        if (focusNode.isConnected) {
          focusNode.scrollIntoView({ behavior: "smooth", block: "nearest", inline: "center" });
        }
      });
    }
  }

  function stopElapsedTimer() {
    if (state.timer) {
      clearInterval(state.timer);
      state.timer = null;
    }
  }

  function startElapsedTimer(iso) {
    state.startedAt = iso || new Date().toISOString();
    stopElapsedTimer();
    state.timer = setInterval(setProgress, 1000);
    setProgress();
    renderSessionSummary();
  }

  function renderSessionSummary() {
    const cfg = state.config || {};
    $("sum-role").textContent = state.role || "-";
    $("sum-questions").textContent = `${cfg.max_primary_questions || "?"} 道主问题`;
    $("sum-followups").textContent =
      `每题最多 ${cfg.max_followups_per_question == null ? "?" : cfg.max_followups_per_question} 次` +
      ` · 总预算 ${cfg.max_total_followups == null ? "?" : cfg.max_total_followups} 次`;
    $("sum-started").textContent = state.startedAt
      ? new Date(state.startedAt).toLocaleTimeString("zh-CN", { hour12: false })
      : "等待开始…";
  }

  function decisionActionLabel(action) {
    switch (action) {
      case "followup": return "深挖追问";
      case "next_question": return "进入下一题";
      case "finish": return "题纲完成，准备收尾";
      default: return action || "决策";
    }
  }

  function reasonLabel(code) {
    switch (code) {
      case "model_output_invalid": return "模型输出无法解析";
      case "model_unavailable": return "模型暂不可用";
      case "question_duplicate": return "生成题目重复";
      case "timeout": return "模型调用超时";
      default: return code ? `错误码 ${code}` : "未知原因";
    }
  }

  function humanError(err) {
    const body = err && err.body;
    if (body && body.code === "lock_busy") {
      return "会话正忙（409 锁冲突），已自动重试；若仍失败，请 1 秒后再点一次。";
    }
    if (body && body.code === "run_in_progress") {
      return "上一轮还在执行，请等待右侧实时事件完成后再操作。";
    }
    if (body && body.code === "state_conflict") {
      return "会话状态已变化：" + (body.message || "请刷新页面后重试");
    }
    if (body && body.code === "report_not_ready") {
      return "报告还在生成中，请稍候…";
    }
    if (body && body.message) {
      return body.message;
    }
    return (err && err.message) || "请求失败，请稍后重试";
  }

  async function api(path, options) {
    const opts = Object.assign({ headers: {} }, options || {});
    opts.headers = Object.assign({ "Content-Type": "application/json" }, opts.headers || {});
    const res = await fetch(path, opts);
    let body = null;
    try {
      body = await res.json();
    } catch (ignored) {
      body = null;
    }
    if (!res.ok) {
      const err = new Error((body && body.message) || `HTTP ${res.status}`);
      err.status = res.status;
      err.body = body;
      throw err;
    }
    return body;
  }

  async function withLockRetry(fn) {
    const attempts = 4;
    for (let attempt = 1; attempt <= attempts; attempt++) {
      try {
        return await fn();
      } catch (err) {
        const code = err && err.body && err.body.code;
        if (err && err.status === 409 && code === "lock_busy" && attempt < attempts) {
          showNotice("warn", `会话繁忙（409 锁冲突），正在自动重试 ${attempt}/${attempts}…`, {
            key: "lock-retry",
            timeout: 1600,
          });
          await sleep(800);
          continue;
        }
        throw err;
      }
    }
  }

  function showThinking(text) {
    removeThinking();
    const el = card("card-thinking");
    el.id = "thinking-card";
    const head = cardHead("处理中");
    const dots = document.createElement("span");
    dots.className = "chip";
    dots.textContent = "…";
    head.appendChild(dots);
    el.appendChild(head);
    const body = document.createElement("div");
    body.className = "card-body muted";
    body.textContent = text;
    el.appendChild(body);
    resultSlotEl.appendChild(el);
  }

  function removeThinking() {
    const el = $("thinking-card");
    if (el) {
      el.remove();
    }
  }

  // ---------- SSE ----------

  function connectEvents() {
    if (state.es) {
      state.es.close();
    }
    state.streamClosed = false;
    setSSE("SSE 连接中…", "waiting");
    const es = new EventSource(`/v1/sessions/${state.sessionId}/events`);
    state.es = es;
    es.onopen = () => {
      if (!state.streamClosed) {
        setSSE("SSE 已连接", "live");
      }
    };
    es.onerror = () => {
      if (!state.streamClosed) {
        setSSE("SSE 重连中…（按事件 id 去重）", "waiting");
      }
    };
    EVENT_TYPES.forEach((type) => {
      es.addEventListener(type, (event) => handleEnvelope(type, event));
    });
  }

  function closeStream() {
    state.streamClosed = true;
    if (state.es) {
      state.es.close();
      state.es = null;
      setSSE("SSE 已关闭", "idle");
    }
  }

  function handleEnvelope(fallbackType, event) {
    let env;
    try {
      env = JSON.parse(event.data);
    } catch (ignored) {
      return;
    }
    if (env.event_id != null) {
      const key = String(env.event_id);
      if (state.seen.has(key)) {
        return; // reconnect replay: already rendered
      }
      state.seen.add(key);
    }
    const type = env.type || fallbackType;
    const payload = env.payload || {};
    const handler = HANDLERS[type];
    if (handler) {
      handler(payload, env);
    }
  }

  const HANDLERS = {
    "session.started": (p, env) => {
      setStatus("面试进行中", "running");
      if (env && env.occurred_at) {
        startElapsedTimer(env.occurred_at);
      }
    },
    "session.phase": (p) => {
      const status = p.status || "";
      const phase = p.phase || "";
      setStatus(statusLabel(status), status === "completed" ? "ok" : status === "failed" ? "error" : "running");
      $("phase-text").textContent = phaseLabel(phase);
      if (phase === "ready_to_finalize") {
        promptFinish();
      }
    },
    "question.asked": onQuestion,
    "answer.received": (p) => {
      const el = card("card-event");
      el.appendChild(cardHead("作答已入队", [{ text: `${p.chars || "?"} 字` }]));
      if (p.truncated) {
        const body = document.createElement("div");
        body.className = "card-body muted";
        body.textContent = "作答超过长度上限，已截断后评估。";
        el.appendChild(body);
      }
      appendEventCard(el);
      setStatus("正在评估作答", "running");
    },
    "evaluation.completed": onEvaluation,
    "evaluation.degraded": onEvaluationDegraded,
    "decision.made": onDecision,
    "run.failed": onRunFailed,
    "run.interrupted": () => {
      showNotice("warn", "检测到上次执行中断，服务端已恢复并继续推进。", { key: "run-interrupted" });
    },
    "report.started": () => {
      reportPanel.hidden = false;
      reportStatus.textContent = "正在生成报告…";
      setStatus("正在生成报告", "running");
    },
    "report.section": renderLiveSection,
    "report.completed": (p) => {
      reportPanel.hidden = false;
      reportStatus.textContent = "报告已生成";
      state.reportCost = p;
      renderRunInfo(p);
      if (state.sessionId) {
        loadReport(3);
      }
    },
    "session.completed": () => {
      closeStream();
      completeStepper();
      setProgress();
      stopElapsedTimer();
      setStatus("面试已完成", "ok");
      $("phase-text").textContent = "报告已生成";
      answerBox.hidden = true;
      finishBar.hidden = true;
      loadReport(3);
    },
    "session.failed": (p) => {
      closeStream();
      setStatus("会话失败", "error");
      showNotice("error", "会话执行失败：" + reasonLabel(p.code), { key: "session-failed" });
    },
    "session.aborted": () => {
      closeStream();
      setStatus("已中止", "idle");
      showNotice("warn", "会话已中止，可刷新页面重新开始。", { key: "aborted" });
    },
  };

  function onQuestion(p) {
    state.pendingTurn = p.turn_id;
    removeThinking();
    archiveFocus();
    hideEmptyState();
    const followup = Boolean(p.is_followup);
    if (!followup) {
      state.primaryAsked += 1;
      notePrimaryQuestion(state.primaryAsked, p.topic);
    } else {
      noteFollowupQuestion(state.stepper.current || state.primaryAsked);
    }
    setProgress();
    const total = state.config && state.config.max_primary_questions ? state.config.max_primary_questions : "?";

    currentQuestionEl.innerHTML = "";
    currentQuestionEl.hidden = false;
    const head = document.createElement("div");
    head.className = "q-head";
    head.innerHTML =
      `<span class="chip ${followup ? "followup" : "primary"}">${followup ? "追问" : "主问题"}</span>` +
      `<span class="muted">第 ${state.primaryAsked} / 共 ${total} 题</span>` +
      `<span class="muted">追问 ${state.followupsUsed} / ${state.config && state.config.max_total_followups != null ? state.config.max_total_followups : "?"}</span>` +
      `<span class="muted">深度 ${p.depth || 1}</span>`;
    currentQuestionEl.appendChild(head);
    if (p.topic) {
      const topic = document.createElement("div");
      topic.className = "q-topic muted";
      topic.textContent = "主题：" + p.topic;
      currentQuestionEl.appendChild(topic);
    }
    const body = document.createElement("div");
    body.className = "q-text";
    body.innerHTML = clampHtml(p.text || "", 3);
    currentQuestionEl.appendChild(body);

    answerBox.hidden = false;
    $("answer-context").textContent =
      `当前待答：${followup ? "追问" : `主问题 ${state.primaryAsked} / 共 ${state.config && state.config.max_primary_questions ? state.config.max_primary_questions : "?"} 题`}` +
      ` · turn ${String(p.turn_id).slice(0, 8)}`;
    answerInput.disabled = false;
    answerInput.value = "";
    submitBtn.disabled = false;
    answerInput.focus();
    setStatus("等待作答", "running");
    $("phase-text").textContent = "等待作答";
    refreshClamps();
  }

  function dimensionGrid(scores) {
    const grid = document.createElement("div");
    grid.className = "dim-grid";
    rubricPairs().forEach(([key, label]) => {
      const raw = scores && scores[key];
      if (raw == null) {
        return;
      }
      const row = document.createElement("div");
      row.className = "dim";
      const name = document.createElement("span");
      name.className = "dim-name";
      name.textContent = label;
      const bar = document.createElement("div");
      bar.className = "dim-bar";
      const fill = document.createElement("span");
      fill.style.width = `${Math.max(0, Math.min(5, Number(raw))) * 20}%`;
      bar.appendChild(fill);
      const value = document.createElement("span");
      value.className = "dim-value";
      value.textContent = `${raw}/5`;
      row.appendChild(name);
      row.appendChild(bar);
      row.appendChild(value);
      grid.appendChild(row);
    });
    return grid;
  }

  function normalizeScoreData(src) {
    const scores = src.scores || {};
    const base = Number(src.base_score != null ? src.base_score : 0);
    const bonusList = src.bonus || [];
    const penaltyList = src.penalty || [];
    const bonus = src.bonus_points != null
      ? Number(src.bonus_points)
      : Math.min(BONUS_CAP, bonusList.reduce((sum, item) => sum + Number(item.points || 0), 0));
    const rawPenalty = src.penalty_points != null
      ? Number(src.penalty_points)
      : penaltyList.reduce((sum, item) => sum + Number(item.points || 0), 0);
    const penalty = Math.max(PENALTY_CAP, rawPenalty);
    const total = src.total_score != null
      ? Number(src.total_score)
      : src.score != null
        ? Number(src.score)
        : Math.max(0, Math.min(100, base + bonus + penalty));
    return { scores, base, bonus, penalty, total, bonusList, penaltyList };
  }

  function humanScoreLine(d) {
    const dims = rubricDims();
    const values = dims.map((dim) => d.scores[dim.key]);
    const filled = values.filter((v) => v != null);
    const allEqual = filled.length === dims.length && filled.every((v) => v === filled[0]);
    const weights = dims.map((dim) => Number(dim.weight).toFixed(2)).join(" / ");
    let line;
    if (allEqual) {
      line = `${dims.length} 个维度均为 ${filled[0]} 分 → ${filled[0]}/5×100 = ${d.base.toFixed(1)}`;
    } else {
      line = `${dims.length} 个维度 ${values.map((v) => (v == null ? "-" : v)).join("/")}，按 ${weights} 权重折算 → base ${d.base.toFixed(1)}`;
    }
    if (d.bonus === 0 && d.penalty === 0) {
      line += "，无加分扣分。";
    } else {
      line += `，加分 +${d.bonus.toFixed(1)}、扣分 ${d.penalty.toFixed(1)} → total ${d.total.toFixed(1)}。`;
    }
    return line;
  }

  function scoreTableHtml(d) {
    const rows = rubricPairs().map(([key, label]) => {
      const raw = d.scores[key];
      if (raw == null) {
        return "";
      }
      const weight = dimWeight(key);
      const contribution = Number(raw) / 5 * weight * 100;
      return `<tr><td>${escapeHtml(label)}</td><td class="num">${raw} / 5</td>` +
        `<td class="num">${weight.toFixed(2)}</td><td class="num">${contribution.toFixed(1)}</td></tr>`;
    }).join("");
    return `<table class="calc-table"><thead><tr><th>维度</th><th class="num">原始分</th>` +
      `<th class="num">权重</th><th class="num">贡献值</th></tr></thead><tbody>${rows}</tbody></table>` +
      `<div class="formula">base = Σ(维度分 / 5 × 权重) × 100 = ${d.base.toFixed(1)}</div>`;
  }

  function adjustmentListHtml(list, kind) {
    if (!list || list.length === 0) {
      return `<p class="decision-note">无${kind === "bonus" ? "加分" : "扣分"}条目。</p>`;
    }
    const items = list.map((item) => {
      const points = Number(item.points || 0);
      const label = points > 0 ? `+${points}` : String(points);
      return `<li><span class="pts">${escapeHtml(item.type || kind)} ${label}</span> · 「${escapeHtml(item.quote || "")}」` +
        `${item.reason ? " · " + escapeHtml(item.reason) : ""}</li>`;
    }).join("");
    return `<ul class="adjust-list">${items}</ul>`;
  }

  function uniqueEvidence(list) {
    const seen = new Set();
    const out = [];
    (list || []).forEach((e) => {
      const quote = String(e.quote || "").trim();
      if (!quote) {
        return;
      }
      const key = quote.replace(/[\s\p{P}\p{S}]/gu, "").toLowerCase();
      if (!key || seen.has(key)) {
        return;
      }
      seen.add(key);
      out.push(Object.assign({}, e, { quote }));
    });
    return out;
  }

  function evidenceHtml(list) {
    const unique = uniqueEvidence(list);
    if (unique.length === 0) {
      return '<p class="decision-note">本轮没有可展示的证据摘录。</p>';
    }
    const dims = new Set(unique.map((e) => e.dimension || ""));
    const item = (e) =>
      `<li>${clampHtml(e.quote, 3)}<span class="dim-tag">支持：${escapeHtml(dimLabel(e.dimension) || "未标注")}</span>` +
      `${e.reason ? `<p class="evidence-reason">${escapeHtml(e.reason)}</p>` : ""}</li>`;
    if (dims.size < 2) {
      return `<ul class="evidence-list">${unique.map(item).join("")}</ul>`;
    }
    const known = new Set(rubricDims().map((d) => d.key));
    const groups = rubricPairs()
      .map(([key, label]) => ({ label, items: unique.filter((e) => e.dimension === key) }))
      .filter((g) => g.items.length > 0);
    const unknown = unique.filter((e) => !known.has(e.dimension));
    if (unknown.length > 0) {
      groups.push({ label: "未标注", items: unknown });
    }
    return groups.map((g) =>
      `<div class="evidence-group"><div class="calc-label">${escapeHtml(g.label)} · ${g.items.length} 条</div>` +
      `<ul class="evidence-list">${g.items.map(item).join("")}</ul></div>`).join("");
  }

  function rationaleBodyHtml(src, withDecision) {
    const d = normalizeScoreData(src);
    const missing = src.missing_points || [];
    const followup = src.followup;
    const evidenceBlock = evidenceHtml(src.evidence || []);
    const missingHtml = missing.length > 0
      ? `<ul class="missing-list">${missing.map((m) => `<li>${escapeHtml(m)}</li>`).join("")}</ul>`
      : '<p class="decision-note">关键点已覆盖。</p>';
    const followupHtml = followup
      ? `<div class="calc-label">本轮追问建议</div><p class="decision-note">${
        followup.needed
          ? `需要追问 · 目标：${escapeHtml(followup.target || "未指定")}${followup.reason ? " · " + escapeHtml(followup.reason) : ""}`
          : "不需要追问。"
      }</p>`
      : "";
    return `
      <div class="calc-label">维度分 × 权重</div>
      ${scoreTableHtml(d)}
      <div class="calc-label">加分 / 扣分</div>
      <p class="decision-note">加分合计 +${d.bonus.toFixed(1)}（上限 +${BONUS_CAP}），扣分合计 ${d.penalty.toFixed(1)}（下限 ${PENALTY_CAP}）。</p>
      ${adjustmentListHtml(d.bonusList, "bonus")}
      ${adjustmentListHtml(d.penaltyList, "penalty")}
      <div class="formula">total = base + bonus + penalty = ${d.base.toFixed(1)} + ${d.bonus.toFixed(1)} + ${d.penalty.toFixed(1)} = ${d.total.toFixed(1)}</div>
      <div class="calc-label">证据摘录</div>
      ${evidenceBlock}
      <div class="calc-label">缺失要点</div>
      ${missingHtml}
      ${withDecision === false ? "" : `<div class="calc-label">决策与理由</div>
      <p class="decision-note" id="decision-${escapeHtml(src.turn_id || "")}">决策尚未返回，评估完成后会在这里显示动作与理由。</p>`}
      ${followupHtml}
      ${src.model || src.prompt_version ? `<details class="run-info"><summary>运行信息 · 模型与 Prompt</summary>` +
        `<div class="run-info-body"><span>模型：${escapeHtml(src.model || "-")}</span>` +
        `<span>Prompt：${escapeHtml(src.prompt_version || "-")}</span></div></details>` : ""}
    `;
  }

  function rationaleElement(src, withDecision) {
    const d = normalizeScoreData(src);
    const details = document.createElement("details");
    details.className = "rationale";
    details.innerHTML =
      `<summary><span class="rationale-title">查看评分依据 ⌄</span></summary>` +
      `<div class="rationale-body"><p class="human-line">${escapeHtml(humanScoreLine(d))}</p>` +
      `${rationaleBodyHtml(src, withDecision)}</div>`;
    return details;
  }

  function scoreSummaryOf(p, d) {
    if (p.degraded) {
      return "模型输出不可用，本题不计入总分，流程继续。";
    }
    const missing = (p.missing_points || []).length;
    const covered = missing === 0 ? "关键点覆盖完整" : `${missing} 个要点未覆盖`;
    if (d.total >= 85) {
      return `表现优秀：${covered}，可直接进入下一题。`;
    }
    if (d.total >= 70) {
      return `表现良好：${covered}，补充取舍与数据细节会更稳。`;
    }
    if (d.total >= 60) {
      return `基本达标：${covered}，深度与工程判断仍需补强。`;
    }
    return `需要补强：${covered}，建议按「结论-原理-取舍-数据」重答。`;
  }

  function buildResultCard(p) {
    const d = normalizeScoreData(p);
    const el = card("card-eval result-card");
    const top = document.createElement("div");
    top.className = "result-top";
    top.innerHTML =
      `<div class="result-score-wrap"><span class="result-score">${d.total.toFixed(1)}</span>` +
      `<span class="result-unit">/ 100</span></div>` +
      `<div class="result-meta"><span class="band-pill">${bandLabel(d.total)}</span>` +
      `<p class="result-summary">${escapeHtml(scoreSummaryOf(p, d))}</p></div>`;
    el.appendChild(top);
    const grid = dimensionGrid(p.scores);
    grid.classList.add("compact");
    el.appendChild(grid);
    el.appendChild(rationaleElement(p));
    return el;
  }

  function onEvaluation(p) {
    removeThinking();
    const el = buildResultCard(p);
    appendResult(el);
    state.lastEvalCard = el;
    submitBtn.disabled = true;
    answerBox.hidden = true;
  }

  function onEvaluationDegraded(p) {
    removeThinking();
    const el = card("card-eval degraded result-card");
    el.innerHTML =
      `<div class="result-top"><div class="result-score-wrap"><span class="result-score degraded">—</span></div>` +
      `<div class="result-meta"><span class="band-pill warn">评估降级</span>` +
      `<p class="result-summary">模型输出不可用，本题不计入总分，流程继续。</p></div></div>` +
      `<details class="rationale"><summary><span class="rationale-title">查看详情 ⌄</span></summary>` +
      `<div class="rationale-body"><p class="decision-note">错误码：${escapeHtml(reasonLabel(p.code))}</p></div></details>`;
    appendResult(el);
    showNotice("warn", `评估降级：${reasonLabel(p.code)}。该题不计分，流程继续。`, { key: "eval-degraded" });
    submitBtn.disabled = true;
    answerBox.hidden = true;
  }

  function onDecision(p) {
    const actionLabel = decisionActionLabel(p.action);
    if (typeof p.followups_used === "number") {
      state.followupsUsed = p.followups_used;
      setProgress();
    }
    const note = p.turn_id ? $(`decision-${p.turn_id}`) : null;
    if (note) {
      note.textContent = `${actionLabel}：${p.reason || "未提供理由"}`;
    }
    const el = card("card-event");
    el.appendChild(cardHead("评估决策", [{ text: actionLabel }]));
    if (p.reason) {
      const body = document.createElement("div");
      body.className = "card-body muted";
      body.textContent = p.reason;
      el.appendChild(body);
    }
    appendEventCard(el);

    if (p.action === "finish") {
      promptFinish();
    } else {
      setStatus("正在生成" + (p.action === "followup" ? "追问" : "下一题") + "…", "running");
      $("phase-text").textContent = "准备出题";
      showThinking(p.action === "followup" ? "正在根据你的回答生成追问…" : "正在生成下一题…");
    }
  }

  function promptFinish() {
    if (state.canFinish) {
      return;
    }
    state.canFinish = true;
    completeStepper();
    setProgress();
    finishBtn.classList.add("pulse");
    answerBox.hidden = true;
    showNotice("ok", "题纲已完成，点击「结束面试并生成报告」查看五段报告。", { key: "finish-ready" });
    setStatus("等待收尾", "running");
    $("phase-text").textContent = "题纲完成，可收尾";
  }

  function onRunFailed(p) {
    removeThinking();
    showNotice("error", `本轮处理失败（步骤 ${p.step_name || "?"}，${p.code || "step_failed"}）。` +
      (p.retryable ? " 可以点击重试。" : ""), {
      key: "run-failed",
      actionLabel: p.retryable && p.run_id ? "重试本轮" : null,
      onAction: p.retryable && p.run_id ? () => retryRun(p.run_id) : null,
    });
    setStatus("执行失败", "error");
    submitBtn.disabled = false;
  }

  async function retryRun(runId) {
    try {
      await withLockRetry(() => api(`/v1/sessions/${state.sessionId}/runs/${runId}/retry`, { method: "POST", body: "{}" }));
      showNotice("ok", "已重新入队，等待实时事件…", { key: "run-failed", timeout: 2500 });
      setStatus("重试中", "running");
    } catch (err) {
      showNotice("error", "重试失败：" + humanError(err), { key: "run-failed" });
    }
  }

  // ---------- report ----------

  function ringSvg(score) {
    const size = 112;
    const cx = 56;
    const cy = 56;
    const r = 43;
    const circ = 2 * Math.PI * r;
    const pct = Math.max(0, Math.min(100, Number(score || 0))) / 100;
    return `<svg viewBox="0 0 ${size} ${size}" width="${size}" role="img" aria-label="总分 ${score}">` +
      `<circle cx="${cx}" cy="${cy}" r="${r}" fill="none" stroke="#e6ecf5" stroke-width="10"/>` +
      `<circle cx="${cx}" cy="${cy}" r="${r}" fill="none" stroke="#3452e1" stroke-width="10" stroke-linecap="round" ` +
      `stroke-dasharray="${(circ * pct).toFixed(1)} ${circ.toFixed(1)}" transform="rotate(-90 ${cx} ${cy})"/>` +
      `<text x="${cx}" y="${cy + 2}" text-anchor="middle" class="ring-value">${Number(score || 0).toFixed(1)}</text>` +
      `<text x="${cx}" y="${cy + 20}" text-anchor="middle" class="ring-label">/ 100</text></svg>`;
  }

  function radarSvg(dims) {
    const pairs = rubricPairs();
    const n = pairs.length;
    const size = 250;
    const cx = 125;
    const cy = 118;
    const r = 80;
    const angle = (i) => -Math.PI / 2 + i * 2 * Math.PI / n;
    const point = (i, ratio) => {
      const x = cx + Math.cos(angle(i)) * r * ratio;
      const y = cy + Math.sin(angle(i)) * r * ratio;
      return `${x.toFixed(1)},${y.toFixed(1)}`;
    };
    const values = pairs.map(([key]) => {
      const found = (dims || []).find((d) => d.dimension === key);
      return Math.max(0, Math.min(100, Number(found ? found.score : 0))) / 100;
    });
    const vertices = (ratio) => Array.from({ length: n }, (_, i) => point(i, ratio)).join(" ");
    const rings = [0.25, 0.5, 0.75, 1].map((g) =>
      `<polygon points="${vertices(g)}" fill="none" stroke="#e3e9f3" stroke-width="1"/>`).join("");
    const axes = Array.from({ length: n }, (_, i) => {
      const p = point(i, 1).split(",");
      return `<line x1="${cx}" y1="${cy}" x2="${p[0]}" y2="${p[1]}" stroke="#e3e9f3"/>`;
    }).join("");
    const shape = `<polygon points="${Array.from({ length: n }, (_, i) => point(i, values[i])).join(" ")}" ` +
      `fill="rgba(52,82,225,0.16)" stroke="#3452e1" stroke-width="2"/>`;
    const labels = pairs.map(([, label], i) => {
      const lx = cx + Math.cos(angle(i)) * (r + 22);
      const ly = cy + Math.sin(angle(i)) * (r + 22);
      return `<text x="${lx.toFixed(0)}" y="${ly.toFixed(0)}" text-anchor="middle" dominant-baseline="middle" ` +
        `font-size="13" fill="#5b6b82">${escapeHtml(label)}</text>`;
    }).join("");
    return `<svg viewBox="0 0 ${size} 246" width="100%" style="max-width:340px" role="img" aria-label="维度雷达">${rings}${axes}${shape}${labels}</svg>`;
  }

  function trendSvg(questions) {
    const list = questions || [];
    if (list.length === 0) {
      return '<p class="decision-note">暂无逐题数据。</p>';
    }
    const width = 880;
    const labelW = 132;
    const valueW = 64;
    const rowH = 46;
    const top = 12;
    const barH = 18;
    const plotW = width - labelW - valueW;
    const height = top + list.length * rowH + 6;
    const rows = list.map((q, i) => {
      const score = Math.max(0, Math.min(100, Number(q.score || 0)));
      const cy = top + i * rowH + rowH / 2;
      const w = Math.max(2, (score / 100) * plotW);
      return `<text x="0" y="${cy - 2}" font-size="13" font-weight="600" fill="#24324a">第${q.seq}题</text>` +
        `<text x="0" y="${cy + 15}" font-size="11" fill="#5b6b82">${escapeHtml(truncateText(q.topic || "", 12))}</text>` +
        `<rect x="${labelW}" y="${cy - barH / 2}" width="${plotW}" height="${barH}" rx="4" fill="#edf1f7"/>` +
        `<rect x="${labelW}" y="${cy - barH / 2}" width="${w}" height="${barH}" rx="4" fill="#8fa3c8"/>` +
        `<text x="${labelW + w + 8}" y="${cy + 5}" font-size="13" fill="#24324a">${score.toFixed(1)}</text>`;
    }).join("");
    return `<svg viewBox="0 0 ${width} ${height}" width="100%" role="img" aria-label="逐题得分趋势">${rows}</svg>`;
  }

  // SM-2 interval progression for a card answered correctly each time with
  // EF 2.5: 1, 6, 15, 38, 95, 237 days. Pure rule arithmetic, no model.
  function forgettingCurveSvg() {
    const intervals = [1, 6, 15, 38, 95, 237];
    const width = 880;
    const height = 132;
    const left = 46;
    const bottom = 28;
    const plotW = width - left * 2;
    const plotH = height - bottom - 14;
    const step = plotW / intervals.length;
    const barW = step - 18;
    const max = intervals[intervals.length - 1];
    const bars = intervals.map((days, i) => {
      const h = Math.max(4, (plotH * days) / max);
      const x = left + i * step + 9;
      const y = height - bottom - h;
      return `<rect class="fc-bar" x="${x}" y="${y}" width="${barW}" height="${h}" rx="4"/>` +
        `<text class="fc-value" x="${x + barW / 2}" y="${y - 5}" text-anchor="middle">${days}天</text>` +
        `<text class="fc-label" x="${x + barW / 2}" y="${height - 9}" text-anchor="middle">第${i + 1}次</text>`;
    }).join("");
    return `<svg viewBox="0 0 ${width} ${height}" width="100%" role="img" aria-label="遗忘曲线预测">${bars}</svg>` +
      `<div class="ring-caption">SM-2 预测：每次自评 4–5 分时的复习间隔（1 / 6 / 15 / 38 / 95 / 237 天）</div>`;
  }

  function renderReportHero(structured, questions) {
    const score = Number(structured.overall_score || 0);
    const total = questions.length;
    const plan = Number(structured.plan_items || 0) || total;
    const meta = [
      ["总分", score.toFixed(1), ""],
      ["等级", structured.score_band || bandLabel(score), ""],
      ["题数", `${total} 题`, ""],
      ["题纲覆盖", `${total} / ${plan}`, "分母为本场题纲题目数"],
    ];
    reportMeta.innerHTML = meta.map(([label, value, tip]) =>
      `<div class="meta-box"${tip ? ` title="${escapeHtml(tip)}"` : ""}>${escapeHtml(label)}` +
      `<strong>${escapeHtml(value)}</strong></div>`).join("");
    reportCharts.innerHTML =
      `<div class="chart-card"><div class="chart-title">总分进度</div>${ringSvg(score)}` +
      `<div class="ring-caption">加权总分 · ${escapeHtml(structured.score_band || bandLabel(score))}</div></div>` +
      `<div class="chart-card"><div class="chart-title">维度雷达</div>${radarSvg(structured.dimensions || [])}</div>` +
      `<div class="chart-card wide"><div class="chart-title">逐题得分趋势</div>${trendSvg(questions)}</div>` +
      `<div class="chart-card wide"><div class="chart-title">遗忘曲线预测</div>${forgettingCurveSvg()}</div>`;
  }

  function renderRunInfo(cost) {
    const parts = [];
    if (cost && cost.sample) {
      parts.push("演示样例 · 未调用模型");
    } else if (cost) {
      if (cost.model_calls != null) parts.push(`模型调用 ${cost.model_calls} 次`);
      if (cost.total_tokens != null) {
        parts.push(`Token ${cost.total_tokens}（in ${cost.prompt_tokens || 0} / out ${cost.completion_tokens || 0}）`);
      }
      if (cost.tool_calls != null) parts.push(`工具调用 ${cost.tool_calls} 次`);
      if (cost.model_name) parts.push(`模型 ${cost.model_name}`);
      if (cost.prompt_version) parts.push(`Prompt ${cost.prompt_version}`);
    }
    runInfoBody.innerHTML = parts.map((text) => {
      return text.indexOf("<a ") === 0
        ? `<span>${text}</span>`
        : `<span>${escapeHtml(text)}</span>`;
    }).join("") + '<span><a href="/metrics" target="_blank" rel="noreferrer">打开指标 /metrics</a></span>';
    runInfo.hidden = false;
  }

  function sectionPreview(section, questions) {
    const idx = Number(section.index);
    if (idx === 1) {
      return "加权得分与短板说明，点击查看明细";
    }
    if (idx === 2 && questions.length > 0) {
      return `共 ${questions.length} 题，点击查看逐题点评`;
    }
    const plain = String(section.markdown || "")
      .replace(/[#>*`|]/g, " ")
      .replace(/-{2,}/g, " ")
      .replace(/\s+/g, " ")
      .trim();
    return plain.length > 80 ? plain.slice(0, 80) + "…" : plain;
  }

  function reportSectionElement(section, questions) {
    const idx = Number(section.index || 0);
    const details = document.createElement("details");
    details.className = "section-fold";
    details.dataset.index = String(idx);
    details.open = Boolean(state.reportSectionsOpen[idx]);

    const summary = document.createElement("summary");
    summary.innerHTML =
      `<span class="sec-title">${escapeHtml(section.title || `第 ${idx + 1} 段`)}</span>` +
      `<span class="sec-preview">${escapeHtml(sectionPreview(section, questions))}</span>` +
      `<span class="sec-toggle">${details.open ? "收起 ⌃" : "展开 ⌄"}</span>`;
    details.appendChild(summary);

    const body = document.createElement("div");
    body.className = "sec-body";
    if (idx === 2 && questions.length > 0) {
      const list = document.createElement("div");
      list.className = "q-list";
      questions.forEach((q) => list.appendChild(questionReviewElement(q)));
      body.appendChild(list);
    } else {
      const md = document.createElement("div");
      md.className = "md";
      md.innerHTML = renderMarkdown(section.markdown || "");
      body.appendChild(md);
    }
    details.appendChild(body);
    details.addEventListener("toggle", () => {
      state.reportSectionsOpen[idx] = details.open;
      summary.querySelector(".sec-toggle").textContent = details.open ? "收起 ⌃" : "展开 ⌄";
    });
    return details;
  }

  function renderLiveSection(p) {
    reportPanel.hidden = false;
    reportStatus.textContent = "正在生成报告…";
    const idx = Number(p.index || 0);
    const existing = reportSections.querySelector(`[data-index="${idx}"]`);
    const el = reportSectionElement({ index: idx, title: p.title, markdown: p.markdown }, []);
    if (existing) {
      existing.replaceWith(el);
    } else {
      reportSections.appendChild(el);
    }
  }

  async function loadReport(retries) {
    try {
      const dto = await api(`/v1/sessions/${state.sessionId}/report`);
      renderReport(dto);
      state.reportLoaded = true;
    } catch (err) {
      if (err.status === 409 && retries > 0) {
        await sleep(800);
        return loadReport(retries - 1);
      }
      reportStatus.textContent = "报告读取失败";
      showNotice("error", "报告读取失败：" + humanError(err), { key: "report-load" });
    }
  }

  function questionReviewElement(q) {
    const details = document.createElement("details");
    details.className = "q-review";
    details.open = Boolean(state.reviewOpen[q.turn_id]);
    const kind = q.kind === "followup" ? "追问" : "主问题";
    const score = Number(q.score || 0);
    const head = document.createElement("summary");
    head.innerHTML =
      `<span>第 ${q.seq} 题 · ${escapeHtml(q.topic || "未命名主题")}</span>` +
      `<span class="chip">${kind}</span>` +
      `<span class="q-score">${score.toFixed(1)} 分${q.degraded ? "（降级）" : ""}</span>`;
    details.appendChild(head);

    const body = document.createElement("div");
    body.className = "md q-body";
    let html = "";
    if (q.question) {
      html += `<div class="calc-label">题目</div>${clampHtml(q.question, 3)}`;
    }
    if (q.comment) {
      html += `<div class="calc-label">点评</div>${clampHtml(q.comment, 3)}`;
    }
    if (q.strong_points && q.strong_points.length > 0) {
      html += `<p><strong>亮点：</strong>${escapeHtml(q.strong_points.join("；"))}</p>`;
    }
    if (q.missing_points && q.missing_points.length > 0) {
      html += `<p><strong>待补：</strong>${escapeHtml(q.missing_points.join("；"))}</p>`;
    }
    body.innerHTML = html;
    details.appendChild(body);

    if (!q.degraded) {
      details.appendChild(rationaleElement(q, false));
    }
    details.addEventListener("toggle", () => {
      state.reviewOpen[q.turn_id] = details.open;
    });
    return details;
  }

  function renderReport(dto, options) {
    const structured = dto.structured || {};
    const sections = structured.sections || [];
    const questions = structured.questions || [];
    const scroll = !options || options.scroll !== false;
    reportPanel.hidden = false;
    reportStatus.textContent = dto.degraded ? "已生成（降级拼装）" : "已生成";
    $("report-sample-tag").hidden = !dto.sample;
    renderReportHero(structured, questions);
    renderRunInfo(dto.sample ? { sample: true } : Object.assign({}, structured, state.reportCost || {}));

    reportSections.innerHTML = "";
    if (sections.length > 0) {
      sections.forEach((section) => reportSections.appendChild(reportSectionElement(section, questions)));
    } else if (dto.markdown) {
      reportSections.appendChild(reportSectionElement({ index: 0, title: "报告全文", markdown: dto.markdown }, questions));
    }
    if (dto.degraded) {
      const banner = document.createElement("div");
      banner.className = "notice warn";
      banner.style.gridColumn = "1 / -1";
      banner.innerHTML = `报告降级：${escapeHtml(reasonLabel(structured.error_code))}，以下内容由已有评估数据拼装。`;
      reportCharts.appendChild(banner);
    }
    refreshClamps();
    refreshReviewEntry();
    if (scroll) {
      reportPanel.scrollIntoView({ behavior: "smooth", block: "start" });
    }
  }

  // ---------- actions ----------

  function sampleResultCard(q) {
    const el = buildResultCard(q);
    const tag = document.createElement("span");
    tag.className = "sample-corner";
    tag.textContent = "演示样例";
    el.appendChild(tag);
    return el;
  }

  function sampleQuestionEl(q) {
    const wrap = document.createElement("div");
    wrap.className = "history-item";
    const head = document.createElement("div");
    head.className = "q-head";
    head.innerHTML =
      `<span class="chip primary">主问题</span>` +
      `<span class="muted">第 ${q.seq} / 共 ${SAMPLE_QUESTIONS.length} 题</span>` +
      `<span class="muted">主题：${escapeHtml(q.topic)}</span>`;
    wrap.appendChild(head);
    const text = document.createElement("div");
    text.className = "q-text";
    text.innerHTML = clampHtml(q.question, 3);
    wrap.appendChild(text);
    wrap.appendChild(sampleResultCard(q));
    return wrap;
  }

  function renderSampleQuestion(q) {
    hideEmptyState();
    currentQuestionEl.innerHTML = "";
    currentQuestionEl.hidden = false;
    const head = document.createElement("div");
    head.className = "q-head";
    head.innerHTML =
      `<span class="chip primary">主问题</span>` +
      `<span class="muted">第 ${q.seq} / 共 ${SAMPLE_QUESTIONS.length} 题</span>` +
      `<span class="muted">追问 0 / 2</span>` +
      `<span class="muted">深度 ${q.depth}</span>`;
    currentQuestionEl.appendChild(head);
    const topic = document.createElement("div");
    topic.className = "q-topic muted";
    topic.textContent = "主题：" + q.topic;
    currentQuestionEl.appendChild(topic);
    const body = document.createElement("div");
    body.className = "q-text";
    body.innerHTML = clampHtml(q.question, 3);
    currentQuestionEl.appendChild(body);
    answerBox.hidden = true;
  }

  function loadSample() {
    state.sampleMode = false;
    restartInterview();
    state.sampleMode = true;
    state.role = "Go 后端开发工程师";
    state.config = { max_primary_questions: 3, max_followups_per_question: 1, max_total_followups: 2 };
    state.rubric = null;
    state.primaryAsked = 3;
    state.followupsUsed = 0;
    resetStepper(3);
    state.stepper.topics = SAMPLE_QUESTIONS.map((q) => q.topic);
    state.startedAt = "2026-11-06T12:40:00Z";
    state.historyCount = SAMPLE_QUESTIONS.length - 1;
    state.reportLoaded = true;

    $("setup-block").hidden = true;
    $("session-summary").hidden = false;
    $("progress-strip").hidden = false;
    $("restart-btn").textContent = "重新载入样例";
    renderSessionSummary();
    $("sum-role").textContent = "Go 后端开发工程师（样例）";
    $("sum-started").textContent = "演示样例 · 12:40";
    setProgress();
    $("prog-elapsed").textContent = "12:40";

    historyBlock.hidden = false;
    historyBlock.open = true;
    historyCountEl.textContent = String(state.historyCount);
    [SAMPLE_QUESTIONS[1], SAMPLE_QUESTIONS[2]].forEach((q) => historyBody.appendChild(sampleQuestionEl(q)));

    renderSampleQuestion(SAMPLE_QUESTIONS[0]);
    resultSlotEl.appendChild(sampleResultCard(SAMPLE_QUESTIONS[0]));
    showNotice("warn", "<strong>演示样例</strong> · 内置确定性数据（3 题 · 82 / 68 / 74），未调用模型，也不会写入真实会话。", { key: "sample" });

    renderReport({ structured: SAMPLE_REPORT, sample: true }, { scroll: false });
    window.scrollTo({ top: 0, behavior: "auto" });

    setStatus("演示样例", "ok");
    $("phase-text").textContent = "样例模式 · 未调用模型";
    setSSE("样例模式", "idle");
    refreshClamps();
  }


  // ---------- M11 review (spaced repetition) ----------

  // tz_offset_minutes is minutes east of UTC; JS returns the opposite sign.
  const REVIEW_TZ_OFFSET = -new Date().getTimezoneOffset() || 480;
  let reviewCards = [];
  let reviewIndex = 0;

  function reviewTzOffset() {
    return REVIEW_TZ_OFFSET;
  }

  function formatReviewDue(value) {
    const d = new Date(value);
    if (isNaN(d.getTime())) {
      return "今天";
    }
    const mm = String(d.getMonth() + 1).padStart(2, "0");
    const dd = String(d.getDate()).padStart(2, "0");
    return `${mm}/${dd}`;
  }

  function gradeLabel(grade) {
    return ["完全忘记", "几乎想不起", "勉强回忆", "想起要点", "比较熟练", "秒答"][grade] || String(grade);
  }

  async function refreshReviewEntry() {
    if (state.sampleMode) {
      return;
    }
    try {
      const due = await api(`/v1/review/due?tz_offset_minutes=${reviewTzOffset()}&limit=1`);
      const count = Number(due.count || 0);
      reviewDueCount.textContent = String(count);
      reviewEntry.hidden = false;
      reviewEntry.classList.toggle("has-due", count > 0);
      reviewEntry.title = count > 0 ? "点击开始今日复习" : "今天没有到期卡片";
    } catch (err) {
      // Review can be disabled (503) or unavailable in a minimal deployment.
      reviewEntry.hidden = true;
    }
  }

  async function openReview() {
    reviewPanel.hidden = false;
    reviewStatus.textContent = "正在加载…";
    reviewBody.innerHTML = "";
    try {
      const due = await api(`/v1/review/due?tz_offset_minutes=${reviewTzOffset()}&limit=50`);
      reviewCards = Array.isArray(due.cards) ? due.cards : [];
      reviewIndex = 0;
      renderReviewCard();
    } catch (err) {
      reviewStatus.textContent = "加载失败";
      reviewBody.innerHTML = `<div class="notice warn">${escapeHtml(humanError(err))}</div>`;
    }
    reviewPanel.scrollIntoView({ behavior: "smooth", block: "start" });
  }

  function renderReviewCard() {
    if (reviewIndex >= reviewCards.length) {
      reviewStatus.textContent = "今日复习完成";
      reviewBody.innerHTML =
        `<div class="review-done"><strong>今天的卡片已复习完</strong>` +
        `<p class="muted">评分已写回 SM-2 排程，下次到期时会重新出现在这里。</p></div>`;
      refreshReviewEntry();
      return;
    }
    const card = reviewCards[reviewIndex];
    const reps = Number(card.repetitions || 0);
    reviewStatus.textContent = `第 ${reviewIndex + 1} / ${reviewCards.length} 张 · 到期 ${formatReviewDue(card.due_at)}`;
    const points = (card.key_points || []).map((p) => `<li>${escapeHtml(p)}</li>`).join("");
    reviewBody.innerHTML =
      `<article class="review-card" data-card-id="${escapeHtml(card.id)}">` +
      `<div class="review-kicker"><span class="chip primary">${escapeHtml(card.topic || "复习卡")}</span>` +
      (card.dimension ? `<span class="chip">${escapeHtml(card.dimension)}</span>` : "") +
      `<span class="muted">第 ${reps + 1} 次复习 · EF ${Number(card.ease_factor || 2.5).toFixed(2)}</span></div>` +
      `<div class="review-question">${escapeHtml(card.question || "")}</div>` +
      `<details class="review-answer"><summary>展开参考答案</summary>` +
      (card.answer_digest ? `<div class="calc-label">你当时的答法</div><p>${escapeHtml(card.answer_digest)}</p>` : "") +
      `<div class="calc-label">标准要点</div><ul>${points || "<li>补齐核心机制与边界条件</li>"}</ul>` +
      `</details>` +
      `<div class="review-retest" hidden></div>` +
      `<div class="grade-row"><span class="muted">回忆质量 0–5：</span>` +
      [0, 1, 2, 3, 4, 5].map((g) =>
        `<button class="grade-btn" type="button" data-grade="${g}" title="${gradeLabel(g)}">${g}</button>`).join("") +
      `</div>` +
      `<div class="review-actions">` +
      `<button class="ghost" type="button" data-review-action="retest">换问法再测</button>` +
      `<button class="ghost" type="button" data-review-action="skip">稍后再复习</button>` +
      `</div>` +
      `</article>`;
  }

  async function gradeReview(grade) {
    const card = reviewCards[reviewIndex];
    if (!card) {
      return;
    }
    const buttons = reviewBody.querySelectorAll(".grade-btn");
    buttons.forEach((btn) => { btn.disabled = true; });
    try {
      const result = await api(`/v1/review/${encodeURIComponent(card.id)}/grade`, {
        method: "POST",
        body: JSON.stringify({ grade }),
      });
      const calc = result.calculation || {};
      reviewCards[reviewIndex] = result.card || card;
      showNotice("ok",
        `已排程：EF ${Number(calc.new_ease || 0).toFixed(2)}，间隔 ${calc.new_interval_days} 天，下次 ${formatReviewDue(calc.due_at)}`,
        { key: "review-grade", timeout: 2400 });
      reviewIndex += 1;
      renderReviewCard();
    } catch (err) {
      buttons.forEach((btn) => { btn.disabled = false; });
      showNotice("error", "复习评分失败：" + humanError(err), { key: "review-grade" });
    }
  }

  async function showReviewRetest() {
    const card = reviewCards[reviewIndex];
    if (!card) {
      return;
    }
    const box = reviewBody.querySelector(".review-retest");
    if (!box) {
      return;
    }
    try {
      const retest = await api(`/v1/review/${encodeURIComponent(card.id)}/retest`);
      box.hidden = false;
      box.innerHTML =
        `<div class="calc-label">换问法（规则生成，未调用模型）</div>` +
        `<p>${escapeHtml(retest.question || "")}</p>`;
    } catch (err) {
      showNotice("error", "换问法失败：" + humanError(err), { key: "review-retest" });
    }
  }

  // ---------- resume attachment (local parsing only) ----------

  const RESUME_MAX_BYTES = 5 * 1024 * 1024;
  const RESUME_TEXT_FORMATS = [".pdf", ".docx", ".txt", ".md"];
  const RESUME_IMAGE_FORMATS = [".png", ".jpg", ".jpeg"];

  function formatBytes(n) {
    if (n >= 1024 * 1024) return (n / (1024 * 1024)).toFixed(1) + " MB";
    if (n >= 1024) return Math.round(n / 1024) + " KB";
    return n + " B";
  }

  function resumeExt(name) {
    const raw = String(name || "");
    const i = raw.lastIndexOf(".");
    return i >= 0 ? raw.slice(i).toLowerCase() : "";
  }

  function setUploadState(kind, html) {
    const el = $("resume-upload-state");
    if (!kind) {
      el.hidden = true;
      el.className = "upload-state";
      el.innerHTML = "";
      return;
    }
    el.hidden = false;
    el.className = "upload-state " + kind;
    el.innerHTML = html;
  }

  function renderResumeProfile(doc) {
    const box = $("resume-profile");
    const profile = (doc && doc.profile) || {};
    const skills = Array.isArray(profile.skills) ? profile.skills : [];
    const projects = Array.isArray(profile.projects) ? profile.projects : [];
    const skillsHtml = skills.length
      ? skills.slice(0, 12).map((s) => `<span class="chip">${escapeHtml(s)}</span>`).join("")
      : '<span class="muted">未识别</span>';
    let projectHtml = '<span class="muted">未识别</span>';
    if (projects.length) {
      projectHtml = projects.slice(0, 3).map((p) => escapeHtml(truncateText(p.name || "未命名项目", 22))).join(" · ");
      if (projects.length > 3) {
        projectHtml += ` 等 ${projects.length} 个`;
      }
    }
    box.hidden = false;
    box.innerHTML =
      '<div class="profile-head">结构化画像<span class="tag">本地解析 · 未调用模型</span></div>' +
      '<dl class="profile-list">' +
      `<div class="profile-row"><dt>年限</dt><dd>${profile.years ? profile.years + " 年" : '<span class="muted">未识别</span>'}</dd></div>` +
      `<div class="profile-row"><dt>技能</dt><dd>${skillsHtml}</dd></div>` +
      `<div class="profile-row"><dt>项目</dt><dd>${projectHtml}</dd></div>` +
      "</dl>";
  }

  async function handleResumeFile(file) {
    if (!file) {
      return;
    }
    const ext = resumeExt(file.name);
    if (RESUME_IMAGE_FORMATS.includes(ext)) {
      setUploadState("error",
        "<b>需 OCR / 多模态，尚未启用</b><span>PNG / JPG 无法本地解析；请改用 PDF / DOCX / TXT / MD，或直接在下方粘贴简历文本。</span>");
      return;
    }
    if (!RESUME_TEXT_FORMATS.includes(ext)) {
      setUploadState("error",
        `<b>不支持的文件类型</b><span>${escapeHtml(ext || "无扩展名")} 不在白名单内，仅支持 PDF / DOCX / TXT / MD。</span>`);
      return;
    }
    if (file.size > RESUME_MAX_BYTES) {
      setUploadState("error",
        `<b>文件超过 5 MB</b><span>当前 ${formatBytes(file.size)}；请压缩后重试，或直接在下方粘贴简历文本。</span>`);
      return;
    }

    setUploadState("parsing", `<b>正在本地解析</b><span>${escapeHtml(file.name)} · ${formatBytes(file.size)}</span>`);
    try {
      const form = new FormData();
      form.append("file", file);
      const res = await fetch("/v1/resume/parse", { method: "POST", body: form });
      let body = null;
      try {
        body = await res.json();
      } catch (ignored) {
        body = null;
      }
      if (!res.ok) {
        const err = new Error((body && body.message) || `HTTP ${res.status}`);
        err.status = res.status;
        err.body = body;
        throw err;
      }
      $("resume-text").value = body.text || "";
      diagnoseOverrides = {};
      $("diagnose-result").hidden = true;
      refreshAnalysisEmptyState();
      setDiagnoseState(null);
      renderResumeProfile(body);
      setUploadState("ok",
        `<b>解析成功</b><span>${escapeHtml(body.filename)} · ${Number(body.chars || 0).toLocaleString()} 字` +
        `${body.truncated ? " · 已截断" : ""} · 已回填下方文本</span>`);
    } catch (err) {
      $("resume-profile").hidden = true;
      setUploadState("error",
        `<b>解析失败，可手动降级</b><span>${escapeHtml(humanError(err))}</span>` +
        "<span>请在下方直接粘贴简历文本，面试流程不受影响。</span>");
    }
  }

  function resetResumeUpload() {
    setUploadState(null);
    $("resume-profile").hidden = true;
    $("resume-profile").innerHTML = "";
    diagnoseOverrides = {};
    $("diagnose-result").hidden = true;
    refreshAnalysisEmptyState();
    setDiagnoseState(null);
  }

  // ---------- resume diagnosis (deterministic rules, no model) ----------

  const SEGMENT_TYPE_LABELS = {
    internship: "实习/工作",
    personal_project: "个人项目",
    opensource: "开源贡献",
    competition: "竞赛",
    coursework: "课程/毕设",
    unknown: "待确认",
  };
  const SEVERITY_LABELS = { high: "高", medium: "中", low: "低" };
  const BAND_TONE = { 1: "danger", 2: "warn", 3: "warn", 4: "ok", 5: "ok" };
  let diagnoseOverrides = {};

  function setDiagnoseState(kind, html) {
    const el = $("diagnose-state");
    if (!kind) {
      el.hidden = true;
      el.className = "upload-state";
      el.innerHTML = "";
      return;
    }
    el.hidden = false;
    el.className = "upload-state " + kind;
    el.innerHTML = html;
  }

  function dimBar(weight, deducted) {
    const pct = weight > 0 ? Math.min(100, Math.round((deducted / weight) * 100)) : 0;
    return `<div class="diag-bar" aria-hidden="true"><span style="width:${pct}%"></span></div>`;
  }

  function renderDiagnosis(diag) {
    const box = $("diagnose-result");
    const tone = BAND_TONE[diag.band.level] || "warn";
    const dims = Array.isArray(diag.dimensions) ? diag.dimensions : [];
    const segments = Array.isArray(diag.segments) ? diag.segments : [];
    const issues = Array.isArray(diag.issues) ? diag.issues : [];
    const confirms = Array.isArray(diag.needs_confirmation) ? diag.needs_confirmation : [];
    const manual = Array.isArray(diag.manual_checks) ? diag.manual_checks : [];

    const scoreRing =
      `<div class="diag-ring ${tone}" style="--score:${Number(diag.overall_score) || 0}">` +
      `<div class="diag-ring-inner"><b>${Number(diag.overall_score) || 0}</b><span>${diag.band.level} 档</span></div></div>`;

    const subScores =
      `<div class="diag-subscore"><b>${diag.screen_score.score}</b><span>初筛分 · HR 视角</span></div>` +
      `<div class="diag-subscore"><b>${diag.tech_score.score}</b><span>技术分 · 面试官视角</span></div>` +
      `<div class="diag-subscore"><b>${diag.document_score}</b><span>文档分 · A–E 规则</span></div>`;

    const top = (diag.top_issues || []).map((text) => `<li>${escapeHtml(text)}</li>`).join("");

    const dimHtml = dims.map((d) => (
      `<div class="diag-dim">` +
      `<div class="diag-dim-head"><span>${escapeHtml(d.id)} · ${escapeHtml(d.name)}</span>` +
      `<span class="muted">扣 ${d.deducted} / ${d.weight}</span></div>` +
      dimBar(d.weight, d.deducted) +
      `</div>`
    )).join("");

    const segmentHtml = segments.map((seg) => {
      if (typeof seg.score !== "number" || seg.score < 0) {
        return `<div class="diag-segment unknown"><div class="diag-segment-head">` +
          `<b>${escapeHtml(truncateText(seg.title, 36))}</b><span class="tag">${escapeHtml(SEGMENT_TYPE_LABELS[seg.type] || seg.type)}</span>` +
          `</div><p class="muted">类型未确认，暂不计分。</p></div>`;
      }
      const segDims = (seg.dimensions || []).map((d) => (
        `<div class="diag-dim"><div class="diag-dim-head"><span>${escapeHtml(d.name)}</span>` +
        `<span class="muted">扣 ${d.deducted} / ${d.weight}</span></div>${dimBar(d.weight, d.deducted)}</div>`
      )).join("");
      const prompts = (seg.followups || []).map((p) => `<li>${escapeHtml(p)}</li>`).join("");
      return `<div class="diag-segment">` +
        `<div class="diag-segment-head"><b>${escapeHtml(truncateText(seg.title, 36))}</b>` +
        `<span class="tag">${escapeHtml(SEGMENT_TYPE_LABELS[seg.type] || seg.type)}</span>` +
        `<span class="diag-segment-score">${seg.score}</span></div>` +
        `<p class="muted">${seg.months ? seg.months + " 个月 · " : ""}篇幅权重 ${seg.weight_percent}% · ${seg.type_source === "user" ? "类型由你指定" : "类型自动识别"}</p>` +
        segDims +
        (prompts ? `<details class="diag-fold"><summary>该段面试追问（${seg.followups.length} 条）</summary><ul>${prompts}</ul></details>` : "") +
        `</div>`;
    }).join("");

    const issueHtml = issues.map((issue) => {
      const sev = SEVERITY_LABELS[issue.severity] || issue.severity;
      const dim = issue.dimension_name || issue.dimension;
      const rewrite = issue.rewrite && (issue.rewrite.before || issue.rewrite.after)
        ? `<details class="diag-fold"><summary>查看改写示例（差 → 好）</summary>` +
          `<div class="diag-rewrite"><p><span class="bad">差</span>${escapeHtml(issue.rewrite.before)}</p>` +
          `<p><span class="good">好</span>${escapeHtml(issue.rewrite.after)}</p></div></details>`
        : "";
      return `<div class="diag-issue sev-${escapeHtml(issue.severity)}">` +
        `<div class="diag-issue-head"><span class="sev">${escapeHtml(sev)}</span>` +
        `<b>${escapeHtml(issue.rule_id)} · ${escapeHtml(issue.title)}</b>` +
        `<span class="diag-deduct">−${issue.deducted}</span></div>` +
        `<p class="diag-quote">原文：${escapeHtml(issue.quote)}</p>` +
        `<p class="muted">${escapeHtml(dim)} · 证据强度：${escapeHtml(issue.evidence)}` +
        `${issue.mode === "auto_manual" ? " · 自动判定部分，可人工申诉" : ""}` +
        `${issue.note ? " · " + escapeHtml(issue.note) : ""}</p>` +
        rewrite + `</div>`;
    }).join("") || '<p class="muted">未发现自动可判定的扣分项。</p>';

    const confirmHtml = confirms.length
      ? `<div class="diag-confirm"><b>有 ${confirms.length} 段经历类型无法判断，请确认后重算</b>` +
        confirms.map((item) => {
          const options = Object.keys(SEGMENT_TYPE_LABELS)
            .filter((key) => key !== "unknown")
            .map((key) => `<option value="${key}"${diagnoseOverrides[item.index] === key ? " selected" : ""}>` +
              `${escapeHtml(SEGMENT_TYPE_LABELS[key])}</option>`).join("");
          return `<div class="diag-confirm-row"><span>#${item.index} ${escapeHtml(truncateText(item.title, 40))}</span>` +
            `<select data-segment="${item.index}" class="diag-type-select">${options}</select>` +
            `<button type="button" class="secondary diag-confirm-btn" data-segment="${item.index}">按此类型重算</button></div>`;
        }).join("") + `</div>`
      : "";

    const manualHtml = manual.length
      ? `<details class="diag-fold"><summary>需人工确认的 ${manual.length} 项（不计入自动分）</summary>` +
        manual.map((item) => `<li><b>${escapeHtml(item.rule_id)}</b> ${escapeHtml(item.title)}` +
          `<span class="muted"> — ${escapeHtml(item.note)}</span></li>`).join("") + `</details>`
      : "";

    box.innerHTML =
      `<div class="diag-head">${scoreRing}<div class="diag-head-main">` +
      `<div class="diag-badges">${subScores}</div>` +
      `<p class="muted">${escapeHtml(diag.band.name)} · ${escapeHtml(diag.rules_summary)} · 不调用模型 · ${diag.rule_hits} 条自动命中</p>` +
      `<p class="diag-advice">${escapeHtml(diag.conflict_advice)}</p>` +
      `<ul class="diag-top">${top}</ul>` +
      `</div></div>` +
      `<div class="diag-grid">` +
      `<section><h4>A–E 维度扣分</h4>${dimHtml}</section>` +
      `<section><h4>分段得分（按经历类型）</h4>${segmentHtml || '<p class="muted">未识别到可计分的经历段落。</p>'}</section>` +
      `</div>` +
      confirmHtml +
      `<section class="diag-issues"><h4>问题列表（按严重度排序）</h4>${issueHtml}</section>` +
      manualHtml;
    showAnalysisResult("diagnosis");
  }

  async function runResumeDiagnosis(extraOverrides) {
    const text = $("resume-text").value.trim();
    if (!text) {
      setDiagnoseState("error", "<b>没有可诊断的文本</b><span>请先上传简历或粘贴正文。</span>");
      refreshAnalysisEmptyState();
      return;
    }
    diagnoseOverrides = Object.assign({}, diagnoseOverrides, extraOverrides || {});
    const segmentTypes = Object.keys(diagnoseOverrides)
      .map((key) => ({ index: Number(key), type: diagnoseOverrides[key] }));
    setDiagnoseState("parsing", "<b>正在按规则诊断</b><span>分段、类型识别、40+ 检查项逐条匹配 · 不调用模型</span>");
    try {
      const diag = await api("/v1/resume/analyze", {
        method: "POST",
        body: JSON.stringify({
          text,
          jd_text: $("jd-text").value.trim(),
          target_role: $("target-role").value.trim(),
          filename: "pasted-resume.txt",
          segment_types: segmentTypes,
        }),
      });
      renderDiagnosis(diag);
      setDiagnoseState("ok",
        `<b>诊断完成</b><span>总分 ${diag.overall_score}（${escapeHtml(diag.band.name)}）· 初筛 ${diag.screen_score.score} / 技术 ${diag.tech_score.score} · 未调用模型</span>`);
    } catch (err) {
      setDiagnoseState("error", `<b>诊断失败</b><span>${escapeHtml(humanError(err))}</span>`);
    }
  }

  // ---------- JD analysis & application kit (rule-based, no model) ----------

  let jdState = { id: "", kitMarkdown: "" };

  function setJDState(kind, html) {
    const el = $("jd-state");
    if (!kind) {
      el.hidden = true;
      el.className = "upload-state";
      el.innerHTML = "";
      return;
    }
    el.hidden = false;
    el.className = "upload-state " + kind;
    el.innerHTML = html;
  }

  function jdChips(items, cls) {
    if (!Array.isArray(items) || !items.length) {
      return '<span class="muted">无</span>';
    }
    return items.map((item) => `<span class="jd-chip ${cls}">${escapeHtml(item)}</span>`).join("");
  }

  function renderJDResult(result) {
    const box = $("jd-result");
    const structured = result.structured || {};
    const match = result.match || {};
    const gaps = result.gaps || {};
    const outline = Array.isArray(result.outline) ? result.outline : [];
    const score = match.available ? (Number(match.score) || 0) : 0;
    const tone = score >= 70 ? "ok" : score >= 45 ? "warn" : "danger";
    const hasResume = $("resume-text").value.trim().length > 0;

    const ring = match.available
      ? `<div class="diag-ring ${tone}" style="--score:${score}">` +
        `<div class="diag-ring-inner"><b>${score}</b><span>匹配度</span></div></div>`
      : `<div class="diag-ring danger" style="--score:0">` +
        `<div class="diag-ring-inner"><b>—</b><span>未匹配</span></div></div>`;

    const years = structured.min_years
      ? (structured.max_years ? `${structured.min_years}–${structured.max_years} 年` : `${structured.min_years} 年以上`)
      : "年限未识别";

    const matchedSet = new Set((match.matched_keywords || []).map((kw) => String(kw).toLowerCase()));
    const requiredMatched = (structured.required_skills || []).filter((kw) => matchedSet.has(String(kw).toLowerCase()));
    const requiredMissing = (structured.required_skills || []).filter((kw) => !matchedSet.has(String(kw).toLowerCase()));

    const outlineHtml = outline.map((item) => (
      `<li><b>${escapeHtml(item.topic)}</b> <span class="muted">· ${escapeHtml(item.difficulty)}</span>` +
      `<br><span class="muted">${escapeHtml(item.objective)}</span></li>`
    )).join("") || '<li class="muted">JD 未匹配到领域包，未生成题纲。</li>';

    const gapHtml = (Array.isArray(gaps.missing_experiences) && gaps.missing_experiences.length)
      ? `<div class="jd-section"><h4>缺口与准备方向</h4><ul>${gaps.missing_experiences.map(
          (item) => `<li>${escapeHtml(item)}</li>`).join("")}</ul></div>`
      : "";

    box.innerHTML =
      `<div class="jd-head">${ring}<div class="jd-head-main">` +
      `<p class="jd-meta"><span>岗位：${escapeHtml(structured.role || "未识别")}</span>` +
      `${structured.company ? `<span>公司：${escapeHtml(structured.company)}</span>` : ""}` +
      `<span>经验：${escapeHtml(years)}</span><span>来源：${result.source_kind === "url" ? "公开链接" : "粘贴文本"}</span></p>` +
      `<p class="diag-advice">${escapeHtml(match.note || "")}</p>` +
      `</div></div>` +
      `<div class="jd-grid">` +
      `<div class="jd-section"><h4>技能覆盖</h4>` +
      `<p class="muted">必备技能（红 = 简历未体现）</p>` +
      `<div class="jd-chips">${jdChips(requiredMatched, "matched")}${jdChips(requiredMissing, "missing")}</div>` +
      `<p class="muted">加分项</p>` +
      `<div class="jd-chips">${jdChips(structured.nice_to_have, "nice")}</div></div>` +
      `<div class="jd-section"><h4>该 JD 的面试题纲（复用领域包 Plan）</h4><ol class="jd-outline">${outlineHtml}</ol></div>` +
      `</div>` +
      gapHtml +
      `<div class="jd-section"><h4>申请材料包（不代投）</h4>` +
      `<button type="button" class="secondary" data-jd-action="kit"${hasResume ? "" : " disabled"}>生成申请材料包</button>` +
      `${hasResume ? "" : '<span class="muted">先填写上方简历文本</span>'}</div>` +
      `<div id="jd-kit" class="jd-kit" hidden></div>`;
    showAnalysisResult("jd");
  }

  async function runJDAnalysis() {
    const url = $("jd-url").value.trim();
    const text = $("jd-text").value.trim();
    if (!url && !text) {
      setJDState("error", "<b>没有可解析的 JD</b><span>请粘贴 JD 文本，或输入公开岗位链接。</span>");
      refreshAnalysisEmptyState();
      return;
    }
    const payload = {
      resume_text: $("resume-text").value.trim(),
      target_role: $("target-role").value.trim(),
      limit: 5,
    };
    if (url) {
      payload.url = url;
    } else {
      payload.text = text;
    }

    jdState = { id: "", kitMarkdown: "" };
    setJDState("parsing",
      `<b>正在解析 JD</b><span>${url ? "抓取公开页面 · 遵守 robots.txt 与限速" : "解析粘贴文本"} · 不调用模型</span>`);
    try {
      const result = await api("/v1/jd/analyze", { method: "POST", body: JSON.stringify(payload) });
      jdState.id = result.jd_id || "";
      renderJDResult(result);
      const scoreText = result.match && result.match.available
        ? `匹配度 ${result.match.score}`
        : "未提供简历，仅解析";
      setJDState("ok",
        `<b>JD 解析完成</b><span>${escapeHtml(result.structured.role || "未识别岗位")} · ${scoreText} · 题纲 ${result.outline.length} 题 · 未调用模型</span>`);
    } catch (err) {
      setJDState("error", `<b>解析失败</b><span>${escapeHtml(humanError(err))}</span>`);
    }
  }

  async function runApplyKit() {
    const id = jdState.id;
    const resumeText = $("resume-text").value.trim();
    if (!id || !resumeText) {
      setJDState("error", "<b>无法生成材料包</b><span>请先解析 JD，并填写简历文本。</span>");
      return;
    }
    setJDState("parsing", "<b>正在生成申请材料包</b><span>只重排简历已有事实 · 不新增虚构内容 · 不代投</span>");
    try {
      const kit = await api(`/v1/jd/${id}/kit`, {
        method: "POST",
        body: JSON.stringify({
          resume_text: resumeText,
          target_role: $("target-role").value.trim(),
          candidate_name: $("candidate-name").value.trim(),
        }),
      });
      jdState.kitMarkdown = kit.markdown || "";
      renderApplyKit(kit);
      setJDState("ok", `<b>材料包已生成</b><span>摘要 / 开场 / 题纲 / 预演问题 · 未调用模型 · 不提供自动投递</span>`);
    } catch (err) {
      setJDState("error", `<b>生成失败</b><span>${escapeHtml(humanError(err))}</span>`);
    }
  }

  function renderApplyKit(kit) {
    const box = $("jd-kit");
    const facts = (kit.facts || []).map((fact) => (
      `<li>${escapeHtml(fact.text)} <span class="muted">（${escapeHtml(fact.source)}）</span></li>`
    )).join("");
    const questions = (kit.rehearsal_questions || []).map((q) => `<li>${escapeHtml(q)}</li>`).join("");
    const warnings = (kit.warnings || []).map((w) => `<p class="muted">注意：${escapeHtml(w)}</p>`).join("");
    box.hidden = false;
    box.innerHTML =
      `<h4>定制简历摘要</h4><p>${escapeHtml(kit.summary)}</p>` +
      `<h4>岗位开场</h4><p>${escapeHtml(kit.opener)}</p>` +
      `<h4>预演问题</h4><ul>${questions || '<li class="muted">无</li>'}</ul>` +
      `<h4>事实来源（全部来自简历原文）</h4><ul>${facts || '<li class="muted">无可引用事实</li>'}</ul>` +
      warnings +
      `<div class="jd-kit-actions">` +
      `<button type="button" class="secondary" data-jd-action="copy">复制 Markdown</button>` +
      `<button type="button" class="secondary" data-jd-action="download">下载 Markdown</button>` +
      `<span class="muted">不提供自动投递：请自行确认后到目标平台提交。</span></div>`;
  }

  function copyTextFallback(text) {
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.style.position = "fixed";
    ta.style.opacity = "0";
    document.body.appendChild(ta);
    ta.select();
    let ok = false;
    try {
      ok = document.execCommand("copy");
    } catch (ignored) {
      ok = false;
    }
    ta.remove();
    return ok;
  }

  async function copyKitMarkdown() {
    const text = jdState.kitMarkdown;
    if (!text) {
      return;
    }
    try {
      await navigator.clipboard.writeText(text);
    } catch (ignored) {
      if (!copyTextFallback(text)) {
        showNotice("error", "复制失败，请使用下载 Markdown。", { timeout: 2200 });
        return;
      }
    }
    showNotice("ok", "申请材料包已复制到剪贴板。", { timeout: 1600 });
  }

  function downloadKitMarkdown() {
    const text = jdState.kitMarkdown;
    if (!text) {
      return;
    }
    const blob = new Blob([text], { type: "text/markdown;charset=utf-8" });
    const url = URL.createObjectURL(blob);
    const link = document.createElement("a");
    link.href = url;
    link.download = `application-kit-${jdState.id || "jd"}.md`;
    document.body.appendChild(link);
    link.click();
    link.remove();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  }

  function setSetupBusy(busy) {
    ["target-role", "jd-text", "resume-text", "candidate-name", "max-primary", "max-followups", "domain"].forEach((id) => {
      $(id).disabled = busy;
    });
    $("start-btn").disabled = busy;
  }

  async function startSessionRun() {
    try {
      await withLockRetry(() => api(`/v1/sessions/${state.sessionId}/start`, {
        method: "POST",
        headers: { "Idempotency-Key": `demo-start-${state.sessionId}` },
        body: "{}",
      }));
      setStatus("已入队，等待首题…", "running");
      $("phase-text").textContent = "准备出题";
      showThinking("正在生成首题…");
    } catch (err) {
      showNotice("error", "启动失败：" + humanError(err), {
        key: "start-error",
        actionLabel: "重试启动",
        onAction: startSessionRun,
      });
      setStatus("启动失败", "error");
    }
  }

  async function startInterview(event) {
    if (event) {
      event.preventDefault();
    }
    if (state.sessionId) {
      return;
    }
    const role = $("target-role").value.trim();
    const jd = $("jd-text").value.trim();
    if (!role || !jd) {
      $("setup-error").hidden = false;
      $("setup-error").textContent = "目标岗位与 JD 为必填项。";
      return;
    }
    $("setup-error").hidden = true;
    const primary = Number($("max-primary").value);
    const followups = Number($("max-followups").value);
    setSetupBusy(true);
    setStatus("正在创建会话…", "running");

    try {
      const created = await api("/v1/sessions", {
        method: "POST",
        body: JSON.stringify({
          candidate_name: $("candidate-name").value.trim() || "演示候选人",
          target_role: role,
          resume_text: $("resume-text").value.trim(),
          jd_text: jd,
          domain: $("domain").value,
          config: {
            max_primary_questions: primary,
            max_followups_per_question: followups,
            max_depth: 2,
            max_total_followups: Math.max(primary, primary * followups),
            duration_minutes: 30,
            difficulty: "mid",
            locale: "zh-CN",
          },
        }),
      });
      state.sessionId = created.session_id;
      state.rubric = created.rubric || null;
      state.config = created.config || {
        max_primary_questions: primary,
        max_followups_per_question: followups,
        max_total_followups: Math.max(primary, primary * followups),
      };
      state.role = role;
      state.primaryAsked = 0;
      state.followupsUsed = 0;
      resetStepper(state.config.max_primary_questions || primary);
      $("session-meta").hidden = false;
      $("session-id").textContent = state.sessionId;
      hideAnalysisResults();
      hideEmptyState();
      $("setup-block").hidden = true;
      $("session-summary").hidden = false;
      $("progress-strip").hidden = false;
      startElapsedTimer(created.created_at);
      finishBar.hidden = false;
      connectEvents();
    } catch (err) {
      $("setup-error").hidden = false;
      $("setup-error").textContent = humanError(err);
      setStatus("启动失败", "error");
      setSetupBusy(false);
      return;
    }
    await startSessionRun();
  }

  function restartInterview() {
    if (state.sampleMode) {
      loadSample();
      return;
    }
    closeStream();
    stopElapsedTimer();
    state.sessionId = null;
    state.pendingTurn = null;
    state.seen = new Set();
    state.busy = false;
    state.streamClosed = false;
    state.reportLoaded = false;
    state.canFinish = false;
    state.config = null;
    state.rubric = null;
    state.role = "";
    state.startedAt = null;
    state.primaryAsked = 0;
    state.followupsUsed = 0;
    resetStepper(0);
    stepperEl.hidden = true;
    stepperHint.textContent = "";
    stepperCompact.textContent = "";
    state.lastEvalCard = null;
    state.eventCount = 0;
    state.historyCount = 0;
    state.reportSectionsOpen = {};
    state.reviewOpen = {};
    state.reportCost = null;

    emptyState.hidden = false;
    currentQuestionEl.hidden = true;
    currentQuestionEl.innerHTML = "";
    resultSlotEl.innerHTML = "";
    historyBody.innerHTML = "";
    historyBlock.hidden = true;
    historyCountEl.textContent = "0";
    eventBody.innerHTML = "";
    eventDetails.hidden = true;
    eventCountEl.textContent = "0";
    noticesEl.innerHTML = "";
    answerBox.hidden = true;
    answerInput.value = "";
    answerInput.disabled = false;
    submitBtn.disabled = false;
    finishBar.hidden = true;
    finishBtn.disabled = false;
    finishBtn.classList.remove("pulse");
    reportPanel.hidden = true;
    reportMeta.innerHTML = "";
    reportCharts.innerHTML = "";
    runInfo.hidden = true;
    runInfoBody.innerHTML = "";
    reportSections.innerHTML = "";
    reportStatus.textContent = "正在生成…";
    $("report-sample-tag").hidden = true;
    $("session-meta").hidden = true;
    $("session-id").textContent = "";
    $("progress-strip").hidden = true;
    $("session-summary").hidden = true;
    $("setup-block").hidden = false;
    $("setup-error").hidden = true;
    $("setup-error").textContent = "";
    resetResumeUpload();
    hideAnalysisResults();
    $("restart-btn").textContent = "重开一场";
    setSetupBusy(false);
    setStatus("未开始", "idle");
    $("phase-text").textContent = "创建会话后这里显示阶段";
    setSSE("SSE 未连接", "idle");
  }

  async function submitAnswer() {
    const text = answerInput.value.trim();
    if (!text || !state.pendingTurn || state.busy) {
      return;
    }
    const turnId = state.pendingTurn;
    state.pendingTurn = null;
    state.busy = true;
    submitBtn.disabled = true;
    answerInput.disabled = true;

    const answerCard = card("card-answer");
    answerCard.appendChild(cardHead("你的回答", [{ text: `${text.length} 字` }]));
    const body = document.createElement("div");
    body.className = "card-body";
    body.innerHTML = clampHtml(text, 3);
    answerCard.appendChild(body);
    currentQuestionEl.appendChild(answerCard);
    refreshClamps();

    showThinking("正在评估作答…");
    try {
      await withLockRetry(() => api(`/v1/sessions/${state.sessionId}/answers`, {
        method: "POST",
        headers: { "Idempotency-Key": `demo-answer-${turnId}` },
        body: JSON.stringify({ turn_id: turnId, answer: text }),
      }));
      answerInput.value = "";
      setStatus("正在评估作答", "running");
    } catch (err) {
      removeThinking();
      showNotice("error", "提交失败：" + humanError(err), {
        key: "submit-error",
        actionLabel: "重试提交",
        onAction: () => {
          answerInput.value = text;
          state.pendingTurn = turnId;
          submitAnswer();
        },
      });
      answerInput.disabled = false;
      submitBtn.disabled = false;
      answerBox.hidden = false;
    } finally {
      state.busy = false;
    }
  }

  async function finishInterview() {
    if (state.busy || !state.sessionId) {
      return;
    }
    state.busy = true;
    finishBtn.disabled = true;
    reportPanel.hidden = false;
    reportStatus.textContent = "正在生成报告…";
    reportMeta.innerHTML = "";
    reportSections.innerHTML = '<div class="decision-note">报告生成中，实时分段会通过 SSE 推送…</div>';
    setStatus("正在生成报告", "running");
    $("phase-text").textContent = "正在生成报告";
    answerBox.hidden = true;
    finishBtn.classList.remove("pulse");
    reportPanel.scrollIntoView({ behavior: "smooth", block: "start" });

    try {
      await withLockRetry(() => api(`/v1/sessions/${state.sessionId}/finish`, {
        method: "POST",
        headers: { "Idempotency-Key": `demo-finish-${state.sessionId}` },
        body: "{}",
      }));
    } catch (err) {
      showNotice("error", "收尾失败：" + humanError(err), {
        key: "finish-error",
        actionLabel: "重试收尾",
        onAction: finishInterview,
      });
      reportStatus.textContent = "收尾失败";
      setStatus("收尾失败", "error");
    } finally {
      finishBtn.disabled = false;
      state.busy = false;
    }
  }

  // ---------- wiring ----------

  $("setup-form").addEventListener("submit", startInterview);
  $("restart-btn").addEventListener("click", restartInterview);
  $("sample-btn").addEventListener("click", loadSample);

  const resumeDrop = $("resume-drop");
  const resumeFile = $("resume-file");
  resumeDrop.addEventListener("click", () => resumeFile.click());
  // The hidden input's click bubbles back up to the dropzone; stop it so
  // opening the picker cannot re-trigger itself.
  resumeFile.addEventListener("click", (event) => event.stopPropagation());
  resumeDrop.addEventListener("keydown", (event) => {
    if (event.key === "Enter" || event.key === " ") {
      event.preventDefault();
      resumeFile.click();
    }
  });
  resumeFile.addEventListener("change", () => {
    const file = resumeFile.files && resumeFile.files[0];
    resumeFile.value = "";
    if (file) {
      handleResumeFile(file);
    }
  });
  ["dragenter", "dragover"].forEach((type) => {
    resumeDrop.addEventListener(type, (event) => {
      event.preventDefault();
      resumeDrop.classList.add("dragging");
    });
  });
  ["dragleave", "drop"].forEach((type) => {
    resumeDrop.addEventListener(type, (event) => {
      event.preventDefault();
      resumeDrop.classList.remove("dragging");
    });
  });
  resumeDrop.addEventListener("drop", (event) => {
    const file = event.dataTransfer && event.dataTransfer.files && event.dataTransfer.files[0];
    if (file) {
      handleResumeFile(file);
    }
  });

  $("diagnose-btn").addEventListener("click", () => runResumeDiagnosis());
  $("jd-analyze-btn").addEventListener("click", runJDAnalysis);
  $("jd-result").addEventListener("click", (event) => {
    const btn = event.target.closest("[data-jd-action]");
    if (!btn) {
      return;
    }
    const action = btn.getAttribute("data-jd-action");
    if (action === "kit") {
      runApplyKit();
    } else if (action === "copy") {
      copyKitMarkdown();
    } else if (action === "download") {
      downloadKitMarkdown();
    }
  });
  $("diagnose-result").addEventListener("click", (event) => {
    const btn = event.target.closest(".diag-confirm-btn");
    if (!btn) {
      return;
    }
    const index = btn.getAttribute("data-segment");
    const select = $("diagnose-result").querySelector(`.diag-type-select[data-segment="${index}"]`);
    if (!select) {
      return;
    }
    const overrides = {};
    overrides[index] = select.value;
    runResumeDiagnosis(overrides);
  });
  submitBtn.addEventListener("click", submitAnswer);
  finishBtn.addEventListener("click", finishInterview);

  reviewEntry.addEventListener("click", openReview);
  $("review-close").addEventListener("click", () => {
    reviewPanel.hidden = true;
  });
  reviewPanel.addEventListener("click", (event) => {
    const gradeBtn = event.target.closest("[data-grade]");
    if (gradeBtn) {
      gradeReview(Number(gradeBtn.getAttribute("data-grade")));
      return;
    }
    const actionBtn = event.target.closest("[data-review-action]");
    if (!actionBtn) {
      return;
    }
    const action = actionBtn.getAttribute("data-review-action");
    if (action === "retest") {
      showReviewRetest();
    } else if (action === "skip") {
      reviewIndex += 1;
      renderReviewCard();
    } else if (action === "close") {
      reviewPanel.hidden = true;
    }
  });
  refreshReviewEntry();

  document.addEventListener("toggle", refreshClamps, true);
  document.addEventListener("click", (event) => {
    const btn = event.target.closest("[data-expand]");
    if (!btn) {
      return;
    }
    event.preventDefault();
    event.stopPropagation();
    const target = $(btn.dataset.expand);
    if (!target) {
      return;
    }
    const expanded = target.classList.toggle("is-expanded");
    btn.textContent = expanded ? "收起" : "展开全文";
  });
  answerInput.addEventListener("keydown", (event) => {
    if ((event.ctrlKey || event.metaKey) && event.key === "Enter") {
      submitAnswer();
    }
  });

  if (new URLSearchParams(location.search).get("sample") === "1") {
    loadSample();
  }
})();
