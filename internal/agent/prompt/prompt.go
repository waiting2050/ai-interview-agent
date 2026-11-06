// Package prompt owns the model-facing text. Prompts live here so the
// evaluation node stays about control flow and validation.
package prompt

import (
	"fmt"
	"strings"
)

// EvaluationVersion is persisted on every evaluation so rubric iterations can
// be compared without guessing which prompt produced a score.
const EvaluationVersion = "eval-v3"

const evaluationSystem = `[TASK:evaluation] 你是资深技术面试官，负责评估候选人对一道技术问题的回答。只输出一个 JSON 对象，不要输出 Markdown 代码块、解释或多余文字。

评分维度（每个维度给 1-5 的整数分）：
- correctness 正确性：结论是否正确，有无事实性错误。
- depth 深度：是否触达原理、边界条件与权衡。按四要素判定：
  - 5 分 = 机制 + 边界 + 取舍 + 验证：讲清内部如何工作；说明前提、失效条件或异常路径；解释为什么不选另一种做法及其代价；有数据、实验或生产结果佐证。
  - 4 分 = 机制 + 取舍：讲清原理与选择理由，但边界条件或验证证据缺一项。
  - 3 分 = 结论 + 局部机制：结论正确，但只解释一两个局部环节，未覆盖边界、取舍或验证。
  - 1 分 = 只背定义或结论错误。没有边界与验证最多 4 分；没有取舍最多 3 分。
- engineering_judgment 工程判断：是否给出可落地的取舍与风险。
- structure 表达结构：是否分点、有层次、先结论后论据。
- evidence_credibility 经历可信度：细节是否具体、可追问，避免空泛口号。

除 depth 外，其余维度用通用锚点：1 分 = 明显错误或几乎无信息；3 分 = 基本正确但泛泛而谈；5 分 = 准确、深入且有具体取舍。

硬性规则：
1. evidence 中每条 quote 必须逐字出现在候选人回答里（允许标点/空格差异），否则会被丢弃。
2. bonus/penalty 的 quote 同样必须来自回答原文；bonus 为 1-3 分，penalty 为 -3 到 -1 分。
3. 只有回答出现明显亮点才给 bonus；答非所问、事实错误、空泛模板才给 penalty。
4. 如果存在 missing_points 或 penalty，且值得追问，可令 followup.needed=true，并给出 followup.target（要追问的具体点）。没有明确追问价值时必须为 false。
5. confidence 取 0-1；回答信息不足时降低 confidence，而不是编造细节。
6. 如果 user 消息给出了「本题期望覆盖」，evidence.reason 必须明确写出被覆盖的期望点原文（格式如「覆盖：goroutine 调度与 G-M-P 模型」）；未覆盖的期望点必须逐条写入 missing_points。不能只写「回答原文片段」之类的笼统理由。

JSON 结构：
{
  "schema_version": 1,
  "turn_id": "",
  "scores": {"correctness": 3, "depth": 3, "engineering_judgment": 3, "structure": 3, "evidence_credibility": 3},
  "evidence": [{"dimension": "correctness", "quote": "回答原文片段", "reason": "为什么"}],
  "bonus": [{"type": "highlight", "quote": "回答原文片段", "points": 1, "reason": "亮点"}],
  "penalty": [{"type": "violation", "quote": "回答原文片段", "points": -1, "reason": "问题"}],
  "confidence": 0.6,
  "missing_points": ["缺失的关键点"],
  "followup": {"needed": false, "target": "", "reason": ""}
}`

// EvaluationSystem returns the system message for answer evaluation.
func EvaluationSystem() string { return evaluationSystem }

type EvaluationInput struct {
	TargetRole     string
	Difficulty     string
	Topic          string
	Question       string
	Answer         string
	ResumeText     string
	JDText         string
	ExpectedPoints []string
}

// EvaluationUser renders the user message. Resume and JD are truncated so one
// answer never sends an unbounded payload.
func EvaluationUser(in EvaluationInput) string {
	var b strings.Builder
	fmt.Fprintf(&b, "目标岗位：%s\n难度：%s\n当前题目主题：%s\n", in.TargetRole, in.Difficulty, in.Topic)
	if len(in.ExpectedPoints) > 0 {
		fmt.Fprintf(&b, "本题期望覆盖：%s\n", strings.Join(in.ExpectedPoints, "；"))
	}
	if in.ResumeText != "" {
		fmt.Fprintf(&b, "\n<resume>\n%s\n</resume>\n", truncate(in.ResumeText, 1200))
	}
	if in.JDText != "" {
		fmt.Fprintf(&b, "\n<jd>\n%s\n</jd>\n", truncate(in.JDText, 800))
	}
	fmt.Fprintf(&b, "\n<question>\n%s\n</question>\n", in.Question)
	fmt.Fprintf(&b, "\n<answer>\n%s\n</answer>\n", in.Answer)
	b.WriteString("\n请按系统消息中的 JSON 结构输出评估结果。")
	return b.String()
}

// RepairMessage is appended as a second user turn when the previous output was
// invalid. It lists the exact issues so the model can correct them.
func RepairMessage(issues []string) string {
	return "上一次输出未通过校验：" + strings.Join(issues, "；") +
		"。请修正后重新只输出一个合法 JSON 对象，quote 必须来自候选人回答原文。"
}

func truncate(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes]) + "…"
}

// QuestionVersion is persisted alongside question metadata in logs so prompt
// changes can be traced without storing the full text.
const QuestionVersion = "question-v1"

const questionSystem = `[TASK:question] 你是资深技术面试官，负责根据题纲知识点和候选人简历生成下一道主问题。只输出一个 JSON 对象：{"question": "..."}，不要输出 Markdown、解释、答案或期望点清单。

要求：
1. 必须围绕给定的知识点、目标和期望覆盖点，问出可验证的实现细节、边界条件或工程取舍。
2. 优先结合候选人的技能或项目经历；候选人没有相关经历时可给假设场景，但不得编造其经历。
3. 不得重复历史问题（user 消息会列出）；也不要把期望覆盖点原样念给候选人。
4. 问题长度 40-180 个汉字，一次只问一个核心问题，语气自然。
5. 输出必须是合法 JSON，question 字段不能为空。`

const followupSystem = `[TASK:followup] 你是资深技术面试官，正在对候选人的上一轮回答进行追问。只输出一个 JSON 对象：{"question": "..."}，不要输出 Markdown、解释或答案。

要求：
1. 必须围绕「追问目标」和本题未覆盖的期望点继续深挖，不要换题、不要重复主问题。
2. 要承接候选人上一轮回答中的具体说法，或明确指出其回答中缺失的关键环节，让对方能接着答。
3. 不得重复历史问题；不要把期望覆盖点原样念给候选人。
4. 问题长度 30-150 个汉字，一次只问一个核心点。
5. 输出必须是合法 JSON，question 字段不能为空。`

// QuestionKind values.
const (
	QuestionKindPrimary  = "primary"
	QuestionKindFollowup = "followup"
)

// QuestionInput is everything the question prompt may see.
type QuestionInput struct {
	Kind           string
	TargetRole     string
	Difficulty     string
	JDText         string
	ProfileText    string
	Topic          string
	Objective      string
	ExpectedPoints []string
	History        []string
	FollowupTarget string
	LastQuestion   string
	Answer         string
}

// QuestionSystem returns the system message for the requested question kind.
func QuestionSystem(kind string) string {
	if kind == QuestionKindFollowup {
		return followupSystem
	}
	return questionSystem
}

// QuestionUser renders the question-generation user message. History is capped
// so a long interview never sends an unbounded payload.
func QuestionUser(in QuestionInput) string {
	var b strings.Builder
	fmt.Fprintf(&b, "目标岗位：%s\n难度：%s\n", in.TargetRole, in.Difficulty)
	if in.JDText != "" {
		fmt.Fprintf(&b, "<jd>\n%s\n</jd>\n", truncate(in.JDText, 800))
	}
	if in.ProfileText != "" {
		fmt.Fprintf(&b, "<profile>\n%s\n</profile>\n", truncate(in.ProfileText, 1000))
	}
	fmt.Fprintf(&b, "<topic>%s</topic>\n", in.Topic)
	if in.Objective != "" {
		fmt.Fprintf(&b, "<objective>%s</objective>\n", in.Objective)
	}
	if len(in.ExpectedPoints) > 0 {
		fmt.Fprintf(&b, "<expected_points>%s</expected_points>\n", strings.Join(in.ExpectedPoints, "；"))
	}
	if len(in.History) > 0 {
		start := 0
		if len(in.History) > 12 {
			start = len(in.History) - 12
		}
		b.WriteString("<history>\n")
		for i, question := range in.History[start:] {
			fmt.Fprintf(&b, "%d. %s\n", start+i+1, truncate(question, 120))
		}
		b.WriteString("</history>\n")
	}
	if in.Kind == QuestionKindFollowup {
		if in.FollowupTarget != "" {
			fmt.Fprintf(&b, "<followup_target>%s</followup_target>\n", in.FollowupTarget)
		}
		if in.LastQuestion != "" {
			fmt.Fprintf(&b, "<last_question>\n%s\n</last_question>\n", in.LastQuestion)
		}
		if in.Answer != "" {
			fmt.Fprintf(&b, "<last_answer>\n%s\n</last_answer>\n", truncate(in.Answer, 1200))
		}
	}
	b.WriteString("\n请按系统消息要求只输出一个合法 JSON 对象。")
	return b.String()
}

// QuestionRepairMessage lists validation issues for a retried question.
func QuestionRepairMessage(issues []string) string {
	return "上一次输出未通过校验：" + strings.Join(issues, "；") +
		"。请重新只输出一个合法 JSON 对象，question 必须是一道全新的、未在历史列表中出现的问题。"
}

// ReportVersion is persisted with every report so prompt iterations can be
// compared.
const ReportVersion = "report-v1"

const reportSystem = `[TASK:report] 你是资深技术面试官，负责根据结构化评估摘要与少量原文片段撰写面试报告点评。只输出一个 JSON 对象，不要输出 Markdown 代码块或解释。

你已经拿到（不会拿到整场对话）：
- 会话概览：岗位、难度、完成题数、综合得分、各维度平均分。
- 逐题摘要：题号、主题、类型、题干、得分、缺失点。
- 薄弱题与最多 8 条回答摘录（每条已截断）。

要求：
1. verdict：120-260 字总评，先给结论，再说明优势与不足，必须引用至少一个具体题目主题。
2. dimension_comments：只写 5 个键 correctness / depth / engineering_judgment / structure / evidence_credibility，每条 20-60 字，结合得分说人话。
3. questions：只能使用提供的 turn_id，逐题 20-80 字点评；不得编造未提供的回答内容。strengths/missing_points 各最多 2 条。
4. improvements：恰好 3 条，按优先级排序，每条 20-60 字，必须具体可练习。
5. next_steps：2-5 条，每条 20-80 字，给出可执行的练习方式。
6. 不得给出分数；分数由系统根据评估记录计算。

JSON 结构：
{
  "verdict": "",
  "dimension_comments": {"correctness": "", "depth": "", "engineering_judgment": "", "structure": "", "evidence_credibility": ""},
  "questions": [{"turn_id": "", "comment": "", "strengths": [""], "missing_points": [""]}],
  "improvements": ["", "", ""],
  "next_steps": ["", ""]
}`

// ReportSystem returns the system message for report commentary generation.
func ReportSystem() string { return reportSystem }

// ReportTurn is the compact per-question evidence injected into the report
// prompt. Answers are excerpts, never the full transcript.
type ReportTurn struct {
	TurnID        string
	Seq           int
	Topic         string
	Kind          string
	Question      string
	AnswerExcerpt string
	Score         float64
	MissingPoints []string
}

type ReportInput struct {
	TargetRole    string
	Difficulty    string
	CandidateName string
	AnsweredTurns int
	OverallScore  float64
	Dimensions    []string
	Turns         []ReportTurn
	WeakTurns     []ReportTurn
}

// ReportUser renders the report prompt. Every block is bounded so the prompt
// stays small even for a long interview.
func ReportUser(in ReportInput) string {
	var b strings.Builder
	fmt.Fprintf(&b, "目标岗位：%s\n难度：%s\n候选人：%s\n完成题数：%d\n综合得分：%.1f\n",
		in.TargetRole, in.Difficulty, in.CandidateName, in.AnsweredTurns, in.OverallScore)
	start := 0
	if len(in.Turns) > 12 {
		start = len(in.Turns) - 12
	}
	b.WriteString("\n<dimensions>\n")
	for _, line := range in.Dimensions {
		fmt.Fprintf(&b, "- %s\n", line)
	}
	b.WriteString("</dimensions>\n\n<turn_summaries>\n")
	for _, turn := range in.Turns[start:] {
		fmt.Fprintf(&b, "- turn_id=%s seq=%d kind=%s topic=%s score=%.1f question=%s missing=%s\n",
			turn.TurnID, turn.Seq, turn.Kind, truncate(turn.Topic, 40), turn.Score,
			truncate(turn.Question, 80), truncate(strings.Join(turn.MissingPoints, "；"), 120))
	}
	b.WriteString("</turn_summaries>\n\n<weak_turns>\n")
	for _, turn := range in.WeakTurns {
		fmt.Fprintf(&b, "- turn_id=%s topic=%s score=%.1f\n  Q: %s\n  A: %s\n",
			turn.TurnID, truncate(turn.Topic, 40), turn.Score,
			truncate(turn.Question, 100), truncate(turn.AnswerExcerpt, 320))
	}
	b.WriteString("</weak_turns>\n")
	b.WriteString("\n请按系统消息中的 JSON 结构只输出报告点评。")
	return b.String()
}

// ReportRepairMessage lists validation issues for a retried report.
func ReportRepairMessage(issues []string) string {
	return "上一次输出未通过校验：" + strings.Join(issues, "；") +
		"。请修正后重新只输出一个合法 JSON 对象，turn_id 必须来自给定的 turn_summaries。"
}
