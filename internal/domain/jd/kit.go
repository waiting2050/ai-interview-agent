package jd

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/waiting2050/ai-interview-agent/internal/domain/resume"
)

// ErrEmptyResume marks a kit request without resume facts.
var ErrEmptyResume = errors.New("apply kit needs resume text")

// KitInput is the request shape of POST /v1/jd/{id}/kit.
type KitInput struct {
	ResumeText    string `json:"resume_text"`
	TargetRole    string `json:"target_role"`
	CandidateName string `json:"candidate_name"`
}

// KitFact is one piece of real resume evidence used by the kit.
type KitFact struct {
	Text   string `json:"text"`
	Source string `json:"source"`
}

// Kit is a generated application package. It only reorders resume facts and
// never invents metrics, responsibilities or employers.
type Kit struct {
	JDID               string        `json:"jd_id"`
	Role               string        `json:"role"`
	CandidateName      string        `json:"candidate_name"`
	Summary            string        `json:"summary"`
	Opener             string        `json:"opener"`
	Facts              []KitFact     `json:"facts"`
	Outline            []OutlineItem `json:"outline"`
	RehearsalQuestions []string      `json:"rehearsal_questions"`
	Gaps               Gap           `json:"gaps"`
	Warnings           []string      `json:"warnings,omitempty"`
	Markdown           string        `json:"markdown"`
	ModelCalled        bool          `json:"model_called"`
	GeneratedAt        time.Time     `json:"generated_at"`
}

// BuildKit assembles the M16 package for one cached listing. Everything in
// Summary/Opener is traceable to Facts, which come from resume.Parse only.
func BuildKit(listing *Listing, in KitInput, now time.Time) (Kit, error) {
	text := strings.TrimSpace(in.ResumeText)
	if text == "" {
		return Kit{}, ErrEmptyResume
	}
	profile := resume.Parse(text)
	if profile.IsZero() {
		return Kit{}, ErrEmptyResume
	}

	role := strings.TrimSpace(in.TargetRole)
	if role == "" {
		role = listing.Structured.Role
	}
	if role == "" {
		role = "目标岗位"
	}
	candidate := strings.TrimSpace(in.CandidateName)
	if candidate == "" {
		candidate = "我"
	}

	analysis := AnalyzeListing(listing, text, role, "", 6)

	var facts []KitFact
	if profile.Years > 0 {
		facts = append(facts, KitFact{
			Text:   fmt.Sprintf("%d 年工作经验", profile.Years),
			Source: "简历：年限字段",
		})
	}
	for _, skill := range profile.Skills {
		facts = append(facts, KitFact{Text: "技能：" + skill, Source: "简历：技能字段"})
	}
	for _, project := range profile.Projects {
		if project.Name == "" {
			continue
		}
		entry := project.Name
		if project.Role != "" {
			entry += "（" + project.Role + "）"
		}
		if len(project.TechStack) > 0 {
			entry += " 技术栈：" + strings.Join(project.TechStack, "/")
		}
		facts = append(facts, KitFact{Text: entry, Source: "简历：项目经历"})
		if len(project.Highlights) > 0 {
			facts = append(facts, KitFact{Text: project.Highlights[0], Source: "简历：项目描述原文"})
		}
	}

	matchedSkills := matchedSkills(profile.Skills, listing.Structured)
	var warnings []string
	if len(matchedSkills) == 0 {
		warnings = append(warnings, "简历未命中 JD 的技能关键词；摘要只使用简历已有事实，未做补充")
	}

	summary := buildSummary(candidate, role, profile, matchedSkills)
	opener := buildOpener(candidate, role, listing, profile, matchedSkills)
	questions := buildRehearsalQuestions(analysis)

	kit := Kit{
		JDID:               listing.ID,
		Role:               role,
		CandidateName:      candidate,
		Summary:            summary,
		Opener:             opener,
		Facts:              facts,
		Outline:            analysis.Outline,
		RehearsalQuestions: questions,
		Gaps:               analysis.Gaps,
		Warnings:           warnings,
		ModelCalled:        false,
		GeneratedAt:        now,
	}
	kit.Markdown = renderKitMarkdown(kit, listing)
	return kit, nil
}

func matchedSkills(resumeSkills []string, structured Structured) []string {
	jdSkills := map[string]string{}
	for _, skill := range structured.RequiredSkills {
		jdSkills[strings.ToLower(skill)] = skill
	}
	for _, skill := range structured.NiceToHave {
		jdSkills[strings.ToLower(skill)] = skill
	}
	var out []string
	for _, skill := range resumeSkills {
		if canonical, ok := jdSkills[strings.ToLower(skill)]; ok {
			out = append(out, canonical)
		}
	}
	return out
}

func buildSummary(candidate, role string, profile resume.Profile, matched []string) string {
	var b strings.Builder
	b.WriteString(candidate)
	if profile.Years > 0 {
		fmt.Fprintf(&b, "有 %d 年经验", profile.Years)
	}
	fmt.Fprintf(&b, "，目标岗位为%s", role)
	if len(matched) > 0 {
		fmt.Fprintf(&b, "；与 JD 直接相关的技能是 %s", strings.Join(matched, "、"))
	} else if len(profile.Skills) > 0 {
		limit := len(profile.Skills)
		if limit > 5 {
			limit = 5
		}
		fmt.Fprintf(&b, "；简历中记录的技能是 %s", strings.Join(profile.Skills[:limit], "、"))
	}
	if len(profile.Projects) > 0 {
		names := make([]string, 0, 3)
		for _, project := range profile.Projects {
			if project.Name == "" {
				continue
			}
			names = append(names, project.Name)
			if len(names) == 3 {
				break
			}
		}
		if len(names) > 0 {
			fmt.Fprintf(&b, "；代表项目：%s", strings.Join(names, "、"))
		}
	}
	b.WriteString("。以上事实均来自简历原文，未新增或改写指标。")
	return b.String()
}

func buildOpener(candidate, role string, listing *Listing, profile resume.Profile, matched []string) string {
	var b strings.Builder
	b.WriteString("您好，我是")
	b.WriteString(candidate)
	if listing.Structured.Company != "" {
		fmt.Fprintf(&b, "，看到%s的%s岗位", listing.Structured.Company, role)
	} else {
		fmt.Fprintf(&b, "，看到%s岗位的招聘信息", role)
	}
	if profile.Years > 0 {
		fmt.Fprintf(&b, "。我有 %d 年相关经验", profile.Years)
	}
	if len(matched) > 0 {
		fmt.Fprintf(&b, "，在 %s 上有可展开的项目经历", strings.Join(matched, "、"))
	} else if len(profile.Projects) > 0 {
		b.WriteString("，简历中的项目经历与岗位有可迁移的部分")
	}
	for _, project := range profile.Projects {
		if project.Name == "" {
			continue
		}
		fmt.Fprintf(&b, "，例如「%s」", project.Name)
		break
	}
	b.WriteString("。希望有机会围绕岗位要求与我的真实经历进一步交流。")
	return b.String()
}

func buildRehearsalQuestions(analysis Analysis) []string {
	var out []string
	for _, item := range analysis.Outline {
		question := fmt.Sprintf("围绕「%s」准备一个真实项目：%s", item.Topic, item.Objective)
		if len(item.ExpectedPoints) > 0 {
			question += "（讲清：" + strings.Join(item.ExpectedPoints, "；") + "）"
		}
		out = append(out, question)
	}
	for _, skill := range analysis.Gaps.MissingSkills {
		out = append(out, fmt.Sprintf(
			"JD 要求 %s，简历未体现：准备说明你是否做过相关项目；如果没有，如实说明计划如何补齐，不要虚构经历。", skill))
		if len(out) >= 10 {
			break
		}
	}
	return out
}

func renderKitMarkdown(kit Kit, listing *Listing) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# 申请材料包 · %s\n\n", kit.Role)
	b.WriteString("> 规则生成，未调用模型；所有事实均来自简历原文，不新增虚构内容。\n")
	if listing.SourceURL != "" {
		fmt.Fprintf(&b, "> JD 来源：%s（获取于 %s）\n", listing.SourceURL, listing.FetchedAt.Format(time.RFC3339))
	}
	b.WriteString("\n## 定制简历摘要\n\n")
	b.WriteString(kit.Summary + "\n\n")
	b.WriteString("## 岗位开场\n\n")
	b.WriteString(kit.Opener + "\n\n")

	b.WriteString("## 面试题纲\n\n")
	for _, item := range kit.Outline {
		fmt.Fprintf(&b, "%d. **%s**（%s）\n", item.Seq, item.Topic, item.Difficulty)
		fmt.Fprintf(&b, "   - 目标：%s\n", item.Objective)
		if len(item.ExpectedPoints) > 0 {
			fmt.Fprintf(&b, "   - 准备要点：%s\n", strings.Join(item.ExpectedPoints, "；"))
		}
	}
	if len(kit.Outline) == 0 {
		b.WriteString("未生成题纲（JD 未匹配到领域包）。\n")
	}

	b.WriteString("\n## 预演问题\n\n")
	for _, question := range kit.RehearsalQuestions {
		fmt.Fprintf(&b, "- %s\n", question)
	}

	if len(kit.Gaps.MissingSkills) > 0 || len(kit.Gaps.MissingExperiences) > 0 {
		b.WriteString("\n## 缺口（面试前先补）\n\n")
		for _, skill := range kit.Gaps.MissingSkills {
			fmt.Fprintf(&b, "- 缺技能：%s\n", skill)
		}
		for _, exp := range kit.Gaps.MissingExperiences {
			fmt.Fprintf(&b, "- %s\n", exp)
		}
	}

	b.WriteString("\n## 事实来源\n\n")
	for _, fact := range kit.Facts {
		fmt.Fprintf(&b, "- %s（%s）\n", fact.Text, fact.Source)
	}

	b.WriteString("\n---\n\n本材料包不提供自动投递、表单代填或账号托管；请在目标平台自行确认后投递。\n")
	return b.String()
}
