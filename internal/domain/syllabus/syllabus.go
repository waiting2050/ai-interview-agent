// Package syllabus keeps the original outline API as a thin view over the
// embedded go-backend domain pack. New code should use domainpack directly;
// this shim exists so existing callers and tests keep their behaviour.
package syllabus

import (
	"strings"

	"github.com/waiting2050/ai-interview-agent/internal/domain/domainpack"
)

const (
	DirectionGo    = "go"
	DirectionMySQL = "mysql"
	DirectionRedis = "redis"
)

type Item struct {
	ID             string
	Topic          string
	Objective      string
	Difficulty     string
	ExpectedPoints []string
}

type Outline struct {
	Direction string
	Items     []Item
}

func pack() *domainpack.Pack {
	return domainpack.Default().MustGet(domainpack.LegacyGoBackendID)
}

// Directions returns the direction ids in manifest order.
func Directions() []string {
	p := pack()
	out := make([]string, 0, len(p.Directions))
	for _, d := range p.Directions {
		out = append(out, d.ID)
	}
	return out
}

// Builtin returns a copy of one built-in outline.
func Builtin(direction string) (Outline, bool) {
	direction = strings.ToLower(strings.TrimSpace(direction))
	p := pack()
	known := false
	for _, d := range p.Directions {
		if d.ID == direction {
			known = true
			break
		}
	}
	if !known {
		return Outline{}, false
	}
	items := make([]Item, 0, len(p.Topics))
	for _, t := range p.Topics {
		if t.Direction != direction {
			continue
		}
		items = append(items, Item{
			ID:             t.ID,
			Topic:          t.Topic,
			Objective:      t.Objective,
			Difficulty:     t.Difficulty,
			ExpectedPoints: append([]string(nil), t.ExpectedPoints...),
		})
	}
	return Outline{Direction: direction, Items: items}, true
}

// Detect returns every direction whose keywords appear in text.
func Detect(text string) []string {
	return pack().Detect(text)
}

// Plan merges matched directions into one ordered outline.
func Plan(text, difficulty string, limit int) Outline {
	p := pack()
	matched := p.Detect(text)
	if len(matched) == 0 {
		matched = []string{DirectionGo}
	}
	items := p.Plan(text, difficulty, limit)
	out := Outline{Direction: strings.Join(matched, "+"), Items: make([]Item, 0, len(items))}
	for _, it := range items {
		out.Items = append(out.Items, Item{
			ID:             it.ID,
			Topic:          it.Topic,
			Objective:      it.Objective,
			Difficulty:     it.Difficulty,
			ExpectedPoints: append([]string(nil), it.ExpectedPoints...),
		})
	}
	return out
}
