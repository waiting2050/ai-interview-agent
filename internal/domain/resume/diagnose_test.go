package resume

import (
	"strings"
	"testing"
)

func TestCoverageUsesSkillAliases(t *testing.T) {
	diag := mustAnalyze(t, AnalyzeOptions{
		Text:   "技能：k8s 1.27、postgres 15",
		JDText: "任职要求：熟悉 Kubernetes 与 PostgreSQL",
	})
	coverage := componentScore(diag.ScreenScore, "JD 关键词覆盖")
	if coverage != 100 {
		t.Fatalf("coverage = %d, want 100 (aliases should match)", coverage)
	}
}

func TestCoverageDoesNotMergeRedisAndRedisson(t *testing.T) {
	diag := mustAnalyze(t, AnalyzeOptions{
		Text:   "技能：Redisson 客户端",
		JDText: "任职要求：熟悉 Redis",
	})
	if coverage := componentScore(diag.ScreenScore, "JD 关键词覆盖"); coverage != 0 {
		t.Fatalf("coverage = %d, want 0", coverage)
	}
}

func componentScore(breakdown ScoreBreakdown, name string) int {
	for _, component := range breakdown.Components {
		if component.Name == name {
			return component.Score
		}
	}
	return -1
}

func mustAnalyze(t *testing.T, opts AnalyzeOptions) Diagnosis {
	t.Helper()
	diag, err := DefaultAnalyzer().Analyze(opts)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	return diag
}

func issueSet(diag Diagnosis) map[string]Issue {
	set := make(map[string]Issue, len(diag.Issues))
	for _, issue := range diag.Issues {
		if _, exists := set[issue.RuleID]; !exists {
			set[issue.RuleID] = issue
		}
	}
	return set
}

func segmentScore(diag Diagnosis, index int) SegmentScore {
	for _, seg := range diag.Segments {
		if seg.Index == index {
			return seg
		}
	}
	return SegmentScore{Score: -100}
}

const perfectResume = `张三
Go 后端开发（校招） | 13800000000 | zhangsan@example.com
教育：XX 大学 软件工程 本科 2022.09-2026.06（预计）
技能：Go、MySQL、Redis、Linux、HTTP、goroutine、channel、context
项目：校园短视频 Feed 服务（个人项目，2025.03-2026.01，团队 1 人，代码 github.com/zhangsan/feed）
- 独立完成 Feed 服务，负责读链路 3 个接口；技术栈 Go/MySQL/Redis/Linux/HTTP，用 goroutine + channel + context 控制并发
- Feed 首屏接口 P99 从 480ms 降至 120ms（4C8G、10 万条动态、wrk 200 并发，对比 v1.3）
- 热点作者主页峰值 1.2k QPS 下存在缓存击穿，采用 singleflight + 逻辑过期，回源下降 87%（7 天监控均值）
- 单机部署于 2 台 2C4G 云主机（Docker Compose），20 人内测，未对外公网开放
- 补 32 个表驱动用例，CI 覆盖率 41% 提升至 68%`

func TestPerfectResumeIsNotOverDeducted(t *testing.T) {
	diag := mustAnalyze(t, AnalyzeOptions{
		Text: perfectResume, TargetRole: "Go 后端开发",
	})
	if len(diag.Issues) != 0 {
		for _, issue := range diag.Issues {
			t.Logf("unexpected %s %s quote=%q", issue.RuleID, issue.Title, issue.Quote)
		}
		t.Fatalf("perfect resume produced %d automatic deductions, want 0", len(diag.Issues))
	}
	if diag.DocumentScore != 100 || diag.OverallScore != 100 {
		t.Fatalf("scores = document %d overall %d, want 100/100", diag.DocumentScore, diag.OverallScore)
	}
	if diag.Band.Level != 5 {
		t.Fatalf("band = %d, want 5", diag.Band.Level)
	}
}

func TestDateOverlapDeducts(t *testing.T) {
	text := `张三 2026 届
XX 科技有限公司 后端开发实习 2025.03-2025.07
- 负责订单查询接口，P95 从 320ms 降到 90ms（4C8G、10 万条数据、wrk 200 并发）
YY 科技有限公司 后端开发实习 2025.06-2025.09
- 维护用户服务，补 20 个单测`
	diag := mustAnalyze(t, AnalyzeOptions{Text: text})
	issue, ok := issueSet(diag)["R01"]
	if !ok {
		t.Fatalf("R01 not triggered: %+v", diag.Issues)
	}
	if issue.SegmentType != TypeInternship {
		t.Fatalf("R01 segment type = %q, want internship", issue.SegmentType)
	}
}

func TestEmploymentGapDeducts(t *testing.T) {
	text := `张三 2026 届
XX 科技有限公司 后端开发实习 2024.01-2024.03
- 负责用户接口开发，补 12 个单测
YY 科技有限公司 后端开发实习 2025.03-2025.06
- 负责订单接口，P95 从 320ms 降到 90ms（4C8G、10 万条、wrk 200 并发）`
	diag := mustAnalyze(t, AnalyzeOptions{Text: text})
	if _, ok := issueSet(diag)["R02"]; !ok {
		t.Fatalf("R02 not triggered: %+v", diag.Issues)
	}
}

func TestShortPeriodWithStackContradictionTriggersRedline(t *testing.T) {
	text := `张三 2026 届
个人项目 分布式电商系统 2025.03-2025.04（三周）
- 三周完成分布式电商系统，技术栈 K8s、Istio、Kafka、RabbitMQ、Elasticsearch、MongoDB、Redis、MySQL`
	diag := mustAnalyze(t, AnalyzeOptions{Text: text})
	set := issueSet(diag)
	if _, ok := set["R03"]; !ok {
		t.Fatalf("R03 not triggered: %+v", diag.Issues)
	}
	if _, ok := set["R04"]; !ok {
		t.Fatalf("R04 not triggered: %+v", diag.Issues)
	}
	if !diag.Redline {
		t.Fatalf("redline = false, want true")
	}
	if set["R03"].Severity != "high" {
		t.Fatalf("R03 severity = %q, want high", set["R03"].Severity)
	}
	if diag.OverallScore > 69 || diag.Band.Level > 3 {
		t.Fatalf("redline overall=%d band=%d, want overall <= 69 and band <= 3", diag.OverallScore, diag.Band.Level)
	}
}

func TestPerformanceMetricWithoutBaselineOrEnvironment(t *testing.T) {
	text := `张三 2026 届
XX 科技有限公司 后端开发实习 2024.06-2024.09
- 负责订单接口开发，P99 从 500ms 降到 100ms`
	diag := mustAnalyze(t, AnalyzeOptions{Text: text})
	if _, ok := issueSet(diag)["R08"]; !ok {
		t.Fatalf("R08 not triggered: %+v", diag.Issues)
	}
}

func TestSkillBarsDeducted(t *testing.T) {
	text := `张三
技能：Go ██████ 90%，MySQL 85%
个人项目 校园二手平台 2025.03-2025.06
- 完成商品与订单模块`
	diag := mustAnalyze(t, AnalyzeOptions{Text: text})
	if _, ok := issueSet(diag)["R38"]; !ok {
		t.Fatalf("R38 not triggered: %+v", diag.Issues)
	}
}

func TestTypoAndPunctuationDeducted(t *testing.T) {
	text := `张三，Go 后端开发, MySql，Ｒｅｄｉｓ
个人项目 校园二手平台 2025.03-2025.06
- 完成商品模块`
	diag := mustAnalyze(t, AnalyzeOptions{Text: text})
	if _, ok := issueSet(diag)["R36"]; !ok {
		t.Fatalf("R36 not triggered: %+v", diag.Issues)
	}
}

func TestSameContentScoresDifferByType(t *testing.T) {
	body := `
- 三周完成分布式电商系统，技术栈 K8s、Istio、Kafka、RabbitMQ、Elasticsearch、MongoDB、Redis、MySQL`
	intern := `张三 2026 届
XX 科技有限公司 后端开发实习 2025.03-2025.04
` + body
	personal := `张三 2026 届
个人项目 分布式电商系统 2025.03-2025.04
` + body

	di := mustAnalyze(t, AnalyzeOptions{Text: intern, SegmentTypes: map[int]SegmentType{0: TypeInternship}})
	dp := mustAnalyze(t, AnalyzeOptions{Text: personal, SegmentTypes: map[int]SegmentType{0: TypePersonalProject}})

	si, sp := segmentScore(di, 0), segmentScore(dp, 0)
	if si.Type != TypeInternship || sp.Type != TypePersonalProject {
		t.Fatalf("types = %q/%q", si.Type, sp.Type)
	}
	if si.Score == sp.Score {
		t.Fatalf("segment scores equal (%d), expected type-specific deduction", si.Score)
	}
	ii, ip := issueSet(di)["R03"], issueSet(dp)["R03"]
	if ii.Deducted == ip.Deducted {
		t.Fatalf("R03 deduction equal (%d), expected per-type values", ii.Deducted)
	}
	if ip.Deducted <= ii.Deducted {
		t.Fatalf("personal deduction %d should exceed internship %d", ip.Deducted, ii.Deducted)
	}
}

func TestHonestLabelsAreNeitherPenalizedNorRewarded(t *testing.T) {
	text := `张三 2026 届
技能：Go、MySQL、Redis、Linux、HTTP、goroutine、channel、context
个人项目 校园二手平台（方案设计未落地） 2025.03-2025.05
- 三周完成分布式方案设计，未落地；单机部署，20 人小规模内测
- Go + MySQL 实现商品与订单模块，Redis 缓存，Linux 部署，HTTP 接口，goroutine/channel/context 控制并发
- 补 18 个表驱动单测，未压测，未统计可用性`
	diag := mustAnalyze(t, AnalyzeOptions{Text: text})
	for _, id := range []string{"R03", "R07", "R09", "R41"} {
		if issue, ok := issueSet(diag)[id]; ok {
			t.Fatalf("%s should not fire on honest labels: %+v", id, issue)
		}
	}
	if seg := segmentScore(diag, 0); seg.Score != 100 {
		t.Fatalf("segment score = %d, want 100 (no deduction, no bonus)", seg.Score)
	}
}

func TestUnknownTypeReturnsConfirmList(t *testing.T) {
	text := `张三 2026 届
2025.03-2025.06 校园二手交易
- 完成商品搜索与订单模块`
	diag := mustAnalyze(t, AnalyzeOptions{Text: text})
	if !diag.NeedsConfirm || len(diag.NeedsConfirmation) != 1 {
		t.Fatalf("needs_confirm=%v list=%+v", diag.NeedsConfirm, diag.NeedsConfirmation)
	}
	if diag.NeedsConfirmation[0].Title == "" || diag.Segments[0].Type != TypeUnknown {
		t.Fatalf("segment type = %q, confirmation = %+v", diag.Segments[0].Type, diag.NeedsConfirmation[0])
	}

	// The user can override the type and re-analyze.
	fixed := mustAnalyze(t, AnalyzeOptions{
		Text: text, SegmentTypes: map[int]SegmentType{0: TypeInternship},
	})
	if fixed.Segments[0].Type != TypeInternship || fixed.Segments[0].TypeSource != "user" {
		t.Fatalf("override failed: %+v", fixed.Segments[0])
	}
	if fixed.NeedsConfirm || len(fixed.NeedsConfirmation) != 0 {
		t.Fatalf("override should clear confirmation: %+v", fixed.NeedsConfirmation)
	}
}

func TestSectionHeadingsDriveTypeWithoutLeaking(t *testing.T) {
	text := `张三 2026 届
项目经历
校园二手平台 2025.03-2025.04
- 完成商品与订单模块
实习经历
XX 科技有限公司 后端实习 2025.07-2025.09
- 团队 4 人，我负责订单查询接口`

	segs := parseSegments(text, nil)
	if len(segs) != 2 {
		t.Fatalf("segments = %d, want 2", len(segs))
	}
	if segs[0].Type != TypePersonalProject || segs[0].Section != TypePersonalProject {
		t.Fatalf("project segment = %+v, want personal_project under project section", segs[0])
	}
	if segs[1].Type != TypeInternship || segs[1].Section != TypeInternship {
		t.Fatalf("internship segment = %+v, want internship under internship section", segs[1])
	}
	if strings.Contains(segs[0].Raw, "实习经历") {
		t.Fatalf("next section heading leaked into project raw: %q", segs[0].Raw)
	}

	diag := mustAnalyze(t, AnalyzeOptions{Text: text})
	if diag.NeedsConfirm {
		t.Fatalf("section headings should be authoritative, got confirmation: %+v", diag.NeedsConfirmation)
	}
}

func TestEmptyResumeRejected(t *testing.T) {
	if _, err := DefaultAnalyzer().Analyze(AnalyzeOptions{Text: "   "}); err == nil {
		t.Fatal("empty resume accepted")
	}
	if _, err := DefaultAnalyzer().Analyze(AnalyzeOptions{Text: "张三", SegmentTypes: map[int]SegmentType{0: "bogus"}}); err == nil {
		t.Fatal("invalid segment type accepted")
	}
}

func TestRulesetLoadsAndAutoRulesAllHaveDetectors(t *testing.T) {
	rs, err := DefaultRuleset()
	if err != nil {
		t.Fatalf("load rules: %v", err)
	}
	if len(rs.Checks) != 41 {
		t.Fatalf("checks = %d, want 41 (40 research + R41 product rule)", len(rs.Checks))
	}
	manual := map[string]bool{"R11": true, "R19": true, "R21": true, "R24": true, "R25": true, "R29": true, "R30": true, "R35": true, "R37": true}
	for _, check := range rs.Checks {
		if check.Mode == "manual" {
			if !manual[check.ID] {
				t.Fatalf("%s marked manual but not documented as manual", check.ID)
			}
			continue
		}
		if !hasDetector(check.ID) {
			t.Fatalf("%s is %s but has no detector", check.ID, check.Mode)
		}
	}
	if strings.TrimSpace(rs.Version) == "" {
		t.Fatal("rules version is empty")
	}
}

func TestTypedRubricWeights(t *testing.T) {
	rs := MustDefaultRuleset()
	intern, ok := rs.ProfileFor(TypeInternship)
	if !ok {
		t.Fatal("internship profile missing")
	}
	personal, ok := rs.ProfileFor(TypePersonalProject)
	if !ok {
		t.Fatal("personal profile missing")
	}
	names := func(p TypedProfile) string {
		var parts []string
		for _, d := range p.Dimensions {
			parts = append(parts, d.Name)
		}
		return strings.Join(parts, "/")
	}
	if names(intern) != "真实性与边界/结果与影响/岗位匹配/结构表达/格式" {
		t.Fatalf("internship dimensions = %s", names(intern))
	}
	if names(personal) != "工程自觉/可验证性/技术深度/结构表达/格式" {
		t.Fatalf("personal dimensions = %s", names(personal))
	}
	if total := func(p TypedProfile) int {
		n := 0
		for _, d := range p.Dimensions {
			n += d.Weight
		}
		return n
	}(intern); total != 100 {
		t.Fatalf("internship weights = %d", total)
	}
}
