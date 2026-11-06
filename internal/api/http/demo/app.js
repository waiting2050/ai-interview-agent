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
  };

  const threadEl = $("thread");
  const noticesEl = $("notices");
  const answerBox = $("answer-box");
  const answerInput = $("answer-input");
  const submitBtn = $("submit-answer");
  const finishBar = $("finish-bar");
  const finishBtn = $("finish-btn");
  const reportPanel = $("report-panel");
  const reportStatus = $("report-status");
  const reportMeta = $("report-meta");
  const reportSections = $("report-sections");

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

  function appendCard(el) {
    const empty = $("empty-state");
    if (empty) {
      empty.remove();
    }
    threadEl.appendChild(el);
    el.scrollIntoView({ behavior: "smooth", block: "nearest" });
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
    appendCard(el);
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
    "session.started": () => {
      setStatus("面试进行中", "running");
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
      appendCard(el);
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
      renderReportMeta(p);
      if (state.sessionId) {
        loadReport(3);
      }
    },
    "session.completed": () => {
      closeStream();
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
    const followup = Boolean(p.is_followup);
    const el = card("card-question");
    el.appendChild(cardHead(`第 ${p.seq || "?"} 题`, [
      { text: followup ? "追问" : "主问题", kind: followup ? "followup" : "primary" },
      { text: `深度 ${p.depth || 1}` },
    ]));
    if (p.topic) {
      const topic = document.createElement("div");
      topic.className = "muted";
      topic.textContent = "主题：" + p.topic;
      el.appendChild(topic);
    }
    const body = document.createElement("div");
    body.className = "card-body question-text";
    body.textContent = p.text || "";
    el.appendChild(body);
    appendCard(el);

    answerBox.hidden = false;
    $("answer-context").textContent = `当前待答：第 ${p.seq || "?"} 题${followup ? "（追问）" : ""} · turn ${String(p.turn_id).slice(0, 8)}`;
    answerInput.disabled = false;
    answerInput.value = "";
    submitBtn.disabled = false;
    answerInput.focus();
    setStatus("等待作答", "running");
    $("phase-text").textContent = "等待作答";
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

  function onEvaluation(p) {
    removeThinking();
    const el = card("card-eval");
    el.appendChild(cardHead("评估结果", [{ text: `turn ${String(p.turn_id || "").slice(0, 8)}` }]));
    const score = Number(p.total_score || 0);
    const row = document.createElement("div");
    row.className = "eval-score-row";
    const scoreEl = document.createElement("span");
    scoreEl.className = "eval-score";
    scoreEl.textContent = score.toFixed(1);
    const band = document.createElement("span");
    band.className = "eval-band";
    band.textContent = `${bandLabel(score)} · 加权总分 / 100`;
    row.appendChild(scoreEl);
    row.appendChild(band);
    el.appendChild(row);
    el.appendChild(dimensionGrid(p.scores));

    const missing = p.missing_points || [];
    if (missing.length > 0) {
      const box = document.createElement("div");
      box.className = "missing";
      box.innerHTML = `<strong>${missing.length} 个要点未覆盖</strong><ul>${missing.map((m) => `<li>${escapeHtml(m)}</li>`).join("")}</ul>`;
      el.appendChild(box);
    } else {
      const ok = document.createElement("div");
      ok.className = "missing";
      ok.style.background = "var(--ok-soft)";
      ok.innerHTML = "<strong>要点覆盖完整</strong><ul><li>本轮没有明显遗漏的关键点。</li></ul>";
      el.appendChild(ok);
    }
    if (p.model || p.prompt_version) {
      const meta = document.createElement("div");
      meta.className = "muted";
      meta.style.marginTop = "8px";
      meta.textContent = `模型：${p.model || "-"} · prompt：${p.prompt_version || "-"}`;
      el.appendChild(meta);
    }
    appendCard(el);
    submitBtn.disabled = true;
    answerBox.hidden = true;
  }

  function onEvaluationDegraded(p) {
    removeThinking();
    const el = card("card-eval degraded");
    el.appendChild(cardHead("评估降级", [{ text: reasonLabel(p.code) }]));
    const body = document.createElement("div");
    body.className = "card-body";
    body.textContent = "本轮模型输出不可用，评分已跳过；该题不计入总分，系统会继续推进。";
    el.appendChild(body);
    appendCard(el);
    showNotice("warn", `评估降级：${reasonLabel(p.code)}。该题不计分，流程继续。`, { key: "eval-degraded" });
    submitBtn.disabled = true;
    answerBox.hidden = true;
  }

  function onDecision(p) {
    const actionLabel = { followup: "深挖追问", next_question: "进入下一题", finish: "题纲完成，准备收尾" }[p.action] || p.action || "决策";
    const el = card("card-event");
    el.appendChild(cardHead("评估决策", [{ text: actionLabel }]));
    if (p.reason) {
      const body = document.createElement("div");
      body.className = "card-body muted";
      body.textContent = p.reason;
      el.appendChild(body);
    }
    appendCard(el);

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

  function renderReportMeta(p) {
    const score = Number(p.overall_score || 0);
    const meta = [
      ["综合得分", `${score.toFixed(1)} / 100`],
      ["等级", bandLabel(score)],
      ["模型调用", `${p.model_calls == null ? "-" : p.model_calls} 次`],
      ["Token", `${p.total_tokens == null ? "-" : p.total_tokens}（in ${p.prompt_tokens || 0} / out ${p.completion_tokens || 0}）`],
    ];
    reportMeta.innerHTML = "";
    meta.forEach(([label, value]) => {
      const box = document.createElement("div");
      box.className = "meta-box";
      box.innerHTML = `${escapeHtml(label)}<strong>${escapeHtml(value)}</strong>`;
      reportMeta.appendChild(box);
    });
    if (p.degraded) {
      const banner = document.createElement("div");
      banner.className = "notice warn";
      banner.style.width = "100%";
      banner.innerHTML = `报告降级：${escapeHtml(reasonLabel(p.error_code))}，以下内容由已有评估数据拼装。`;
      reportMeta.appendChild(banner);
    }
  }

  function renderLiveSection(p) {
    reportPanel.hidden = false;
    reportStatus.textContent = "正在生成报告…";
    const id = `report-sec-${p.index}`;
    const existing = $(id);
    const wrap = document.createElement("div");
    wrap.className = "report-section";
    wrap.id = id;
    const title = document.createElement("h3");
    title.textContent = p.title || `第 ${p.index + 1} 段`;
    const md = document.createElement("div");
    md.className = "md";
    md.innerHTML = renderMarkdown(p.markdown || "");
    wrap.appendChild(title);
    wrap.appendChild(md);
    if (existing) {
      existing.replaceWith(wrap);
    } else {
      reportSections.appendChild(wrap);
    }
    wrap.scrollIntoView({ behavior: "smooth", block: "nearest" });
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

  function renderReport(dto) {
    const structured = dto.structured || {};
    reportPanel.hidden = false;
    reportStatus.textContent = dto.degraded ? "已生成（降级拼装）" : "已生成";
    renderReportMeta({
      overall_score: structured.overall_score,
      degraded: dto.degraded,
      error_code: structured.error_code,
      model_calls: structured.model_calls,
      total_tokens: structured.total_tokens,
      prompt_tokens: structured.prompt_tokens,
      completion_tokens: structured.completion_tokens,
    });

    reportSections.innerHTML = "";
    const sections = structured.sections || [];
    if (sections.length > 0) {
      sections.forEach((section) => {
        const wrap = document.createElement("div");
        wrap.className = "report-section";
        const title = document.createElement("h3");
        title.textContent = section.title || `第 ${(section.index || 0) + 1} 段`;
        const md = document.createElement("div");
        md.className = "md";
        md.innerHTML = renderMarkdown(section.markdown || "");
        wrap.appendChild(title);
        wrap.appendChild(md);
        reportSections.appendChild(wrap);
      });
    } else if (dto.markdown) {
      const wrap = document.createElement("div");
      wrap.className = "report-section";
      const md = document.createElement("div");
      md.className = "md";
      md.innerHTML = renderMarkdown(dto.markdown);
      wrap.appendChild(md);
      reportSections.appendChild(wrap);
    }
    reportPanel.scrollIntoView({ behavior: "smooth", block: "start" });
  }

  // ---------- actions ----------

  function setSetupDisabled(disabled) {
    ["target-role", "jd-text", "resume-text", "candidate-name", "max-primary", "max-followups"].forEach((id) => {
      $(id).disabled = disabled;
    });
    const btn = $("start-btn");
    btn.disabled = disabled;
    if (disabled) {
      btn.textContent = "会话已创建";
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
    setSetupDisabled(true);
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
      $("session-meta").hidden = false;
      $("session-id").textContent = state.sessionId;
      $("empty-state")?.remove();
      finishBar.hidden = false;
      connectEvents();
      await withLockRetry(() => api(`/v1/sessions/${state.sessionId}/start`, {
        method: "POST",
        headers: { "Idempotency-Key": `demo-start-${state.sessionId}` },
        body: "{}",
      }));
      setStatus("已入队，等待首题…", "running");
      $("phase-text").textContent = "准备出题";
      showThinking("正在生成首题…");
    } catch (err) {
      $("setup-error").hidden = false;
      $("setup-error").textContent = humanError(err);
      setStatus("启动失败", "error");
      setSetupDisabled(Boolean(state.sessionId));
    }
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
    body.textContent = text;
    answerCard.appendChild(body);
    appendCard(answerCard);

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
    reportSections.innerHTML = '<div class="report-section"><div class="md muted">报告生成中，实时分段会通过 SSE 推送…</div></div>';
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
  submitBtn.addEventListener("click", submitAnswer);
  finishBtn.addEventListener("click", finishInterview);
  answerInput.addEventListener("keydown", (event) => {
    if ((event.ctrlKey || event.metaKey) && event.key === "Enter") {
      submitAnswer();
    }
  });
})();
