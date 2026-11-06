package resume

import (
	"strings"
	"testing"
)

func TestParseDerivesStructuredFields(t *testing.T) {
	text := `三年 Go 后端开发经验，熟悉 MySQL、Redis 与 Kafka。
项目：支付订单系统，负责核心交易链路与对账。
项目：消息网关，使用 gRPC 和 Kubernetes 部署。`

	profile := Parse(text)
	if profile.Years != 3 {
		t.Fatalf("years = %d, want 3", profile.Years)
	}
	for _, want := range []string{"Go", "MySQL", "Redis", "Kafka", "gRPC", "Kubernetes"} {
		if !hasSkill(profile.Skills, want) {
			t.Fatalf("missing skill %s in %v", want, profile.Skills)
		}
	}
	if len(profile.Projects) != 2 {
		t.Fatalf("projects = %d, want 2: %+v", len(profile.Projects), profile.Projects)
	}
	if profile.Projects[0].Name == "" || len(profile.Projects[0].Highlights) == 0 {
		t.Fatalf("project not populated: %+v", profile.Projects[0])
	}
	if !strings.Contains(profile.Text(), "工作年限：3 年") {
		t.Fatalf("text missing years: %s", profile.Text())
	}
}

func TestNormalizeDeduplicatesAndCaps(t *testing.T) {
	profile := Profile{
		Years:  99,
		Skills: []string{" Go ", "go", "MySQL", ""},
		Projects: []Project{
			{Name: "  ", Highlights: []string{" ", "实现订单编排"}},
		},
	}
	profile.Normalize()
	if profile.Years != 60 {
		t.Fatalf("years = %d", profile.Years)
	}
	if len(profile.Skills) != 2 {
		t.Fatalf("skills = %v", profile.Skills)
	}
	if len(profile.Projects) != 1 || profile.Projects[0].Highlights[0] != "实现订单编排" {
		t.Fatalf("projects = %+v", profile.Projects)
	}
}

func TestParseEmptyTextIsZero(t *testing.T) {
	profile := Parse("   ")
	if !profile.IsZero() {
		t.Fatalf("expected zero profile, got %+v", profile)
	}
}

func hasSkill(skills []string, want string) bool {
	for _, skill := range skills {
		if strings.EqualFold(skill, want) {
			return true
		}
	}
	return false
}
