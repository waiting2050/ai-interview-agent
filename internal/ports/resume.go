package ports

import "github.com/waiting2050/ai-interview-agent/internal/domain/resume"

// ResumeAnalyzer is the rule-based resume diagnosis port. Implementations must
// be deterministic and offline; a model-backed implementation would be a
// separate, explicitly enabled adapter.
type ResumeAnalyzer interface {
	Analyze(opts resume.AnalyzeOptions) (resume.Diagnosis, error)
}
