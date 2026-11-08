package report

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/waiting2050/ai-interview-agent/internal/agent/llm"
	"github.com/waiting2050/ai-interview-agent/internal/agent/prompt"
	"github.com/waiting2050/ai-interview-agent/internal/agent/tools"
	domain "github.com/waiting2050/ai-interview-agent/internal/domain/report"
)

// Autonomous event names. They are emitted before report.section so an
// observer sees the tool phase finish before the report is streamed.
const (
	EventToolRound    = "report.tool_round"
	EventToolCall     = "report.tool_call"
	EventToolDenied   = "report.tool_denied"
	EventToolDegraded = "report.tool_degraded"
)

// Degradation reasons for the autonomous loop.
const (
	ReasonUnknownTool   = "unknown_tool"
	ReasonInvalidArgs   = "invalid_args"
	ReasonMaxRounds     = "max_rounds"
	ReasonMaxCalls      = "max_calls"
	ReasonBudget        = "budget_exceeded"
	ReasonHeuristic     = "heuristic"
	ReasonOutputInvalid = "report_output_invalid"
)

// traceRawRunes bounds the raw response kept per model round for audit.
const traceRawRunes = 2000

// Event is one autonomous-loop event for the app to persist. Payload keys
// mirror the design document: tool, status, duration_ms, result_bytes and
// truncated.
type Event struct {
	Type    string
	Payload map[string]any
}

// EventSink receives loop events. Nil disables emission (unit tests that only
// assert the outcome).
type EventSink func(Event)

// ToolExecutor is the shared runtime for both modes. tools.Query implements it.
type ToolExecutor interface {
	Execute(ctx context.Context, tool string, args json.RawMessage) (tools.Result, error)
	Records() []tools.CallRecord
	Calls() int
	Breakdown() map[string]int
}

// BundleSource collects the deterministic evidence used to assemble the report
// skeleton and to validate turn references. The autonomous loop calls it only
// after the model phase, so the model never starts from pre-collected facts.
type BundleSource interface {
	Collect(ctx context.Context) (tools.Bundle, error)
	Records() []tools.CallRecord
	Calls() int
	Breakdown() map[string]int
}

// ToolRoundTrace is one raw model response kept for post-run audit. Truncated
// marks that Raw was shortened to traceRawRunes.
type ToolRoundTrace struct {
	Round            int    `json:"round"`
	Action           string `json:"action,omitempty"`
	Raw              string `json:"raw"`
	Truncated        bool   `json:"truncated"`
	Error            string `json:"error,omitempty"`
	CompletionTokens int    `json:"completion_tokens"`
}

// AutonomousConfig are the three hard limits. Zero values fall back to the
// documented defaults.
type AutonomousConfig struct {
	MaxRounds int
	MaxCalls  int
	Timeout   time.Duration
}

func (c *AutonomousConfig) normalize() {
	if c.MaxRounds <= 0 {
		c.MaxRounds = 6
	}
	if c.MaxCalls <= 0 {
		c.MaxCalls = 12
	}
	if c.Timeout <= 0 {
		c.Timeout = 20 * time.Second
	}
}

// AutonomousGenerator is the optional capability the app uses when
// REPORT_TOOL_MODE=autonomous. The deterministic Generator interface stays
// unchanged so existing callers and tests keep working.
type AutonomousGenerator interface {
	GenerateWithTools(ctx context.Context, sessionID string, dims []string, exec ToolExecutor, assemble BundleSource, sink EventSink) (Outcome, error)
}

// WithAutonomous enables the model-chosen tool loop. available=false models a
// heuristic provider: the loop degrades to the deterministic path and emits
// report.tool_degraded{reason=heuristic}.
func (b *Builder) WithAutonomous(available bool, cfg AutonomousConfig) *Builder {
	cfg.normalize()
	b.autonomous = true
	b.modelAvailable = available
	b.auton = cfg
	return b
}

// WithEventSink installs the default sink used by GenerateWithTools when the
// caller passes nil.
func (b *Builder) WithEventSink(sink EventSink) *Builder {
	b.sink = sink
	return b
}

type action struct {
	Action string          `json:"action"`
	Tool   string          `json:"tool"`
	Args   json.RawMessage `json:"args"`
	Report json.RawMessage `json:"report"`
}

// GenerateWithTools runs the bounded model↔tool loop. Round 1 starts from the
// task, the tool catalog and the session id only: no evidence is pre-collected.
// Evidence is assembled once, after the model phase, to build the report
// skeleton and validate turn ids. The deterministic fallback is always
// available: any budget, transport or output failure returns a complete report
// instead of failing the run.
func (b *Builder) GenerateWithTools(ctx context.Context, sessionID string, dims []string, exec ToolExecutor, assemble BundleSource, sink EventSink) (Outcome, error) {
	if sink == nil {
		sink = b.sink
	}
	emit := func(kind string, payload map[string]any) {
		if sink != nil {
			sink(Event{Type: kind, Payload: payload})
		}
	}

	now := b.now().UTC()
	out := Outcome{ModelName: b.modelName, PromptVersion: prompt.ReportVersion, ToolMode: "autonomous"}
	out.Report.SessionID = sessionID
	out.Report.GeneratedAt = now
	out.Report.ToolMode = "autonomous"

	if !b.modelAvailable {
		emit(EventToolDegraded, map[string]any{"reason": ReasonHeuristic})
		ev, err := b.collectEvidence(ctx, assemble)
		if err != nil {
			return Outcome{}, err
		}
		out.Report = b.baseReport(ev, now)
		if ev.Overview.AnsweredTurns == 0 {
			out.Report.Degraded = true
			out.Report.ErrorCode = ErrorCodeNoAnswers
			out.Degraded = true
			out.ErrorCode = ErrorCodeNoAnswers
		}
		out.ToolDegradedReason = ReasonHeuristic
		out.Report.ToolDegradeReason = ReasonHeuristic
		b.finish(&out, 0, exec, assemble)
		out.Report.Sections = out.Report.RenderSections()
		return out, nil
	}

	cfg := b.auton
	runCtx := ctx
	cancel := func() {}
	if cfg.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, cfg.Timeout)
	}
	defer cancel()

	user := prompt.ReportToolStartUser(sessionID, tools.AvailableTools())
	specs := make([]prompt.DimensionSpec, 0, len(dims))
	for _, key := range dims {
		specs = append(specs, prompt.DimensionSpec{Key: key, Name: key})
	}
	system := prompt.ReportToolSystem(specs...)

	var (
		rounds       int
		invalidCount int
		degrade      string
		transcript   []string
		bundle       *tools.Bundle
	)

	// collect runs once, after the model phase. Assembly uses the parent
	// context so a run-level deadline does not block the deterministic skeleton.
	collect := func() (tools.Bundle, error) {
		if bundle != nil {
			return *bundle, nil
		}
		ev, err := b.collectEvidence(ctx, assemble)
		if err != nil {
			return tools.Bundle{}, err
		}
		bundle = &ev
		return ev, nil
	}

	appendTurn := func(text string) {
		transcript = append(transcript, text)
		if len(transcript) > 20 {
			transcript = transcript[len(transcript)-20:]
		}
	}

	for rounds < cfg.MaxRounds {
		if runCtx.Err() != nil {
			return b.budgetFallback(ctx, out, rounds, exec, assemble, emit)
		}
		rounds++
		resp, err := b.client.Generate(runCtx, llm.Request{
			System: system, User: composeUser(user, transcript), Temperature: 0.2, MaxTokens: ReportMaxTokens,
		})
		if err != nil {
			if ctx.Err() != nil {
				return Outcome{}, fmt.Errorf("generate report: %w", err)
			}
			if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
				return b.budgetFallback(ctx, out, rounds, exec, assemble, emit)
			}
			ev, cErr := collect()
			if cErr != nil {
				return Outcome{}, cErr
			}
			return b.modelFallback(out, ev, err), nil
		}
		out.Attempts = rounds

		act, parseErr := parseAction(resp.Text)
		trace := traceRound(rounds, resp, act, parseErr)
		out.ToolRoundTraces = append(out.ToolRoundTraces, trace)
		if parseErr != nil {
			appendTurn(prompt.ReportToolInvalidActionMessage(parseErr.Error()))
			continue
		}
		if act.Action == "final" {
			generated, err := parseFinal(act)
			if err != nil {
				out.ValidationIssues = []string{err.Error()}
				setTraceError(out.ToolRoundTraces, err.Error())
				appendTurn(prompt.ReportRepairMessage([]string{err.Error()}))
				continue
			}
			ev, cErr := collect()
			if cErr != nil {
				return Outcome{}, cErr
			}
			if ev.Overview.AnsweredTurns == 0 {
				return b.noAnswers(out, ev, now, rounds, exec, assemble)
			}
			fatal, warnings := validateGenerated(generated, ev.Overview.Turns)
			if len(fatal) > 0 {
				issues := issueStrings(fatal)
				out.ValidationIssues = issues
				setTraceError(out.ToolRoundTraces, strings.Join(issues, "；"))
				appendTurn(prompt.ReportRepairMessage(issues))
				continue
			}
			out.ValidationIssues = issueStrings(warnings)
			out.Report = merge(b.baseReport(ev, now), generated, ev)
			b.finish(&out, rounds, exec, assemble)
			out.Report.Sections = out.Report.RenderSections()
			return out, nil
		}

		// A tool action is only legal while rounds remain. The last round must be
		// a final, otherwise the loop degrades and asks for one forced final.
		if rounds >= cfg.MaxRounds {
			degrade = ReasonMaxRounds
			break
		}
		if exec == nil {
			degrade = ReasonMaxCalls
			break
		}
		if cfg.MaxCalls > 0 && exec.Calls() >= cfg.MaxCalls {
			degrade = ReasonMaxCalls
			break
		}

		emit(EventToolRound, map[string]any{"round": rounds, "tool": act.Tool})
		result, execErr := exec.Execute(runCtx, act.Tool, act.Args)
		if execErr != nil {
			if ctx.Err() != nil {
				return Outcome{}, fmt.Errorf("execute tool: %w", execErr)
			}
			if errors.Is(runCtx.Err(), context.DeadlineExceeded) || errors.Is(execErr, tools.ErrTimeout) && runCtx.Err() != nil {
				return b.budgetFallback(ctx, out, rounds, exec, assemble, emit)
			}
			reason := classifyToolError(execErr)
			if reason == tools.StatusTimeout || reason == tools.StatusError {
				emit(EventToolCall, map[string]any{
					"tool": act.Tool, "status": reason, "duration_ms": result.DurationMS,
					"result_bytes": result.ResultBytes, "truncated": false, "round": rounds,
				})
			} else {
				emit(EventToolDenied, map[string]any{"reason": reason, "tool": act.Tool})
			}
			if errors.Is(execErr, tools.ErrBudgetExceeded) {
				if strings.Contains(execErr.Error(), "reached its") {
					// A per-tool cap is a denial, not the total-call limit: tell
					// the model to use another tool or final.
					appendTurn(result.Marshal())
					continue
				}
				degrade = ReasonMaxCalls
				break
			}
			invalidCount++
			appendTurn(result.Marshal())
			if invalidCount >= 3 {
				degrade = ReasonInvalidArgs
				break
			}
			continue
		}
		invalidCount = 0
		emit(EventToolCall, map[string]any{
			"tool": act.Tool, "status": result.Status,
			"duration_ms": result.DurationMS, "result_bytes": result.ResultBytes,
			"truncated": result.Truncated, "round": rounds,
		})
		appendTurn(prompt.ReportToolResultMessage(act.Tool, result.Marshal()))
	}

	if degrade == "" {
		degrade = ReasonMaxRounds
	}
	emit(EventToolDegraded, map[string]any{"reason": degrade})
	out.ToolDegradedReason = degrade
	out.Report.ToolDegradeReason = degrade

	// One forced final with the evidence already collected. This call is not a
	// tool round; it is the loop's last chance to produce model commentary.
	rounds++
	resp, err := b.client.Generate(runCtx, llm.Request{
		System:      system,
		User:        composeUser(user, append(transcript, prompt.ReportToolForceFinalMessage(degrade))),
		Temperature: 0.2, MaxTokens: ReportMaxTokens,
	})
	if err == nil {
		out.Attempts = rounds
		act, parseErr := parseAction(resp.Text)
		out.ToolRoundTraces = append(out.ToolRoundTraces, traceRound(rounds, resp, act, parseErr))
		if parseErr == nil && act.Action == "final" {
			if generated, genErr := parseFinal(act); genErr == nil {
				ev, cErr := collect()
				if cErr != nil {
					return Outcome{}, cErr
				}
				fatal, warnings := validateGenerated(generated, ev.Overview.Turns)
				if len(fatal) == 0 {
					out.ValidationIssues = issueStrings(warnings)
					out.Report = merge(b.baseReport(ev, now), generated, ev)
					out.Report.ToolDegradeReason = degrade
					b.finish(&out, rounds, exec, assemble)
					out.Report.Sections = out.Report.RenderSections()
					return out, nil
				}
				out.ValidationIssues = issueStrings(fatal)
				setTraceError(out.ToolRoundTraces, strings.Join(out.ValidationIssues, "；"))
			} else {
				setTraceError(out.ToolRoundTraces, genErr.Error())
			}
		}
	}
	if ctx.Err() != nil {
		return Outcome{}, fmt.Errorf("generate forced final: %w", err)
	}

	ev, cErr := collect()
	if cErr != nil {
		return Outcome{}, cErr
	}

	// Forced final failed: the deterministic report is still complete.
	out.Degraded = true
	out.ErrorCode = ReasonOutputInvalid
	out.Report = b.baseReport(ev, now)
	out.Report.Degraded = true
	out.Report.ErrorCode = ReasonOutputInvalid
	out.Report.ErrorDetail = "autonomous final failed after " + degrade
	out.Report.ToolDegradeReason = degrade
	b.finish(&out, rounds, exec, assemble)
	out.Report.Sections = out.Report.RenderSections()
	return out, nil
}

func (b *Builder) collectEvidence(ctx context.Context, src BundleSource) (tools.Bundle, error) {
	if src == nil {
		return tools.Bundle{}, errors.New("report: autonomous mode requires an evidence bundle source")
	}
	return src.Collect(ctx)
}

func (b *Builder) baseReport(ev tools.Bundle, now time.Time) domain.Report {
	base := Fallback(ev, now)
	base.ModelName = b.modelName
	base.PromptVersion = prompt.ReportVersion
	base.ToolMode = "autonomous"
	return base
}

func (b *Builder) noAnswers(out Outcome, ev tools.Bundle, now time.Time, rounds int, exec ToolExecutor, assemble BundleSource) (Outcome, error) {
	out.Report = b.baseReport(ev, now)
	out.Report.Degraded = true
	out.Report.ErrorCode = ErrorCodeNoAnswers
	out.Degraded = true
	out.ErrorCode = ErrorCodeNoAnswers
	b.finish(&out, rounds, exec, assemble)
	out.Report.Sections = out.Report.RenderSections()
	return out, nil
}

func traceRound(round int, resp llm.Response, act action, issue error) ToolRoundTrace {
	trace := ToolRoundTrace{
		Round:            round,
		Raw:              truncateRunes(resp.Text, traceRawRunes),
		CompletionTokens: resp.CompletionTokens,
	}
	if len([]rune(resp.Text)) > traceRawRunes {
		trace.Truncated = true
	}
	if act.Action != "" {
		trace.Action = act.Action
	}
	if issue != nil {
		trace.Error = truncateRunes(issue.Error(), 600)
	}
	return trace
}

func setTraceError(traces []ToolRoundTrace, issue string) {
	if len(traces) == 0 {
		return
	}
	traces[len(traces)-1].Error = truncateRunes(issue, 600)
}

func (b *Builder) finish(out *Outcome, rounds int, exec ToolExecutor, assemble BundleSource) {
	out.ToolMode = "autonomous"
	out.ToolRounds = rounds
	out.Report.ToolMode = "autonomous"
	out.Report.ToolRounds = rounds
	out.Report.Degraded = out.Degraded

	records := make([]tools.CallRecord, 0, 32)
	if exec != nil {
		records = append(records, exec.Records()...)
	}
	if assemble != nil {
		records = append(records, assemble.Records()...)
	}
	out.Report.ToolCallsDetail = details(records)
	out.Report.ToolCalls = len(records)
	byTool := map[string]int{}
	for _, rec := range records {
		byTool[rec.Tool]++
	}
	out.Report.ToolBreakdown = byTool
}

func details(records []tools.CallRecord) []domain.ToolCallDetail {
	rows := make([]domain.ToolCallDetail, 0, len(records))
	for _, rec := range records {
		status := rec.Status
		if status == "" {
			status = tools.StatusOK
		}
		rows = append(rows, domain.ToolCallDetail{
			Tool: rec.Tool, Status: status, Section: rec.SectionLabel, Args: rec.Args,
			DurationMS: rec.DurationMS, ResultBytes: rec.ResultBytes, Truncated: rec.Truncated, Round: rec.Round,
		})
	}
	return rows
}

func (b *Builder) budgetFallback(ctx context.Context, out Outcome, rounds int, exec ToolExecutor, assemble BundleSource, emit func(string, map[string]any)) (Outcome, error) {
	emit(EventToolDegraded, map[string]any{"reason": ReasonBudget})
	now := b.now().UTC()
	ev, err := b.collectEvidence(ctx, assemble)
	if err != nil {
		return Outcome{}, err
	}
	base := b.baseReport(ev, now)
	base.Degraded = true
	base.ErrorCode = ReasonBudget
	base.ToolRounds = rounds
	base.ToolDegradeReason = ReasonBudget
	out.Report = base
	out.Degraded = true
	out.ErrorCode = ReasonBudget
	out.Attempts = rounds
	out.ToolMode = "autonomous"
	out.ToolRounds = rounds
	out.ToolDegradedReason = ReasonBudget
	b.finish(&out, rounds, exec, assemble)
	out.Report.Sections = out.Report.RenderSections()
	return out, nil
}

func (b *Builder) modelFallback(out Outcome, ev tools.Bundle, err error) Outcome {
	now := b.now().UTC()
	base := b.baseReport(ev, now)
	base.Degraded = true
	base.ErrorCode = ErrorCodeModelUnavailable
	base.ErrorDetail = "model_unavailable: " + truncateRunes(err.Error(), 300)
	out.Report = base
	out.Degraded = true
	out.ErrorCode = ErrorCodeModelUnavailable
	out.ToolMode = "autonomous"
	out.Report.ToolMode = "autonomous"
	out.Report.Sections = out.Report.RenderSections()
	return out
}

func composeUser(base string, transcript []string) string {
	if len(transcript) == 0 {
		return base
	}
	return base + "\n\n<conversation>\n" + strings.Join(transcript, "\n") + "\n</conversation>"
}

func parseAction(raw string) (action, error) {
	text, err := extractJSONObject(raw)
	if err != nil {
		return action{}, err
	}
	var act action
	if err := json.Unmarshal([]byte(text), &act); err != nil {
		return action{}, fmt.Errorf("动作不是合法 JSON: %v", err)
	}
	act.Action = strings.ToLower(strings.TrimSpace(act.Action))
	switch act.Action {
	case "tool":
		if strings.TrimSpace(act.Tool) == "" {
			return action{}, fmt.Errorf("action=tool 缺少 tool 字段")
		}
	case "final":
		return act, nil
	default:
		return action{}, fmt.Errorf("未知 action %q", act.Action)
	}
	return act, nil
}

func parseFinal(act action) (*generated, error) {
	var out generated
	if len(act.Report) > 0 && !strings.EqualFold(string(act.Report), "null") {
		if err := json.Unmarshal(act.Report, &out); err != nil {
			return nil, fmt.Errorf("$.report: %v", err)
		}
		return &out, nil
	}
	return nil, fmt.Errorf("$.report 缺失")
}

func classifyToolError(err error) string {
	switch {
	case errors.Is(err, tools.ErrUnknownTool):
		return ReasonUnknownTool
	case errors.Is(err, tools.ErrForbidden):
		return "out_of_scope"
	case errors.Is(err, tools.ErrInvalidArgument):
		return ReasonInvalidArgs
	case errors.Is(err, tools.ErrBudgetExceeded):
		return ReasonMaxCalls
	case errors.Is(err, tools.ErrTimeout):
		return tools.StatusTimeout
	default:
		return tools.StatusError
	}
}
