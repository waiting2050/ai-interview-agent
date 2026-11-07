package eval

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WriteReport persists one run as indented JSON. The file name carries the
// provider, model, prompt version and UTC timestamp so results sort and
// compare without opening them.
func WriteReport(r Report, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create result dir %s: %w", dir, err)
	}
	name := fmt.Sprintf("eval-%s-%s-%s-%s.json",
		slug(r.Provider), slug(r.Model), slug(r.PromptVersion),
		r.GeneratedAt.Format("20060102T150405Z"))
	path := filepath.Join(dir, name)

	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode result: %w", err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return path, nil
}

// ReadReport loads one previously written result file. It is used by the A/B
// diff command and the read-only /eval panel, which never re-run scoring.
func ReadReport(path string) (Report, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Report{}, fmt.Errorf("read result %s: %w", path, err)
	}
	var r Report
	if err := json.Unmarshal(raw, &r); err != nil {
		return Report{}, fmt.Errorf("decode result %s: %w", path, err)
	}
	return r, nil
}

func slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "unknown"
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '.':
			return r
		default:
			return '-'
		}
	}, s)
}
