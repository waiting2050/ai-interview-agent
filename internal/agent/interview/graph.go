// Package interview owns the eino graphs for one interview turn.
//
// The answer graph is a real compose.Graph:
//
//	START -> snapshot -> evaluate -> decide --+-> ask_followup -> END
//	                                          +-> ask_next     -> END
//	                                          +-> END (finish)
//
// Nodes never write business state. Each dependency is provided by the app
// layer, which also wraps them with run_steps recovery, and the app commits the
// graph output plus SSE events in one transaction.
package interview

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cloudwego/eino/compose"
	"github.com/waiting2050/ai-interview-agent/internal/domain/followup"
	"github.com/waiting2050/ai-interview-agent/internal/domain/resume"
	"github.com/waiting2050/ai-interview-agent/internal/domain/session"
)

const (
	nodeSnapshot    = "snapshot"
	nodeEvaluate    = "evaluate"
	nodeDecide      = "decide"
	nodeAskFollowup = "ask_followup"
	nodeAskNext     = "ask_next"
)

// TurnInput is the app -> graph contract for one answer submission. The
// app-provided Snapshot dependency loads the rest of the state.
type TurnInput struct {
	SessionID string `json:"session_id"`
	TurnID    string `json:"turn_id"`
	Answer    string `json:"answer"`
}

// PlanItemView is a read-only outline item carried through the graph.
type PlanItemView struct {
	Seq            int      `json:"seq"`
	Topic          string   `json:"topic"`
	Objective      string   `json:"objective"`
	ExpectedPoints []string `json:"expected_points"`
	Difficulty     string   `json:"difficulty"`
}

// TurnSnapshot is the read-only product of the snapshot node. It is persisted
// by the app so a crash before evaluation replays the same view.
type TurnSnapshot struct {
	SessionID string `json:"session_id"`
	TurnID    string `json:"turn_id"`
	Answer    string `json:"answer"`
	Truncated bool   `json:"truncated"`

	Topic          string   `json:"topic"`
	Question       string   `json:"question"`
	ExpectedPoints []string `json:"expected_points"`
	Depth          int      `json:"depth"`

	FollowupsThisQuestion int            `json:"followups_this_question"`
	FollowupsUsed         int            `json:"followups_used"`
	PlanCursor            int            `json:"plan_cursor"`
	PlanItems             []PlanItemView `json:"plan_items"`
	NextSeq               int            `json:"next_seq"`
	AskedQuestions        []string       `json:"asked_questions"`

	StartedAt time.Time     `json:"started_at"`
	Elapsed   time.Duration `json:"elapsed"`
	Now       time.Time     `json:"now"`

	TargetRole string         `json:"target_role"`
	Difficulty string         `json:"difficulty"`
	Profile    resume.Profile `json:"profile"`
	ResumeText string         `json:"resume_text"`
	JDText     string         `json:"jd_text"`
	Config     session.Config `json:"config"`
}

// Question is the product of either ask node, consumed by the app commit.
type Question struct {
	TurnID         string   `json:"turn_id"`
	Topic          string   `json:"topic"`
	Text           string   `json:"text"`
	Depth          int      `json:"depth"`
	Objective      string   `json:"objective"`
	ExpectedPoints []string `json:"expected_points"`
}

// AnswerDeps are the replaceable node bodies. The app supplies them per run and
// wraps each call with run_steps recovery.
type AnswerDeps struct {
	Snapshot    func(ctx context.Context) (TurnSnapshot, error)
	Evaluate    func(ctx context.Context, snap TurnSnapshot) (Outcome, error)
	Decide      func(ctx context.Context, snap TurnSnapshot, out Outcome) (followup.Decision, error)
	AskFollowup func(ctx context.Context, snap TurnSnapshot, out Outcome, decision followup.Decision) (Question, error)
	AskNext     func(ctx context.Context, snap TurnSnapshot, item PlanItemView) (Question, error)
}

// AnswerState is the single value threaded through every graph node.
type AnswerState struct {
	In       TurnInput         `json:"in"`
	Snapshot TurnSnapshot      `json:"snapshot"`
	Outcome  Outcome           `json:"outcome"`
	Decision followup.Decision `json:"decision"`
	Question Question          `json:"question"`
	Asked    bool              `json:"asked"`

	deps AnswerDeps
}

// AnswerGraph is the compiled answer graph. It is safe for concurrent use;
// per-run state lives in AnswerState, not in the graph.
type AnswerGraph struct {
	runnable compose.Runnable[*AnswerState, *AnswerState]
	err      error
}

// NewAnswerGraph compiles the graph once at startup. The returned value always
// exposes Err so callers can fail fast on a wiring bug.
func NewAnswerGraph(ctx context.Context) *AnswerGraph {
	graph := compose.NewGraph[*AnswerState, *AnswerState]()

	add := func(key string, fn func(context.Context, *AnswerState) (*AnswerState, error)) error {
		return graph.AddLambdaNode(key, compose.InvokableLambda(fn))
	}
	if err := add(nodeSnapshot, snapshotNode); err != nil {
		return &AnswerGraph{err: fmt.Errorf("add node %s: %w", nodeSnapshot, err)}
	}
	if err := add(nodeEvaluate, evaluateNode); err != nil {
		return &AnswerGraph{err: fmt.Errorf("add node %s: %w", nodeEvaluate, err)}
	}
	if err := add(nodeDecide, decideNode); err != nil {
		return &AnswerGraph{err: fmt.Errorf("add node %s: %w", nodeDecide, err)}
	}
	if err := add(nodeAskFollowup, askFollowupNode); err != nil {
		return &AnswerGraph{err: fmt.Errorf("add node %s: %w", nodeAskFollowup, err)}
	}
	if err := add(nodeAskNext, askNextNode); err != nil {
		return &AnswerGraph{err: fmt.Errorf("add node %s: %w", nodeAskNext, err)}
	}

	for _, edge := range [][2]string{
		{compose.START, nodeSnapshot},
		{nodeSnapshot, nodeEvaluate},
		{nodeEvaluate, nodeDecide},
		{nodeAskFollowup, compose.END},
		{nodeAskNext, compose.END},
	} {
		if err := graph.AddEdge(edge[0], edge[1]); err != nil {
			return &AnswerGraph{err: fmt.Errorf("add edge %s -> %s: %w", edge[0], edge[1], err)}
		}
	}

	branch := compose.NewGraphBranch(func(ctx context.Context, state *AnswerState) (string, error) {
		switch state.Decision.Action {
		case followup.ActionFollowUp:
			return nodeAskFollowup, nil
		case followup.ActionNextQuestion:
			return nodeAskNext, nil
		case followup.ActionFinish:
			return compose.END, nil
		default:
			return "", fmt.Errorf("unsupported decision action %q", state.Decision.Action)
		}
	}, map[string]bool{
		nodeAskFollowup: true,
		nodeAskNext:     true,
		compose.END:     true,
	})
	if err := graph.AddBranch(nodeDecide, branch); err != nil {
		return &AnswerGraph{err: fmt.Errorf("add branch from %s: %w", nodeDecide, err)}
	}

	runnable, err := graph.Compile(ctx, compose.WithGraphName("interview_answer"))
	if err != nil {
		return &AnswerGraph{err: fmt.Errorf("compile answer graph: %w", err)}
	}
	return &AnswerGraph{runnable: runnable}
}

// Err reports a compile error. Callers should check it at startup.
func (g *AnswerGraph) Err() error { return g.err }

// Run executes the graph for one answer. The state pointer is mutated in place
// and also returned.
func (g *AnswerGraph) Run(ctx context.Context, in TurnInput, deps AnswerDeps) (*AnswerState, error) {
	if g.err != nil {
		return nil, g.err
	}
	if g.runnable == nil {
		return nil, fmt.Errorf("answer graph is not compiled")
	}
	state := &AnswerState{In: in, deps: deps}
	return g.runnable.Invoke(ctx, state)
}

// StepError marks which run step failed so the app can persist step_name with
// the same names used by run_steps recovery.
type StepError struct {
	Step string
	Err  error
}

func (e *StepError) Error() string { return fmt.Sprintf("step %s: %v", e.Step, e.Err) }
func (e *StepError) Unwrap() error { return e.Err }

// StepName extracts the failed step, defaulting to "graph" for wiring errors.
func StepName(err error) string {
	var stepErr *StepError
	if errors.As(err, &stepErr) {
		return stepErr.Step
	}
	return "graph"
}

func snapshotNode(ctx context.Context, state *AnswerState) (*AnswerState, error) {
	if state.deps.Snapshot == nil {
		return nil, &StepError{Step: nodeSnapshot, Err: fmt.Errorf("snapshot dependency is nil")}
	}
	snap, err := state.deps.Snapshot(ctx)
	if err != nil {
		return nil, &StepError{Step: nodeSnapshot, Err: err}
	}
	state.Snapshot = snap
	return state, nil
}

func evaluateNode(ctx context.Context, state *AnswerState) (*AnswerState, error) {
	if state.deps.Evaluate == nil {
		return nil, &StepError{Step: nodeEvaluate, Err: fmt.Errorf("evaluate dependency is nil")}
	}
	// The persisted input is authoritative after a replay; keep it in sync so a
	// hand-crafted graph test cannot drift from the snapshot.
	state.In.SessionID = state.Snapshot.SessionID
	state.In.TurnID = state.Snapshot.TurnID

	out, err := state.deps.Evaluate(ctx, state.Snapshot)
	if err != nil {
		return nil, &StepError{Step: nodeEvaluate, Err: err}
	}
	state.Outcome = out
	return state, nil
}

func decideNode(ctx context.Context, state *AnswerState) (*AnswerState, error) {
	if state.deps.Decide == nil {
		return nil, &StepError{Step: nodeDecide, Err: fmt.Errorf("decide dependency is nil")}
	}
	decision, err := state.deps.Decide(ctx, state.Snapshot, state.Outcome)
	if err != nil {
		return nil, &StepError{Step: nodeDecide, Err: err}
	}
	state.Decision = decision
	return state, nil
}

func askFollowupNode(ctx context.Context, state *AnswerState) (*AnswerState, error) {
	if state.deps.AskFollowup == nil {
		return nil, &StepError{Step: nodeAskFollowup, Err: fmt.Errorf("ask_followup dependency is nil")}
	}
	question, err := state.deps.AskFollowup(ctx, state.Snapshot, state.Outcome, state.Decision)
	if err != nil {
		return nil, &StepError{Step: nodeAskFollowup, Err: err}
	}
	state.Question = question
	state.Asked = true
	return state, nil
}

func askNextNode(ctx context.Context, state *AnswerState) (*AnswerState, error) {
	if state.deps.AskNext == nil {
		return nil, &StepError{Step: nodeAskNext, Err: fmt.Errorf("ask_next dependency is nil")}
	}
	item, ok := planItemAt(state.Snapshot.PlanItems, state.Snapshot.PlanCursor)
	if !ok {
		return nil, &StepError{Step: nodeAskNext, Err: fmt.Errorf("no plan item at cursor %d", state.Snapshot.PlanCursor)}
	}
	question, err := state.deps.AskNext(ctx, state.Snapshot, item)
	if err != nil {
		return nil, &StepError{Step: nodeAskNext, Err: err}
	}
	state.Question = question
	state.Asked = true
	return state, nil
}

func planItemAt(items []PlanItemView, cursor int) (PlanItemView, bool) {
	if cursor < 0 || cursor >= len(items) {
		return PlanItemView{}, false
	}
	return items[cursor], true
}
