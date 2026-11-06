// Decision rules: hard budget gates -> coverage gate -> model suggestion ->
// final arbitration. The model only advises; deterministic rules win.
package followup

import (
	"strings"
	"time"
)

// Reason names are stable so tests, events and logs can assert on them.
const (
	ReasonTimeLimit             = "time_limit"
	ReasonPlanExhausted         = "plan_exhausted"
	ReasonFollowupBudget        = "followup_budget_exhausted"
	ReasonDepthLimit            = "depth_limit"
	ReasonQuestionFollowupLimit = "question_followup_limit"
	ReasonAnswerUnknown         = "answer_unknown"
	ReasonEvaluationDegraded    = "evaluation_degraded"
	ReasonCoverageSatisfied     = "coverage_satisfied"
	ReasonModelSuggestion       = "model_suggestion"
	ReasonNoFollowupSignal      = "no_followup_signal"
)

// Input is everything the gates may look at. All counters come from the
// persisted session/turn, never from model output.
type Input struct {
	Elapsed    time.Duration
	MaxElapsed time.Duration

	// PlanCursor is the number of primary questions already asked; the
	// current turn is the PlanCursor-th one.
	PlanCursor         int
	RemainingPrimaries int

	Depth                   int
	MaxDepth                int
	FollowupsThisQuestion   int
	MaxFollowupsPerQuestion int
	FollowupsUsed           int
	MaxTotalFollowups       int

	AnswerUnknown         bool
	CoveredAllPoints      bool
	EvaluationDegraded    bool
	SuggestFollowup       bool
	SuggestTarget         string
	SuggestionHasEvidence bool
}

type Decision struct {
	Action Action
	Reason string
	Target string
}

// Decide applies the gates in the documented order. The result is always one
// of follow_up / next_question / finish; when a next question would exceed the
// outline it degrades to finish instead.
func Decide(in Input) Decision {
	if in.MaxElapsed > 0 && in.Elapsed >= in.MaxElapsed {
		return Decision{Action: ActionFinish, Reason: ReasonTimeLimit}
	}
	if in.RemainingPrimaries <= 0 {
		return Decision{Action: ActionFinish, Reason: ReasonPlanExhausted}
	}
	if in.FollowupsUsed >= in.MaxTotalFollowups {
		return next(in, ReasonFollowupBudget, "")
	}
	if in.Depth >= in.MaxDepth {
		return next(in, ReasonDepthLimit, "")
	}
	if in.FollowupsThisQuestion >= in.MaxFollowupsPerQuestion {
		return next(in, ReasonQuestionFollowupLimit, "")
	}
	if in.AnswerUnknown {
		return next(in, ReasonAnswerUnknown, "")
	}
	if in.EvaluationDegraded {
		return next(in, ReasonEvaluationDegraded, "")
	}
	if in.CoveredAllPoints {
		// Coverage is a deterministic gate: when the answer has covered every
		// expected point the interview advances even if the model still wants
		// to follow up. The model only advises.
		return next(in, ReasonCoverageSatisfied, "")
	}
	if in.SuggestFollowup && strings.TrimSpace(in.SuggestTarget) != "" && in.SuggestionHasEvidence {
		return Decision{Action: ActionFollowUp, Reason: ReasonModelSuggestion, Target: in.SuggestTarget}
	}
	return next(in, ReasonNoFollowupSignal, "")
}

func next(in Input, reason, target string) Decision {
	if in.RemainingPrimaries <= 0 {
		return Decision{Action: ActionFinish, Reason: ReasonPlanExhausted}
	}
	return Decision{Action: ActionNextQuestion, Reason: reason, Target: target}
}

// UnknownMarkers are the phrases that mean "I don't know" in Chinese answers.
// They suppress follow-ups so the interview does not interrogate a candidate
// who has explicitly said they have no experience.
var UnknownMarkers = []string{"不会", "不知道", "没做过", "没接触过", "不熟悉", "没了解", "不太清楚", "没经验"}

// IsUnknownAnswer reports whether the answer is a short refusal. A long answer
// that merely contains the word is not treated as unknown.
func IsUnknownAnswer(answer string) bool {
	trimmed := strings.TrimSpace(answer)
	if len([]rune(trimmed)) > 120 {
		return false
	}
	for _, marker := range UnknownMarkers {
		if strings.Contains(trimmed, marker) {
			return true
		}
	}
	return false
}
