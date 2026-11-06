(function () {
  "use strict";

  const $ = (id) => document.getElementById(id);
  const DIMENSIONS = [
    ["correctness", "正确性"],
    ["depth", "深度"],
    ["engineering_judgment", "工程判断"],
    ["structure", "表达结构"],
    ["evidence_credibility", "经历可信度"],
  ];
  // Mirrors internal/domain/evaluation. Weights are shown, not just applied.
  const WEIGHTS = {
    correctness: 0.30,
    depth: 0.25,
    engineering_judgment: 0.20,
    structure: 0.15,
    evidence_credibility: 0.10,
  };
  const BONUS_CAP = 8;
  const PENALTY_CAP = -10;
  const DIM_LABEL = Object.fromEntries(DIMENSIONS);
  const EVENT_TYPES = [
    "session.created", "session.started", "session.phase", "session.aborted", "session.completed",
    "session.failed", "question.asked", "answer.received", "evaluation.completed",
    "evaluation.degraded", "decision.made", "run.started", "run.succeeded", "run.failed",
    "run.interrupted", "report.started", "report.section", "report.tool_calls", "report.completed",
  ];

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
  };

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
    DIMENSIONS.forEach(([key, label]) => {
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
    const values = DIMENSIONS.map(([key]) => d.scores[key]);
    const filled = values.filter((v) => v != null);
    const allEqual = filled.length === DIMENSIONS.length && filled.every((v) => v === filled[0]);
    let line;
    if (allEqual) {
      line = `五维均 ${filled[0]} 分 → ${filled[0]}/5×100 = ${d.base.toFixed(1)}`;
    } else {
      line = `五维 ${values.map((v) => (v == null ? "-" : v)).join("/")}，按 0.30 / 0.25 / 0.20 / 0.15 / 0.10 权重折算 → base ${d.base.toFixed(1)}`;
    }
    if (d.bonus === 0 && d.penalty === 0) {
      line += "，无加分扣分。";
    } else {
      line += `，加分 +${d.bonus.toFixed(1)}、扣分 ${d.penalty.toFixed(1)} → total ${d.total.toFixed(1)}。`;
    }
    return line;
  }

  function scoreTableHtml(d) {
    const rows = DIMENSIONS.map(([key, label]) => {
      const raw = d.scores[key];
      if (raw == null) {
        return "";
      }
      const weight = WEIGHTS[key] || 0;
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
      `<li>${clampHtml(e.quote, 3)}<span class="dim-tag">支持：${escapeHtml(DIM_LABEL[e.dimension] || e.dimension || "未标注")}</span>` +
      `${e.reason ? `<p class="evidence-reason">${escapeHtml(e.reason)}</p>` : ""}</li>`;
    if (dims.size < 2) {
      return `<ul class="evidence-list">${unique.map(item).join("")}</ul>`;
    }
    const groups = DIMENSIONS
      .map(([key, label]) => ({ label, items: unique.filter((e) => e.dimension === key) }))
      .filter((g) => g.items.length > 0);
    const unknown = unique.filter((e) => !DIM_LABEL[e.dimension]);
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

  function onEvaluation(p) {
    removeThinking();
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
    const size = 150;
    const cx = 75;
    const cy = 75;
    const r = 58;
    const circ = 2 * Math.PI * r;
    const pct = Math.max(0, Math.min(100, Number(score || 0))) / 100;
    return `<svg viewBox="0 0 ${size} ${size}" width="${size}" role="img" aria-label="总分 ${score}">` +
      `<circle cx="${cx}" cy="${cy}" r="${r}" fill="none" stroke="#e6ecf5" stroke-width="12"/>` +
      `<circle cx="${cx}" cy="${cy}" r="${r}" fill="none" stroke="#3452e1" stroke-width="12" stroke-linecap="round" ` +
      `stroke-dasharray="${(circ * pct).toFixed(1)} ${circ.toFixed(1)}" transform="rotate(-90 ${cx} ${cy})"/>` +
      `<text x="${cx}" y="${cy + 2}" text-anchor="middle" class="ring-value">${Number(score || 0).toFixed(1)}</text>` +
      `<text x="${cx}" y="${cy + 26}" text-anchor="middle" class="ring-label">/ 100</text></svg>`;
  }

  function radarSvg(dims) {
    const size = 250;
    const cx = 125;
    const cy = 118;
    const r = 80;
    const angle = (i) => -Math.PI / 2 + i * 2 * Math.PI / 5;
    const point = (i, ratio) => {
      const x = cx + Math.cos(angle(i)) * r * ratio;
      const y = cy + Math.sin(angle(i)) * r * ratio;
      return `${x.toFixed(1)},${y.toFixed(1)}`;
    };
    const values = DIMENSIONS.map(([key]) => {
      const found = (dims || []).find((d) => d.dimension === key);
      return Math.max(0, Math.min(100, Number(found ? found.score : 0))) / 100;
    });
    const rings = [0.25, 0.5, 0.75, 1].map((g) =>
      `<polygon points="${[0, 1, 2, 3, 4].map((i) => point(i, g)).join(" ")}" fill="none" stroke="#e3e9f3" stroke-width="1"/>`).join("");
    const axes = [0, 1, 2, 3, 4].map((i) => {
      const p = point(i, 1).split(",");
      return `<line x1="${cx}" y1="${cy}" x2="${p[0]}" y2="${p[1]}" stroke="#e3e9f3"/>`;
    }).join("");
    const shape = `<polygon points="${[0, 1, 2, 3, 4].map((i) => point(i, values[i])).join(" ")}" ` +
      `fill="rgba(52,82,225,0.16)" stroke="#3452e1" stroke-width="2"/>`;
    const labels = DIMENSIONS.map(([, label], i) => {
      const lx = cx + Math.cos(angle(i)) * (r + 22);
      const ly = cy + Math.sin(angle(i)) * (r + 22);
      return `<text x="${lx.toFixed(0)}" y="${ly.toFixed(0)}" text-anchor="middle" dominant-baseline="middle" ` +
        `font-size="13" fill="#5b6b82">${escapeHtml(label)}</text>`;
    }).join("");
    return `<svg viewBox="0 0 ${size} 246" width="${size}" role="img" aria-label="维度雷达">${rings}${axes}${shape}${labels}</svg>`;
  }

  function trendSvg(questions) {
    const list = questions || [];
    if (list.length === 0) {
      return '<p class="decision-note">暂无逐题数据。</p>';
    }
    const width = 880;
    const height = 176;
    const base = 132;
    const top = 24;
    const barW = 22;
    const step = list.length > 1 ? (width - 120) / (list.length - 1) : 0;
    const bars = list.map((q, i) => {
      const score = Math.max(0, Math.min(100, Number(q.score || 0)));
      const h = (score / 100) * (base - top);
      const x = list.length > 1 ? 60 + i * step - barW / 2 : width / 2 - barW / 2;
      const y = base - h;
      return `<rect x="${x}" y="${y}" width="${barW}" height="${h}" rx="3" fill="#8fa3c8"></rect>` +
        `<text x="${x + barW / 2}" y="${y - 6}" text-anchor="middle" font-size="13" fill="#24324a">${score.toFixed(1)}</text>` +
        `<text x="${x + barW / 2}" y="${base + 20}" text-anchor="middle" font-size="13" fill="#5b6b82">第${q.seq}题</text>`;
    }).join("");
    return `<svg viewBox="0 0 ${width} ${height}" width="100%" role="img" aria-label="逐题得分趋势">` +
      `<line x1="24" y1="${base}" x2="${width - 16}" y2="${base}" stroke="#c8d3e3"/>${bars}</svg>`;
  }

  function renderReportHero(structured, questions) {
    const score = Number(structured.overall_score || 0);
    const total = questions.length;
    const scored = questions.filter((q) => !q.degraded).length;
    const coverage = total > 0 ? Math.round(scored / total * 100) : 0;
    const meta = [
      ["总分", score.toFixed(1), ""],
      ["等级", structured.score_band || bandLabel(score), ""],
      ["题数", `${total} 题`, ""],
      ["覆盖率", `${coverage}%`, "有效评分题 / 总题数"],
    ];
    reportMeta.innerHTML = meta.map(([label, value, tip]) =>
      `<div class="meta-box"${tip ? ` title="${escapeHtml(tip)}"` : ""}>${escapeHtml(label)}` +
      `<strong>${escapeHtml(value)}</strong></div>`).join("");
    reportCharts.innerHTML =
      `<div class="chart-card"><div class="chart-title">总分进度</div>${ringSvg(score)}` +
      `<div class="ring-caption">加权总分 · ${escapeHtml(structured.score_band || bandLabel(score))}</div></div>` +
      `<div class="chart-card"><div class="chart-title">维度雷达</div>${radarSvg(structured.dimensions || [])}</div>` +
      `<div class="chart-card wide"><div class="chart-title">逐题得分趋势</div>${trendSvg(questions)}</div>`;
  }

  function renderRunInfo(cost) {
    if (!cost || (cost.model_calls == null && cost.total_tokens == null)) {
      runInfo.hidden = true;
      return;
    }
    const parts = [];
    if (cost.model_calls != null) parts.push(`模型调用 ${cost.model_calls} 次`);
    if (cost.total_tokens != null) {
      parts.push(`Token ${cost.total_tokens}（in ${cost.prompt_tokens || 0} / out ${cost.completion_tokens || 0}）`);
    }
    if (cost.tool_calls != null) parts.push(`工具调用 ${cost.tool_calls} 次`);
    if (cost.model_name) parts.push(`模型 ${cost.model_name}`);
    if (cost.prompt_version) parts.push(`Prompt ${cost.prompt_version}`);
    runInfoBody.innerHTML = parts.map((text) => `<span>${escapeHtml(text)}</span>`).join("");
    runInfo.hidden = false;
  }

  function sectionPreview(section, questions) {
    const idx = Number(section.index);
    if (idx === 1) {
      return "五维加权得分与短板说明，点击查看明细";
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

  function renderReport(dto) {
    const structured = dto.structured || {};
    const sections = structured.sections || [];
    const questions = structured.questions || [];
    reportPanel.hidden = false;
    reportStatus.textContent = dto.degraded ? "已生成（降级拼装）" : "已生成";
    renderReportHero(structured, questions);
    renderRunInfo(Object.assign({}, structured, state.reportCost || {}));

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
    reportPanel.scrollIntoView({ behavior: "smooth", block: "start" });
  }

  // ---------- actions ----------

  function setSetupBusy(busy) {
    ["target-role", "jd-text", "resume-text", "candidate-name", "max-primary", "max-followups"].forEach((id) => {
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
      state.config = created.config || {
        max_primary_questions: primary,
        max_followups_per_question: followups,
        max_total_followups: Math.max(primary, primary * followups),
      };
      state.role = role;
      state.primaryAsked = 0;
      state.followupsUsed = 0;
      $("session-meta").hidden = false;
      $("session-id").textContent = state.sessionId;
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
    state.role = "";
    state.startedAt = null;
    state.primaryAsked = 0;
    state.followupsUsed = 0;
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
    $("session-meta").hidden = true;
    $("session-id").textContent = "";
    $("progress-strip").hidden = true;
    $("session-summary").hidden = true;
    $("setup-block").hidden = false;
    $("setup-error").hidden = true;
    $("setup-error").textContent = "";
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
  submitBtn.addEventListener("click", submitAnswer);
  finishBtn.addEventListener("click", finishInterview);
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
})();
