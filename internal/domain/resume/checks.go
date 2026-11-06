package resume

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// hit is one rule match. Segment is -1 for document-level findings.
type hit struct {
	Segment int
	Quote   string
	Note    string
}

type detector func(*analysisContext) []hit

type bullet struct {
	Segment int
	Text    string
}

type analysisContext struct {
	text       string
	lines      []string
	segments   []*Segment
	bullets    []bullet
	keywords   []string
	matched    []string
	missing    []string
	coverage   float64
	skillsLine string
}

var detectors = map[string]detector{
	"R01": detectR01,
	"R02": detectR02,
	"R03": detectR03,
	"R04": detectR04,
	"R05": detectR05,
	"R06": detectR06,
	"R07": detectR07,
	"R08": detectR08,
	"R09": detectR09,
	"R10": detectR10,
	"R12": detectR12,
	"R13": detectR13,
	"R14": detectR14,
	"R15": detectR15,
	"R16": detectR16,
	"R17": detectR17,
	"R18": detectR18,
	"R20": detectR20,
	"R22": detectR22,
	"R23": detectR23,
	"R26": detectR26,
	"R27": detectR27,
	"R28": detectR28,
	"R31": detectR31,
	"R32": detectR32,
	"R33": detectR33,
	"R34": detectR34,
	"R36": detectR36,
	"R38": detectR38,
	"R39": detectR39,
	"R40": detectR40,
	"R41": detectR41,
}

func hasDetector(id string) bool {
	_, ok := detectors[id]
	return ok
}

func (c *analysisContext) segmentsOf(t SegmentType) []*Segment {
	out := make([]*Segment, 0, len(c.segments))
	for _, seg := range c.segments {
		if seg.Type == t {
			out = append(out, seg)
		}
	}
	return out
}

func (c *analysisContext) knownSegments() []*Segment {
	out := make([]*Segment, 0, len(c.segments))
	for _, seg := range c.segments {
		if seg.Type != TypeUnknown {
			out = append(out, seg)
		}
	}
	return out
}

func containsAny(s string, words ...string) bool {
	for _, word := range words {
		if strings.Contains(s, word) {
			return true
		}
	}
	return false
}

func countAny(s string, words []string) int {
	n := 0
	for _, word := range words {
		n += strings.Count(s, word)
	}
	return n
}

func (c *analysisContext) firstLine(re *regexp.Regexp) string {
	for _, line := range c.lines {
		if re.MatchString(line) {
			return truncate(strings.TrimSpace(line), 160)
		}
	}
	return ""
}

func segLine(seg *Segment, re *regexp.Regexp) string {
	for _, line := range seg.Lines {
		if re.MatchString(line) {
			return truncate(strings.TrimSpace(line), 160)
		}
	}
	return ""
}

func segQuote(seg *Segment) string {
	if q := segLine(seg, regexp.MustCompile(`\S`)); q != "" {
		return q
	}
	return truncate(seg.Title, 160)
}

func (seg *Segment) hasHonest(labels ...string) bool {
	for _, label := range labels {
		for _, actual := range seg.HonestLabels {
			if actual == label {
				return true
			}
		}
	}
	return false
}

func honestProtectsScope(seg *Segment) bool {
	return seg.hasHonest("方案设计未落地", "未落地", "仅方案", "规划中", "课程了解")
}

func honestProtectsDeploy(seg *Segment) bool {
	return seg.hasHonest("单机部署", "单机", "小规模内测", "内测", "未对外", "未公网", "测试机", "本地压测", "未上线")
}

var (
	reShortPeriod       = regexp.MustCompile(`三周|3\s*周|两周|2\s*周|一个月|1\s*个月`)
	reScopeClaim        = regexp.MustCompile(`分布式|高并发|微服务|多租户|容灾|集群|高可用`)
	reStrongVerb        = regexp.MustCompile(`主导|独立负责|独立完成|从\s*0\s*到\s*1|从零到一|架构师|技术负责人|团队负责人|带\s*\d+\s*人`)
	reBoundary          = regexp.MustCompile(`团队\s*\d+\s*人|\d+\s*人团队|负责|我主写|另\s*\d+\s*人|review|评审|导师|模块|接口|链路|联调`)
	reBigScope          = regexp.MustCompile(`千万用户|百万用户|整个项目|全链路|开发了[^。\n]{0,20}(系统|平台)`)
	reAttribution       = regexp.MustCompile(`基于|二次开发|原有|开源|我新增|我参与|团队|另\s*\d+\s*人|负责`)
	reProduction        = regexp.MustCompile(`已上线|上线|生产环境|正式环境|对外服务|支撑\s*\d+\s*(用户|人)`)
	reDeploy            = regexp.MustCompile(`部署|监控|埋点|入口|域名|公网|内测|测试机|云主机|Docker|Compose|未对外|本地|单机|未上线`)
	rePerfToken         = regexp.MustCompile(`(?i)P99|P95|P90|平均|耗时|延迟|响应时间|QPS|吞吐`)
	rePerfChange        = regexp.MustCompile(`优化|降低|降到|降至|提升|缩短|从\s*\d+|[0-9]\s*(?:ms|毫秒|s|秒)\s*(?:到|至|→|->)`)
	reBaseline          = regexp.MustCompile(`从\s*\d|→|->|降至|降低到|提升至|由\s*\d|v\d|对比|基线`)
	reEnv               = regexp.MustCompile(`环境|数据量|并发|压测|wrk|4C|8G|核|机器|内存|条数据|数据集|分区|监控|埋点`)
	reMagnitude         = regexp.MustCompile(`99\.99|99\.9%|百万用户|千万用户|千万级|十万\s*QPS|10\s*万\s*QPS|日均\s*[0-9]+\s*万`)
	reMagnitudeEvidence = regexp.MustCompile(`压测|监控|埋点|统计|部署|内测|本地|单机|测试|来源`)
	reVague             = regexp.MustCompile(`某互联网公司|某大厂|某公司|核心项目`)
	reLink              = regexp.MustCompile(`(?i)http|github|gitee|链接|仓库|readme`)
	reLevel             = regexp.MustCompile(`架构师|技术负责人|团队负责人|带\s*\d+\s*人`)
	reStudent           = regexp.MustCompile(`202\d\s*届|应届|校招|在读|本科|硕士|专科`)
	reNoResult          = regexp.MustCompile(`^(负责|参与|协助|配合|完成)[^。；\n]{0,40}(开发|维护|实现|重构|联调|测试)$`)
	reAdjective         = regexp.MustCompile(`优化了?[^。\n]{0,8}(性能|体验|效率)|大幅提升|明显改善|显著提升|性能优化`)
	rePerfTo            = regexp.MustCompile(`(?:优化|降低|缩短)(?:到|至)\s*\d+`)
	reMixMetric         = regexp.MustCompile(`平均[^。\n]{0,20}(P95|P99)|(P95|P99)[^。\n]{0,20}平均`)
	reHighConcurrency   = regexp.MustCompile(`高并发|大流量|海量请求|海量数据`)
	reTooPrecise        = regexp.MustCompile(`\d+\.\d+\s*%`)
	reMetricValue       = regexp.MustCompile(`(P99|P95|平均|QPS|可用性)[^。\n]{0,12}(?:降至|降到|优化到|提升到|为|是)\s*(\d+(?:\.\d+)?)`)
	reParticipation     = regexp.MustCompile(`参与|协助|配合|负责部分|部分参与`)
	reIntention         = regexp.MustCompile(`求职意向|意向`)
	reUnrelated         = regexp.MustCompile(`社团|销售|客服|兼职|支教|校园大使`)
	reSkillBar          = regexp.MustCompile(`█|★|☆|熟练度|掌握度|\d+\s*/\s*10|(?:精通|熟练|掌握|了解)[^。\n]{0,8}\d+\s*%`)
	reDotDate           = regexp.MustCompile(`20\d{2}\.\d{1,2}`)
	reCNDate            = regexp.MustCompile(`20\d{2}年\d{1,2}月`)
	reDashDate          = regexp.MustCompile(`20\d{2}-\d{1,2}`)
	reEducation         = regexp.MustCompile(`教育|学历`)
	reDegree            = regexp.MustCompile(`本科|硕士|博士|专科|大专|研究生|学士`)
	reMajor             = regexp.MustCompile(`专业|软件工程|计算机|通信|电子|数学|管理|科学`)
	reGraduate          = regexp.MustCompile(`毕业|预计|在读|202\d\s*届|20\d{2}\.\d{1,2}`)
	reProductionClaim   = regexp.MustCompile(`企业级|生产系统|上线系统|商用|生产环境`)
	reTutorial          = regexp.MustCompile(`课程|教程|跟着|仿|clone|demo|练习|作业|毕设`)
	reTypo              = regexp.MustCompile(`的的|了了|在次|以经`)
	reBadCase           = regexp.MustCompile(`golang|MySql|mySQL|Mysql|Ｒｅｄｉｓ|Ｇｏｌａｎｇ`)
	reFullWidth         = regexp.MustCompile(`[Ａ-Ｚａ-ｚ０-９]`)
)

func detectR01(c *analysisContext) []hit {
	internships := c.segmentsOf(TypeInternship)
	if len(internships) < 2 {
		return nil
	}
	if containsAny(c.text, "兼职", "远程", "交接", "并行") {
		return nil
	}
	for i := 0; i < len(internships); i++ {
		for j := i + 1; j < len(internships); j++ {
			a, b := internships[i], internships[j]
			if a.Span == nil || b.Span == nil {
				continue
			}
			if a.Span.StartIndex() <= b.Span.EndIndex() && b.Span.StartIndex() <= a.Span.EndIndex() {
				return []hit{{
					Segment: a.Index,
					Quote:   fmt.Sprintf("%s / %s", truncate(a.Title, 60), truncate(b.Title, 60)),
				}}
			}
		}
	}
	return nil
}

func detectR02(c *analysisContext) []hit {
	internships := sortedSegmentsByStart(c.segmentsOf(TypeInternship))
	if len(internships) < 2 {
		return nil
	}
	for i := 1; i < len(internships); i++ {
		prev, next := internships[i-1], internships[i]
		if prev.Span.Ongoing || next.Span.Ongoing {
			continue
		}
		gap := next.Span.StartIndex() - prev.Span.EndIndex() - 1
		if gap > 6 && !containsAny(c.text, "备考", "自学", "考研", "休学", "病假", "家庭", "待业", "空档") {
			return []hit{{
				Segment: next.Index,
				Quote:   fmt.Sprintf("%s 至 %s 之间约 %d 个月无内容", prev.Span.Raw, next.Span.Raw, gap),
			}}
		}
	}
	return nil
}

func detectR03(c *analysisContext) []hit {
	var hits []hit
	for _, seg := range c.segments {
		if seg.Type == TypeUnknown || honestProtectsScope(seg) {
			continue
		}
		short := (seg.Months > 0 && seg.Months <= 1) || reShortPeriod.MatchString(seg.Raw)
		if short && reScopeClaim.MatchString(seg.Raw) {
			q := segLine(seg, reScopeClaim)
			if q == "" {
				q = segQuote(seg)
			}
			hits = append(hits, hit{Segment: seg.Index, Quote: q})
		}
	}
	return hits
}

func detectR04(c *analysisContext) []hit {
	var hits []hit
	for _, seg := range c.segments {
		if seg.Type == TypeUnknown {
			continue
		}
		if seg.Months > 0 && seg.Months <= 3 && seg.TechCount >= 8 {
			hits = append(hits, hit{
				Segment: seg.Index,
				Quote:   truncate(seg.Title+"："+strings.Join(seg.TechStack, "、"), 160),
			})
		}
	}
	return hits
}

func detectR05(c *analysisContext) []hit {
	var hits []hit
	for _, seg := range c.segments {
		if seg.Type == TypeUnknown {
			continue
		}
		if reStrongVerb.MatchString(seg.Raw) && !reBoundary.MatchString(seg.Raw) {
			hits = append(hits, hit{Segment: seg.Index, Quote: segLine(seg, reStrongVerb)})
		}
	}
	return hits
}

func detectR06(c *analysisContext) []hit {
	var hits []hit
	for _, seg := range c.segments {
		if seg.Type == TypeUnknown {
			continue
		}
		if reBigScope.MatchString(seg.Raw) && !reAttribution.MatchString(seg.Raw) {
			hits = append(hits, hit{Segment: seg.Index, Quote: segLine(seg, reBigScope)})
		}
	}
	return hits
}

func detectR07(c *analysisContext) []hit {
	var hits []hit
	for _, seg := range c.segments {
		if seg.Type == TypeUnknown || honestProtectsDeploy(seg) {
			continue
		}
		if reProduction.MatchString(seg.Raw) && !reDeploy.MatchString(seg.Raw) {
			hits = append(hits, hit{Segment: seg.Index, Quote: segLine(seg, reProduction)})
		}
	}
	return hits
}

func metricNeedsContext(line string) bool {
	if !rePerfToken.MatchString(line) || !rePerfChange.MatchString(line) {
		return false
	}
	if !regexp.MustCompile(`[0-9]`).MatchString(line) {
		return false
	}
	return !reBaseline.MatchString(line) || !reEnv.MatchString(line)
}

func detectR08(c *analysisContext) []hit {
	var hits []hit
	for _, seg := range c.segments {
		if seg.Type == TypeUnknown {
			continue
		}
		for _, line := range seg.Lines {
			if metricNeedsContext(line) {
				hits = append(hits, hit{Segment: seg.Index, Quote: truncate(strings.TrimSpace(line), 160)})
				break
			}
		}
	}
	return hits
}

func detectR09(c *analysisContext) []hit {
	var hits []hit
	for _, seg := range c.segments {
		if seg.Type == TypeUnknown {
			continue
		}
		if !reMagnitude.MatchString(seg.Raw) {
			continue
		}
		if seg.hasHonest("未统计", "未做多副本") {
			continue
		}
		if reMagnitudeEvidence.MatchString(seg.Raw) {
			continue
		}
		hits = append(hits, hit{Segment: seg.Index, Quote: segLine(seg, reMagnitude)})
	}
	return hits
}

func detectR10(c *analysisContext) []hit {
	var hits []hit
	for _, seg := range c.segments {
		if seg.Type == TypeUnknown {
			continue
		}
		if reVague.MatchString(seg.Raw) && !reLink.MatchString(seg.Raw) {
			hits = append(hits, hit{Segment: seg.Index, Quote: segLine(seg, reVague)})
		}
	}
	return hits
}

func detectR12(c *analysisContext) []hit {
	var hits []hit
	for _, seg := range c.segments {
		if seg.Type == TypeUnknown {
			continue
		}
		if reLevel.MatchString(seg.Raw) && reStudent.MatchString(c.text) {
			hits = append(hits, hit{Segment: seg.Index, Quote: segLine(seg, reLevel)})
		}
	}
	return hits
}

func detectR13(c *analysisContext) []hit {
	var hits []hit
	for _, seg := range c.segments {
		if seg.Type == TypeUnknown {
			continue
		}
		for _, line := range seg.Lines {
			text := strings.TrimSpace(strings.TrimLeft(line, "-•·* "))
			if reNoResult.MatchString(text) && !regexp.MustCompile(`[0-9%]`).MatchString(text) {
				hits = append(hits, hit{Segment: seg.Index, Quote: truncate(text, 160)})
				break
			}
		}
	}
	return hits
}

func detectR14(c *analysisContext) []hit {
	var hits []hit
	for _, seg := range c.segments {
		if seg.Type == TypeUnknown {
			continue
		}
		for _, line := range seg.Lines {
			if reAdjective.MatchString(line) && !regexp.MustCompile(`[0-9]`).MatchString(line) {
				hits = append(hits, hit{Segment: seg.Index, Quote: truncate(strings.TrimSpace(line), 160)})
				break
			}
		}
	}
	return hits
}

func detectR15(c *analysisContext) []hit {
	var hits []hit
	for _, seg := range c.segments {
		if seg.Type == TypeUnknown {
			continue
		}
		for _, line := range seg.Lines {
			if rePerfTo.MatchString(line) && rePerfToken.MatchString(line) && !reBaseline.MatchString(line) {
				hits = append(hits, hit{Segment: seg.Index, Quote: truncate(strings.TrimSpace(line), 160)})
				break
			}
		}
	}
	return hits
}

func detectR16(c *analysisContext) []hit {
	var hits []hit
	for _, seg := range c.segments {
		if seg.Type == TypeUnknown {
			continue
		}
		for _, line := range seg.Lines {
			if reMixMetric.MatchString(line) {
				hits = append(hits, hit{Segment: seg.Index, Quote: truncate(strings.TrimSpace(line), 160)})
				break
			}
		}
	}
	return hits
}

func detectR17(c *analysisContext) []hit {
	var hits []hit
	for _, seg := range c.segments {
		if seg.Type == TypeUnknown {
			continue
		}
		hasMetric := false
		hasEnv := false
		for _, line := range seg.Lines {
			if rePerfToken.MatchString(line) && rePerfChange.MatchString(line) && regexp.MustCompile(`[0-9]`).MatchString(line) {
				hasMetric = true
				if reEnv.MatchString(line) {
					hasEnv = true
				}
			}
		}
		if hasMetric && !hasEnv {
			hits = append(hits, hit{Segment: seg.Index, Quote: segQuote(seg)})
		}
	}
	return hits
}

func detectR18(c *analysisContext) []hit {
	var hits []hit
	for _, seg := range c.segments {
		if seg.Type == TypeUnknown {
			continue
		}
		for _, line := range seg.Lines {
			if reHighConcurrency.MatchString(line) && !regexp.MustCompile(`[0-9]`).MatchString(line) {
				hits = append(hits, hit{Segment: seg.Index, Quote: truncate(strings.TrimSpace(line), 160)})
				break
			}
		}
	}
	return hits
}

func detectR20(c *analysisContext) []hit {
	var hits []hit
	for _, line := range c.lines {
		if reTooPrecise.MatchString(line) && !containsAny(line, "约", "左右", "统计", "埋点", "来源", "折算") {
			hits = append(hits, hit{Segment: -1, Quote: truncate(strings.TrimSpace(line), 160)})
			break
		}
	}
	return hits
}

func detectR22(c *analysisContext) []hit {
	var hits []hit
	for _, seg := range c.segments {
		if seg.Type == TypeUnknown {
			continue
		}
		values := map[string]map[string]bool{}
		for _, line := range seg.Lines {
			m := reMetricValue.FindStringSubmatch(line)
			if len(m) != 3 {
				continue
			}
			metric := m[1]
			if values[metric] == nil {
				values[metric] = map[string]bool{}
			}
			values[metric][m[2]] = true
		}
		for metric, set := range values {
			if len(set) > 1 {
				hits = append(hits, hit{
					Segment: seg.Index,
					Quote:   fmt.Sprintf("%s 在同一段出现 %d 个不同目标值", metric, len(set)),
				})
				break
			}
		}
	}
	return hits
}

func detectR23(c *analysisContext) []hit {
	var hits []hit
	for _, seg := range c.segments {
		if seg.Type == TypeUnknown {
			continue
		}
		body := 0
		tooLong := ""
		for _, line := range seg.Lines {
			text := strings.TrimSpace(line)
			if text == "" {
				continue
			}
			body++
			if len([]rune(text)) > 200 && tooLong == "" {
				tooLong = truncate(text, 160)
			}
		}
		if tooLong != "" || body > 8 {
			if tooLong == "" {
				tooLong = segQuote(seg)
			}
			hits = append(hits, hit{Segment: seg.Index, Quote: tooLong})
		}
	}
	return hits
}

func detectR26(c *analysisContext) []hit {
	if len(c.bullets) < 3 {
		return nil
	}
	participations := 0
	first := ""
	for _, b := range c.bullets {
		if reParticipation.MatchString(b.Text) {
			participations++
			if first == "" {
				first = truncate(b.Text, 160)
			}
		}
	}
	if participations == 0 || float64(participations)/float64(len(c.bullets)) <= 0.5 {
		return nil
	}
	return []hit{{Segment: -1, Quote: first}}
}

func detectR27(c *analysisContext) []hit {
	words := []string{"主导", "独立负责", "独立完成", "从0到1", "从 0 到 1", "从零到一"}
	if countAny(c.text, words) < 3 {
		return nil
	}
	return []hit{{Segment: -1, Quote: c.firstLine(reStrongVerb)}}
}

func detectR28(c *analysisContext) []hit {
	known := c.knownSegments()
	if len(known) < 2 {
		return nil
	}
	first := known[0]
	if first.Type != TypeCoursework && first.Type != TypeCompetition {
		return nil
	}
	for _, seg := range known[1:] {
		if seg.Type == TypeInternship || seg.Type == TypePersonalProject {
			return []hit{{Segment: first.Index, Quote: truncate(first.Title, 160)}}
		}
	}
	return nil
}

func detectR31(c *analysisContext) []hit {
	if len(c.keywords) == 0 || c.coverage >= 0.5 {
		return nil
	}
	quote := c.skillsLine
	if quote == "" {
		quote = c.firstLine(regexp.MustCompile(`技能|技术栈`))
	}
	if quote == "" {
		quote = "（全文未命中目标岗位关键词）"
	}
	return []hit{{Segment: -1, Quote: truncate(quote, 160), Note: "缺失：" + strings.Join(c.missing, "、")}}
}

var splitSkillLineRe = regexp.MustCompile(`[：:]`)

func detectR32(c *analysisContext) []hit {
	if c.skillsLine == "" || !containsAny(c.text, "项目") {
		return nil
	}
	line := splitSkillLineRe.Split(c.skillsLine, 2)
	if len(line) != 2 {
		return nil
	}
	skills := splitList(line[1])
	if len(skills) == 0 {
		return nil
	}
	rest := strings.Replace(c.text, c.skillsLine, "", 1)
	var missing []string
	for _, skill := range skills {
		name := strings.TrimSpace(skill)
		if len([]rune(name)) == 0 || len([]rune(name)) > 24 {
			continue
		}
		if !strings.Contains(strings.ToLower(rest), strings.ToLower(name)) {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return []hit{{Segment: -1, Quote: truncate(c.skillsLine, 160), Note: "项目中未使用：" + strings.Join(missing, "、")}}
}

func detectR33(c *analysisContext) []hit {
	roles := []string{"后端", "算法", "前端", "测试", "产品", "运营"}
	for _, line := range c.lines {
		if !reIntention.MatchString(line) {
			continue
		}
		if countAny(line, roles) >= 2 {
			return []hit{{Segment: -1, Quote: truncate(strings.TrimSpace(line), 160)}}
		}
	}
	return nil
}

func detectR34(c *analysisContext) []hit {
	total := len([]rune(c.text))
	if total < 200 {
		return nil
	}
	unrelated := 0
	quote := ""
	for _, line := range c.lines {
		if reUnrelated.MatchString(line) && !containsAny(line, "Go", "Java", "后端", "开发", "项目", "技术") {
			unrelated += len([]rune(line))
			if quote == "" {
				quote = truncate(strings.TrimSpace(line), 160)
			}
		}
	}
	if quote == "" || float64(unrelated)/float64(total) <= 0.3 {
		return nil
	}
	return []hit{{Segment: -1, Quote: quote}}
}

func detectR36(c *analysisContext) []hit {
	if reTypo.MatchString(c.text) {
		return []hit{{Segment: -1, Quote: c.firstLine(reTypo)}}
	}
	if m := reBadCase.FindString(c.text); m != "" {
		return []hit{{Segment: -1, Quote: c.firstLine(reBadCase)}}
	}
	if reFullWidth.MatchString(c.text) {
		return []hit{{Segment: -1, Quote: c.firstLine(reFullWidth)}}
	}
	zhComma := strings.Count(c.text, "，")
	enComma := strings.Count(c.text, ",")
	zhColon := strings.Count(c.text, "：")
	enColon := strings.Count(c.text, ":")
	if (zhComma > 0 && enComma >= 2) || (zhColon > 0 && enColon >= 2) {
		return []hit{{Segment: -1, Quote: "中英文标点混用（，/, 或 ：/:）"}}
	}
	return nil
}

func detectR38(c *analysisContext) []hit {
	if line := c.firstLine(reSkillBar); line != "" {
		return []hit{{Segment: -1, Quote: line}}
	}
	for _, line := range c.lines {
		if containsAny(line, "技能", "熟练", "掌握", "了解") && regexp.MustCompile(`\d+\s*%`).MatchString(line) {
			return []hit{{Segment: -1, Quote: truncate(strings.TrimSpace(line), 160)}}
		}
	}
	return nil
}

func detectR39(c *analysisContext) []hit {
	formats := 0
	if reDotDate.MatchString(c.text) {
		formats++
	}
	if reCNDate.MatchString(c.text) {
		formats++
	}
	if reDashDate.MatchString(c.text) {
		formats++
	}
	if formats < 2 {
		return nil
	}
	quote := c.firstLine(reCNDate)
	if quote == "" {
		quote = c.firstLine(reDashDate)
	}
	if quote == "" {
		quote = c.firstLine(reDotDate)
	}
	return []hit{{Segment: -1, Quote: quote}}
}

func detectR40(c *analysisContext) []hit {
	if !reEducation.MatchString(c.text) {
		return nil
	}
	if reDegree.MatchString(c.text) && reMajor.MatchString(c.text) && reGraduate.MatchString(c.text) {
		return nil
	}
	return []hit{{Segment: -1, Quote: c.firstLine(reEducation)}}
}

func detectR41(c *analysisContext) []hit {
	var hits []hit
	for _, seg := range c.segments {
		switch seg.Type {
		case TypePersonalProject, TypeCoursework, TypeCompetition, TypeOpenSource:
		default:
			continue
		}
		if honestProtectsScope(seg) || honestProtectsDeploy(seg) {
			continue
		}
		if !reProductionClaim.MatchString(seg.Raw) {
			continue
		}
		if !reTutorial.MatchString(seg.Raw) && !reProduction.MatchString(seg.Raw) {
			continue
		}
		if reTutorial.MatchString(seg.Raw) || !reDeploy.MatchString(seg.Raw) {
			hits = append(hits, hit{Segment: seg.Index, Quote: segLine(seg, reProductionClaim)})
		}
	}
	return hits
}

// detectorIDs lists rules that must have an executable detector. R11/R19/R21/
// R24/R25/R29/R30/R35/R37 are manual by design and have none.
func detectorIDs() []string {
	ids := make([]string, 0, len(detectors))
	for id := range detectors {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
