package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/waiting2050/ai-interview-agent/internal/agent/knowledge"
)

// Searcher is the read-only knowledge seam used by search_knowledge. The
// production retriever satisfies it; tests inject a deterministic double.
type Searcher interface {
	Search(ctx context.Context, query string) (knowledge.Result, error)
}

// Tool names in the autonomous catalog.
const (
	ToolSessionOverview = "get_session_overview"
	ToolListWeakTurns   = "list_weak_turns"
	ToolSearchTurns     = "search_turns"
	ToolGetTurn         = "get_turn"
	ToolSearchKnowledge = "search_knowledge"
)

// Catalog caps per tool for one report run. They are the documented limits in
// docs/m20-design.md §4.3.
var catalogCaps = map[string]int{
	ToolSessionOverview: 1,
	ToolListWeakTurns:   2,
	ToolSearchTurns:     4,
	ToolGetTurn:         4,
	ToolSearchKnowledge: 3,
}

// ToolCap returns the documented per-tool call limit, zero when unknown.
func ToolCap(tool string) int { return catalogCaps[tool] }

// AvailableTools lists the callable tools in catalog order.
func AvailableTools() []string {
	return []string{ToolSessionOverview, ToolListWeakTurns, ToolSearchTurns, ToolGetTurn, ToolSearchKnowledge}
}

// Result is the JSON envelope returned to the model. Status lets the prompt
// distinguish an empty result from a failure without inventing evidence.
type Result struct {
	Tool        string          `json:"tool"`
	Status      string          `json:"status"`
	Truncated   bool            `json:"truncated"`
	Note        string          `json:"note,omitempty"`
	Data        json.RawMessage `json:"data,omitempty"`
	DurationMS  int64           `json:"duration_ms"`
	ResultBytes int             `json:"result_bytes"`
}

// Marshal returns the envelope as the text appended to the model conversation.
func (r Result) Marshal() string {
	b, err := json.Marshal(r)
	if err != nil {
		return `{"tool":"` + r.Tool + `","status":"error","note":"结果序列化失败"}`
	}
	return string(b)
}

// SearchKnowledgeInput is the search_knowledge parameter object.
type SearchKnowledgeInput struct {
	Query string `json:"query"`
	Topic string `json:"topic,omitempty"`
	TopK  int    `json:"top_k,omitempty"`
}

// KnowledgeHit is the bounded knowledge snippet returned to the model. Full
// chunk text is deliberately not exposed.
type KnowledgeHit struct {
	Title      string  `json:"title"`
	SourcePath string  `json:"source_path"`
	SectionKey string  `json:"section_key,omitempty"`
	Snippet    string  `json:"snippet"`
	Cosine     float64 `json:"cosine,omitempty"`
}

// KnowledgeResult is the search_knowledge payload.
type KnowledgeResult struct {
	Hits      []KnowledgeHit `json:"hits"`
	Total     int            `json:"total"`
	Truncated bool           `json:"truncated"`
	Note      string         `json:"note,omitempty"`
}

// Execute is the single entry point shared by deterministic and autonomous
// modes. It validates the tool name and arguments, enforces the budgets inside
// Query and returns a JSON envelope suitable for the model conversation.
func (q *Query) Execute(ctx context.Context, tool string, args json.RawMessage) (Result, error) {
	tool = strings.TrimSpace(tool)
	rawArgs := strings.TrimSpace(string(args))
	if rawArgs == "" {
		rawArgs = "{}"
	}
	if ToolCap(tool) == 0 {
		q.noteRejected(ctx, tool, truncateRunes(rawArgs, 120), StatusDenied, "unknown_tool")
		return Result{Tool: tool, Status: StatusDenied, Note: "未知工具；可用工具：" + strings.Join(AvailableTools(), ", ")},
			fmt.Errorf("%w: %s", ErrUnknownTool, tool)
	}

	start := time.Now()
	beforeCalls := q.calls
	if err := q.validateCallBudget(tool); err != nil {
		q.records = append(q.records, CallRecord{
			Tool: tool, Args: truncateRunes(rawArgs, 120), Status: StatusDenied,
			SectionLabel: q.sectionLabel, Round: q.round(),
		})
		return Result{Tool: tool, Status: StatusDenied, Note: err.Error()}, err
	}

	var (
		payload any
		err     error
	)
	switch tool {
	case ToolSessionOverview:
		if err = requireNoArgs(rawArgs); err == nil {
			payload, err = q.SessionOverview(ctx)
		}
	case ToolListWeakTurns:
		var in struct {
			Limit int `json:"limit,omitempty"`
		}
		if err = decodeArgs(rawArgs, &in); err == nil {
			if in.Limit != 0 && (in.Limit < 1 || in.Limit > maxWeakLimit) {
				err = fmt.Errorf("%w: limit must be in [1,%d]", ErrInvalidArgument, maxWeakLimit)
			} else {
				payload, err = q.ListWeakTurns(ctx, in.Limit)
			}
		}
	case ToolSearchTurns:
		var in SearchInput
		if err = decodeArgs(rawArgs, &in); err == nil {
			payload, err = q.SearchTurns(ctx, in)
		}
	case ToolGetTurn:
		var in struct {
			TurnID string `json:"turn_id"`
		}
		if err = decodeArgs(rawArgs, &in); err == nil {
			if strings.TrimSpace(in.TurnID) == "" {
				err = fmt.Errorf("%w: turn_id is required", ErrInvalidArgument)
			} else {
				payload, err = q.Turn(ctx, in.TurnID)
			}
		}
	case ToolSearchKnowledge:
		var in SearchKnowledgeInput
		if err = decodeArgs(rawArgs, &in); err == nil {
			payload, err = q.searchKnowledge(ctx, in)
		}
	}

	if err != nil {
		status := classify(err)
		if q.calls == beforeCalls {
			// The tool rejected the request before consuming a call; record the
			// attempt so repeated invalid arguments stay visible in the audit.
			q.noteRejected(ctx, tool, truncateRunes(rawArgs, 120), status, err.Error())
		} else {
			q.records = append(q.records, CallRecord{
				Tool: tool, Args: truncateRunes(rawArgs, 120), Status: status,
				DurationMS: sinceMS(start), SectionLabel: q.sectionLabel, Round: q.round(),
			})
		}
		return Result{Tool: tool, Status: status, Note: err.Error(), DurationMS: sinceMS(start)}, err
	}

	body, truncated, capErr := capPayload(payload, q.opts.MaxResultRunes)
	if capErr != nil {
		q.records = append(q.records, CallRecord{
			Tool: tool, Args: truncateRunes(rawArgs, 120), Status: StatusError,
			DurationMS: sinceMS(start), SectionLabel: q.sectionLabel, Round: q.round(),
		})
		return Result{Tool: tool, Status: StatusError, Note: capErr.Error()}, capErr
	}

	status := StatusOK
	if payloadEmpty(tool, payload) {
		status = StatusEmpty
	}
	result := Result{
		Tool:        tool,
		Status:      status,
		Truncated:   truncated,
		Data:        body,
		DurationMS:  sinceMS(start),
		ResultBytes: len(body),
	}
	if truncated {
		result.Note = "结果超过上限已截断；断言可依据已返回的部分作出，必要时缩小 limit 或换更精确的 query"
		q.records = append(q.records, CallRecord{
			Tool: tool, Args: truncateRunes(rawArgs, 120), Status: status,
			DurationMS: result.DurationMS, ResultRunes: q.opts.MaxResultRunes,
			ResultBytes: len(body), Truncated: true, SectionLabel: q.sectionLabel, Round: q.round(),
		})
	} else {
		// The individual tool methods record their own calls; this envelope
		// record is only needed when truncation changed the reported size.
		q.patchLastRecord(tool, StatusEmpty, len(body))
		if status == StatusEmpty {
			result.Note = "无命中：这是真实的空结果，不要编造证据"
		}
	}
	return result, nil
}

// validateCallBudget duplicates the cheap checks in beginCall so unknown
// arguments fail before a repository read, while still counting the attempt.
func (q *Query) validateCallBudget(tool string) error {
	if strings.TrimSpace(q.sessionID) == "" {
		return fmt.Errorf("%w: session id is empty", ErrInvalidArgument)
	}
	if q.calls >= q.opts.MaxCalls {
		return fmt.Errorf("%w: total calls reached %d", ErrBudgetExceeded, q.opts.MaxCalls)
	}
	if limit := q.toolLimit(tool); limit > 0 && q.toolCalls[tool] >= limit {
		return fmt.Errorf("%w: %s reached its %d-call limit", ErrBudgetExceeded, tool, limit)
	}
	return nil
}

// patchLastRecord fills in the status and byte size on the record written by
// the underlying tool method.
func (q *Query) patchLastRecord(tool, status string, bytes int) {
	for i := len(q.records) - 1; i >= 0; i-- {
		if q.records[i].Tool == tool {
			q.records[i].Status = status
			q.records[i].ResultBytes = bytes
			return
		}
	}
}

func (q *Query) searchKnowledge(ctx context.Context, in SearchKnowledgeInput) (KnowledgeResult, error) {
	query := strings.TrimSpace(in.Query)
	if query == "" {
		return KnowledgeResult{}, fmt.Errorf("%w: query is required", ErrInvalidArgument)
	}
	if in.TopK == 0 {
		in.TopK = 3
	}
	if in.TopK < 1 || in.TopK > 6 {
		return KnowledgeResult{}, fmt.Errorf("%w: top_k must be in [1,6]", ErrInvalidArgument)
	}

	callCtx, cancel, start, err := q.beginCall(ctx, ToolSearchKnowledge, fmt.Sprintf("query=%s top_k=%d", query, in.TopK))
	if err != nil {
		return KnowledgeResult{}, err
	}
	defer cancel()

	if q.opts.Searcher == nil {
		q.endCall(ctx, ToolSearchKnowledge, "disabled", start, 0, false)
		return KnowledgeResult{Note: "知识检索未启用（无 embedding 语料）", Total: 0}, nil
	}

	res, err := q.opts.Searcher.Search(callCtx, query)
	if err != nil {
		return KnowledgeResult{}, fmt.Errorf("search knowledge: %w", q.callError(ToolSearchKnowledge, callCtx, err))
	}

	out := KnowledgeResult{Total: len(res.Hits)}
	topic := strings.TrimSpace(in.Topic)
	for _, hit := range res.Hits {
		if topic != "" && !strings.Contains(hit.TopicID, topic) && !strings.Contains(hit.Title, topic) {
			continue
		}
		out.Hits = append(out.Hits, KnowledgeHit{
			Title:      truncateRunes(hit.Title, 80),
			SourcePath: hit.SourcePath,
			SectionKey: truncateRunes(hit.SectionKey, 80),
			Snippet:    truncateRunes(hit.Snippet, 240),
			Cosine:     hit.Cosine,
		})
		if len(out.Hits) == in.TopK {
			break
		}
	}
	out.Total = len(out.Hits)
	if len(out.Hits) == 0 {
		out.Note = "无命中；不要编造知识库内容"
	}
	body, _ := json.Marshal(out)
	q.endCall(ctx, ToolSearchKnowledge, fmt.Sprintf("query=%s top_k=%d", query, in.TopK), start, utf8.RuneCount(body), false)
	return out, nil
}

func requireNoArgs(raw string) error {
	if raw == "" || raw == "{}" || raw == "null" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return fmt.Errorf("%w: args must be a JSON object", ErrInvalidArgument)
	}
	if len(m) > 0 {
		return fmt.Errorf("%w: get_session_overview takes no args", ErrInvalidArgument)
	}
	return nil
}

func decodeArgs(raw string, out any) error {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidArgument, err)
	}
	if dec.More() {
		return fmt.Errorf("%w: trailing data after JSON object", ErrInvalidArgument)
	}
	return nil
}

func classify(err error) string {
	switch {
	case errors.Is(err, ErrTimeout):
		return StatusTimeout
	case errors.Is(err, ErrForbidden), errors.Is(err, ErrInvalidArgument), errors.Is(err, ErrBudgetExceeded):
		return StatusDenied
	default:
		return StatusError
	}
}

func payloadEmpty(tool string, payload any) bool {
	switch v := payload.(type) {
	case SearchResult:
		return len(v.Turns) == 0
	case KnowledgeResult:
		return len(v.Hits) == 0
	case TurnDetail:
		return strings.TrimSpace(v.TurnID) == ""
	default:
		return false
	}
}

// capPayload bounds the model-visible payload. It re-marshals a reduced
// structure instead of cutting the JSON string, so the model always receives a
// valid object it can still reason over.
func capPayload(payload any, maxRunes int) (json.RawMessage, bool, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, false, err
	}
	if utf8.RuneCount(body) <= maxRunes {
		return body, false, nil
	}

	switch v := payload.(type) {
	case Overview:
		v.Truncated = true
		for len(v.Turns) > 1 {
			v.Turns = v.Turns[:len(v.Turns)/2]
			if b, err := json.Marshal(v); err == nil && utf8.RuneCount(b) <= maxRunes {
				return b, true, nil
			}
		}
		return marshalCapped(v)
	case SearchResult:
		v.Truncated = true
		for len(v.Turns) > 1 {
			v.Turns = v.Turns[:len(v.Turns)/2]
			if b, err := json.Marshal(v); err == nil && utf8.RuneCount(b) <= maxRunes {
				return b, true, nil
			}
		}
		return marshalCapped(v)
	case TurnDetail:
		v.Truncated = true
		v.Answer = truncateRunes(v.Answer, maxRunes/3)
		return marshalCapped(v)
	case KnowledgeResult:
		v.Truncated = true
		for len(v.Hits) > 1 {
			v.Hits = v.Hits[:len(v.Hits)/2]
			if b, err := json.Marshal(v); err == nil && utf8.RuneCount(b) <= maxRunes {
				return b, true, nil
			}
		}
		return marshalCapped(v)
	default:
		return nil, false, fmt.Errorf("%w: result cannot be capped", ErrInvalidArgument)
	}
}

func marshalCapped(payload any) (json.RawMessage, bool, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, false, err
	}
	return body, true, nil
}

func sinceMS(start time.Time) int64 { return time.Since(start).Milliseconds() }
