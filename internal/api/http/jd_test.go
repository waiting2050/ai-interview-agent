package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const jdTestText = `公司：星海科技
岗位：Go 后端开发工程师
岗位职责
- 使用 Go、MySQL、Redis 开发订单履约系统
任职要求
1. 3 年以上后端开发经验，熟悉 Kafka 与分布式系统
加分项
- 有 Kubernetes 经验者优先
`

const jdTestResume = `3 年 Go 后端经验。
技能：Go、MySQL、Redis
项目：订单履约系统
- 负责订单履约与库存链路，用 Redis 缓存热点数据
`

func TestJDAnalyzeExposesWeakSkillChecks(t *testing.T) {
	srv, _, _ := newTestRouter(t)
	const jdText = `岗位：后端开发
任职要求
1. 熟悉 Kafka`
	const resumeText = `技能：消息队列、MQ 使用经验`
	resp, err := http.Post(srv.URL+"/v1/jd/analyze", "application/json",
		strings.NewReader(`{"text":`+jsonString(jdText)+`,"resume_text":`+jsonString(resumeText)+`}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out struct {
		Match struct {
			WeakKeywords []string `json:"weak_keywords"`
			SkillChecks  []struct {
				Skill   string `json:"skill"`
				Status  string `json:"status"`
				Matched string `json:"matched"`
				Source  string `json:"source"`
			} `json:"skill_checks"`
		} `json:"match"`
		Gaps struct {
			WeakSkills []struct {
				Skill string `json:"skill"`
			} `json:"weak_skills"`
		} `json:"gaps"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if !containsString(out.Match.WeakKeywords, "Kafka") {
		t.Fatalf("weak keywords = %v", out.Match.WeakKeywords)
	}
	found := false
	for _, check := range out.Match.SkillChecks {
		if check.Skill != "Kafka" {
			continue
		}
		found = true
		if check.Status != "weak" || check.Source != "parent" || check.Matched != "消息队列" {
			t.Fatalf("check = %+v", check)
		}
	}
	if !found {
		t.Fatalf("Kafka check missing: %+v", out.Match.SkillChecks)
	}
	if len(out.Gaps.WeakSkills) != 1 || out.Gaps.WeakSkills[0].Skill != "Kafka" {
		t.Fatalf("weak skills = %+v", out.Gaps.WeakSkills)
	}
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func TestJDAnalyzeGetAndKit(t *testing.T) {
	srv, _, _ := newTestRouter(t)

	resp, err := http.Post(srv.URL+"/v1/jd/analyze", "application/json",
		strings.NewReader(`{"text":`+jsonString(jdTestText)+`,"resume_text":`+jsonString(jdTestResume)+`}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	var analysis struct {
		JDID        string `json:"jd_id"`
		SourceKind  string `json:"source_kind"`
		ModelCalled bool   `json:"model_called"`
		Structured  struct {
			Role           string   `json:"role"`
			RequiredSkills []string `json:"required_skills"`
		} `json:"structured"`
		Match struct {
			Available bool `json:"available"`
			Score     int  `json:"score"`
		} `json:"match"`
		Gaps struct {
			MissingSkills []string `json:"missing_skills"`
		} `json:"gaps"`
		Outline []struct {
			Topic string `json:"topic"`
		} `json:"outline"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&analysis); err != nil {
		t.Fatal(err)
	}
	if analysis.SourceKind != "paste" || analysis.ModelCalled {
		t.Fatalf("analysis = %+v", analysis)
	}
	if analysis.Structured.Role != "Go 后端开发工程师" {
		t.Fatalf("role = %q", analysis.Structured.Role)
	}
	if !analysis.Match.Available || analysis.Match.Score <= 0 {
		t.Fatalf("match = %+v", analysis.Match)
	}
	if len(analysis.Gaps.MissingSkills) == 0 {
		t.Fatal("expected Kafka as a gap")
	}
	if len(analysis.Outline) == 0 {
		t.Fatal("outline is empty")
	}
	if analysis.JDID == "" {
		t.Fatal("jd_id is empty")
	}

	getResp, err := http.Get(srv.URL + "/v1/jd/" + analysis.JDID)
	if err != nil {
		t.Fatal(err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("get status = %d", getResp.StatusCode)
	}
	var listing struct {
		ID         string `json:"id"`
		SourceKind string `json:"source_kind"`
		RawText    string `json:"raw_text"`
	}
	if err := json.NewDecoder(getResp.Body).Decode(&listing); err != nil {
		t.Fatal(err)
	}
	if listing.ID != analysis.JDID || listing.SourceKind != "paste" {
		t.Fatalf("listing = %+v", listing)
	}
	if !strings.Contains(listing.RawText, "Kafka") {
		t.Fatal("raw text was not cached")
	}

	kitResp, err := http.Post(srv.URL+"/v1/jd/"+analysis.JDID+"/kit", "application/json",
		strings.NewReader(`{"resume_text":`+jsonString(jdTestResume)+`,"candidate_name":"张三"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer kitResp.Body.Close()
	if kitResp.StatusCode != http.StatusOK {
		t.Fatalf("kit status = %d", kitResp.StatusCode)
	}
	var kit struct {
		Summary     string `json:"summary"`
		Opener      string `json:"opener"`
		Markdown    string `json:"markdown"`
		ModelCalled bool   `json:"model_called"`
	}
	if err := json.NewDecoder(kitResp.Body).Decode(&kit); err != nil {
		t.Fatal(err)
	}
	if kit.ModelCalled {
		t.Fatal("kit must be rule-generated")
	}
	if !strings.Contains(kit.Summary, "订单履约系统") {
		t.Fatalf("summary = %q", kit.Summary)
	}
	if !strings.Contains(kit.Markdown, "不提供自动投递") {
		t.Fatal("markdown must state no auto-apply")
	}
}

func TestJDAnalyzeValidationAndErrors(t *testing.T) {
	srv, _, _ := newTestRouter(t)

	resp, err := http.Post(srv.URL+"/v1/jd/analyze", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty status = %d", resp.StatusCode)
	}
	var body errorBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "invalid_argument" {
		t.Fatalf("code = %q", body.Code)
	}

	// The test router has no fetcher: URL-only requests surface as 502 so the
	// UI can fall back to pasting text.
	resp2, err := http.Post(srv.URL+"/v1/jd/analyze", "application/json",
		strings.NewReader(`{"url":"https://jobs.example.com/1"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadGateway {
		t.Fatalf("url status = %d", resp2.StatusCode)
	}
	var fetchBody errorBody
	if err := json.NewDecoder(resp2.Body).Decode(&fetchBody); err != nil {
		t.Fatal(err)
	}
	if fetchBody.Code != "jd_fetch_failed" {
		t.Fatalf("code = %q", fetchBody.Code)
	}

	resp3, err := http.Get(srv.URL + "/v1/jd/missing")
	if err != nil {
		t.Fatal(err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusNotFound {
		t.Fatalf("missing status = %d", resp3.StatusCode)
	}
}

func jsonString(s string) string {
	data, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(data)
}
