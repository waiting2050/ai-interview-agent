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
	GenerateWithTools(ctx context.Context, ev tools.Bundle, exec ToolExecutor, sink EventSink) (Outcome, error)
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

// GenerateWithTools runs the bounded model↔tool loop. The deterministic fallback
// is always available: any budget, transport or output failure returns a
// complete report instead of failing the run.
func (b *Builder) GenerateWithTools(ctx context.Context, ev tools.Bundle, exec ToolExecutor, sink EventSink) (Outcome, error) {
	if sink == nil {
		sink = b.sink
	}
	emit := func(kind string, payload map[string]any) {
		if sink != nil {
			sink(Event{Type: kind, Payload: payload})
		}
	}

	now := b.now().UTC()
	base := Fallback(ev, now)
	base.ModelName = b.modelName
	base.PromptVersion = prompt.ReportVersion

	out := Outcome{Report: base, ModelName: b.modelName, PromptVersion: prompt.ReportVersion, ToolMode: "autonomous"}
	out.Report.ToolMode = "autonomous"

	if ev.Overview.AnsweredTurns == 0 {
		out.Report.Degraded = true
		out.Report.ErrorCode = ErrorCodeNoAnswers
		out.Degraded = true
		out.ErrorCode = ErrorCodeNoAnswers
		out.Report.Sections = out.Report.RenderSections()
		return out, nil
	}

	if !b.modelAvailable {
		emit(EventToolDegraded, map[string]any{"reason": ReasonHeuristic})
		// The heuristic provider is deterministic by construction: assemble the
		// report from evaluation data without spending a model call.
		out.Report = base
		out.Report.Degraded = false
		out.Report.ErrorCode = ""
		out.Report.ToolCalls = ev.Calls
		out.Report.ToolBreakdown = ev.ToolBreakdown
		out.Report.ToolCallsDetail = details(ev.ToolCalls)
		out.ToolDegradedReason = ReasonHeuristic
		out.Report.ToolDegradeReason = ReasonHeuristic
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

	overviewJSON, _ := json.Marshal(ev.Overview)
	user := prompt.ReportToolUser(buildPromptInput(ev), string(overviewJSON))
	dims := make([]prompt.DimensionSpec, 0, len(ev.Overview.Dimensions))
	for _, d := range ev.Overview.Dimensions {
		dims = append(dims, prompt.DimensionSpec{Key: d.Dimension, Name: d.Name})
	}
	system := prompt.ReportToolSystem(dims...)

	var (
		rounds       int
		invalidCount int
		degrade      string
		transcript   []string
	)

	appendTurn := func(text string) {
		transcript = append(transcript, text)
		if len(transcript) > 20 {
			transcript = transcript[len(transcript)-20:]
		}
	}

	for rounds < cfg.MaxRounds {
		if runCtx.Err() != nil {
			return b.budgetFallback(out, ev, rounds, exec, emit)
		}
		rounds++
		resp, err := b.client.Generate(runCtx, llm.Request{
			System: system, User: composeUser(user, transcript), Temperature: 0.2, MaxTokens: 1200,
		})
		if err != nil {
			if ctx.Err() != nil {
				return Outcome{}, fmt.Errorf("generate report: %w", err)
			}
			if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
				return b.budgetFallback(out, ev, rounds, exec, emit)
			}
			return b.modelFallback(out, ev, err), nil
		}
		out.Attempts = rounds

		act, parseErr := parseAction(resp.Text)
		if parseErr != nil {
			appendTurn(prompt.ReportToolInvalidActionMessage(parseErr.Error()))
			continue
		}
		if act.Action == "final" {
			generated, err := parseFinal(act)
			if err != nil {
				out.ValidationIssues = []string{err.Error()}
				appendTurn(prompt.ReportRepairMessage([]string{err.Error()}))
				continue
			}
			fatal, warnings := validateGenerated(generated, ev.Overview.Turns)
			if len(fatal) > 0 {
				out.ValidationIssues = issueStrings(fatal)
				appendTurn(prompt.ReportRepairMessage(issueStrings(fatal)))
				continue
			}
			out.ValidationIssues = issueStrings(warnings)
			out.Report = merge(base, generated, ev)
			b.finish(&out, "autonomous", rounds, exec)
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
				return b.budgetFallback(out, ev, rounds, exec, emit)
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
		Temperature: 0.2, MaxTokens: 1200,
	})
	if err == nil {
		out.Attempts = rounds
		if act, parseErr := parseAction(resp.Text); parseErr == nil && act.Action == "final" {
			if generated, genErr := parseFinal(act); genErr == nil {
				fatal, warnings := validateGenerated(generated, ev.Overview.Turns)
				if len(fatal) == 0 {
					out.ValidationIssues = issueStrings(warnings)
					out.Report = merge(base, generated, ev)
					b.finish(&out, "autonomous", rounds, exec)
					out.Report.Sections = out.Report.RenderSections()
					return out, nil
				}
			}
		}
	}
	if ctx.Err() != nil {
		return Outcome{}, fmt.Errorf("generate forced final: %w", err)
	}

	// Forced final failed: the deterministic report is still complete.
	out.Degraded = true
	out.ErrorCode = ReasonOutputInvalid
	out.Report.Degraded = true
	out.Report.ErrorCode = ReasonOutputInvalid
	out.Report.ErrorDetail = "autonomous final failed after " + degrade
	base = Fallback(ev, now)
	base.ModelName = b.modelName
	base.PromptVersion = prompt.ReportVersion
	base.Degraded = true
	base.ErrorCode = ReasonOutputInvalid
	out.Report = base
	b.finish(&out, "autonomous", rounds, exec)
	out.Report.Sections = out.Report.RenderSections()
	return out, nil
}

func (b *Builder) finish(out *Outcome, mode string, rounds int, exec ToolExecutor) {
	out.ToolMode = mode
	out.ToolRounds = rounds
	out.Report.ToolMode = mode
	out.Report.ToolRounds = rounds
	out.Report.Degraded = out.Degraded
	if exec != nil {
		out.Report.ToolCalls = exec.Calls()
		out.Report.ToolBreakdown = exec.Breakdown()
		out.Report.ToolCallsDetail = details(exec.Records())
	}
}

func details(records []tools.CallRecord) []domain.ToolCallDetail {
	rows := make([]domain.ToolCallDetail, 0, len(records))
	for _, rec := range records {
		status := rec.Status
		if status == "" {
			status = tools.StatusOK
		}
		rows = append(rows, domain.ToolCallDetail{
			Tool: rec.Tool, Status: status, Args: rec.Args, DurationMS: rec.DurationMS,
			ResultBytes: rec.ResultBytes, Truncated: rec.Truncated, Round: rec.Round,
		})
	}
	return rows
}

func (b *Builder) budgetFallback(out Outcome, ev tools.Bundle, rounds int, exec ToolExecutor, emit func(string, map[string]any)) (Outcome, error) {
	emit(EventToolDegraded, map[string]any{"reason": ReasonBudget})
	now := b.now().UTC()
	base := Fallback(ev, now)
	base.ModelName = b.modelName
	base.PromptVersion = prompt.ReportVersion
	base.Degraded = true
	base.ErrorCode = ReasonBudget
	base.ToolMode = "autonomous"
	base.ToolRounds = rounds
	base.ToolDegradeReason = ReasonBudget
	if exec != nil {
		base.ToolCalls = exec.Calls()
		base.ToolBreakdown = exec.Breakdown()
		base.ToolCallsDetail = details(exec.Records())
	}
	out.Report = base
	out.Degraded = true
	out.ErrorCode = ReasonBudget
	out.Attempts = rounds
	out.ToolMode = "autonomous"
	out.ToolRounds = rounds
	out.ToolDegradedReason = ReasonBudget
	out.Report.Sections = out.Report.RenderSections()
	return out, nil
}

func (b *Builder) modelFallback(out Outcome, ev tools.Bundle, err error) Outcome {
	now := b.now().UTC()
	base := Fallback(ev, now)
	base.ModelName = b.modelName
	base.PromptVersion = prompt.ReportVersion
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
	case "":
		// Leniency: a bare report object without the wrapper is treated as final.
		act.Action = "final"
		act.Report = json.RawMessage(text)
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
