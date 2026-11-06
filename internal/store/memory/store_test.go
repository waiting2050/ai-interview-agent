package memory

import (
	"context"
	"testing"
	"time"

	"github.com/waiting2050/ai-interview-agent/internal/domain/session"
)

func TestSubscribeEventsWakesOnAppend(t *testing.T) {
	s := New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := s.SubscribeEvents(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Repos().Events.Append(context.Background(), &session.Event{SessionID: "s1", Type: "x"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("event append did not wake subscriber")
	}
}
