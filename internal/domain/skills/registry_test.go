package skills_test

import (
	"testing"

	"github.com/waiting2050/ai-interview-agent/internal/domain/skills"
)

func TestNormalizeNameForms(t *testing.T) {
	cases := []struct{ in, want string }{
		{"  K8S  ", "k8s"},
		{"Kubernetes", "kubernetes"},
		{"Go 1.21", "go"},
		{"Go1.21", "go"},
		{"Spring Boot 3.2", "spring boot"},
		{"Spring_Boot", "spring boot"},
		{"spring-boot", "spring boot"},
		{"Node.js", "node js"},
		{"CI/CD", "ci/cd"},
		{"C++", "c++"},
	}
	for _, tc := range cases {
		if got := skills.Normalize(tc.in); got != tc.want {
			t.Fatalf("Normalize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSynonymSpellingsMerge(t *testing.T) {
	cases := []struct {
		text, requirement, matched, source string
	}{
		{"熟悉 k8s 1.27", "Kubernetes", "Kubernetes", skills.SourceAlias},
		{"用 K8S 部署", "Kubernetes", "Kubernetes", skills.SourceAlias},
		{"主力语言 Golang", "Go", "Go", skills.SourceAlias},
		{"Go 1.21 开发", "Go", "Go", skills.SourceExact},
		{"Postgres 15 / pgsql", "PostgreSQL", "PostgreSQL", skills.SourceAlias},
		{"使用 Spring Boot 3.2", "springboot", "Spring Boot", skills.SourceAlias},
		{"Node.js 服务", "nodejs", "Node.js", skills.SourceAlias},
	}
	for _, tc := range cases {
		check := skills.Check(tc.text, tc.requirement)
		if check.Status != skills.StatusCovered || check.Source != tc.source {
			t.Fatalf("%q vs %q = %+v", tc.text, tc.requirement, check)
		}
		if check.Matched != tc.matched {
			t.Fatalf("matched = %q, want %q", check.Matched, tc.matched)
		}
	}
}

func TestHierarchyNeverMergesParentAndChild(t *testing.T) {
	cases := []struct {
		text, requirement, status, source, matched string
	}{
		// A narrower product covers the broader requirement.
		{"有 Kafka 消费经验", "消息队列", skills.StatusCovered, skills.SourceChild, "Kafka"},
		{"用过 Milvus", "向量数据库", skills.StatusCovered, skills.SourceChild, "Milvus"},
		{"熟悉 MySQL", "数据库", skills.StatusCovered, skills.SourceChild, "MySQL"},
		// A broader concept only hints at the concrete requirement.
		{"熟悉消息队列", "Kafka", skills.StatusWeak, skills.SourceParent, "消息队列"},
		{"熟悉 MQ", "Kafka", skills.StatusWeak, skills.SourceParent, "消息队列"},
		{"了解 Spring", "Spring Boot", skills.StatusWeak, skills.SourceParent, "Spring"},
		// Siblings hint at the same family but never count.
		{"用过 RabbitMQ", "Kafka", skills.StatusWeak, skills.SourceSibling, "RabbitMQ"},
		{"熟悉 PostgreSQL", "MySQL", skills.StatusWeak, skills.SourceSibling, "PostgreSQL"},
	}
	for _, tc := range cases {
		check := skills.Check(tc.text, tc.requirement)
		if check.Status != tc.status || check.Source != tc.source || check.Matched != tc.matched {
			t.Fatalf("%q vs %q = %+v", tc.text, tc.requirement, check)
		}
	}
}

func TestRelatedSkillsOnlyHint(t *testing.T) {
	check := skills.Check("用 Docker 打包", "Kubernetes")
	if check.Status != skills.StatusWeak || check.Source != skills.SourceRelated || check.Matched != "Docker" {
		t.Fatalf("check = %+v", check)
	}
}

func TestEditDistanceFallbackStaysConservative(t *testing.T) {
	// A single typo becomes a weak hint, never coverage.
	typo := skills.Check("熟悉 kubenetes", "Kubernetes")
	if typo.Status != skills.StatusWeak || typo.Source != skills.SourceFuzzy {
		t.Fatalf("typo check = %+v", typo)
	}
	// Different products must not be merged.
	for _, tc := range []struct{ text, requirement string }{
		{"redisson 客户端", "Redis"},
		{"mssql 2019", "MySQL"},
		{"kafaka 消费", "消息队列"}, // no fuzzy against parent name
	} {
		if check := skills.Check(tc.text, tc.requirement); check.Status != skills.StatusMissing {
			t.Fatalf("%q vs %q must stay missing: %+v", tc.text, tc.requirement, check)
		}
	}
}

func TestChineseTextMatchesWithoutSpaces(t *testing.T) {
	if check := skills.Check("负责消息队列治理", "消息队列"); check.Status != skills.StatusCovered {
		t.Fatalf("check = %+v", check)
	}
	if check := skills.Check("负责智能体开发", "Agent"); check.Status != skills.StatusCovered || check.Source != skills.SourceAlias {
		t.Fatalf("check = %+v", check)
	}
}

func TestVersionSuffixOnRequirementSide(t *testing.T) {
	check := skills.Check("Spring Boot", "Spring Boot 3")
	if check.Status != skills.StatusCovered || check.Matched != "Spring Boot" {
		t.Fatalf("check = %+v", check)
	}
}
