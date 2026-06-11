package webhook

import (
	"testing"
	"time"
)

func TestReplayCacheWindow(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	c := newReplayCache(10*time.Minute, clock)

	if c.CheckAndRecord("d1") {
		t.Error("first sighting flagged as replay")
	}
	if !c.CheckAndRecord("d1") {
		t.Error("second sighting inside window not flagged")
	}
	now = now.Add(11 * time.Minute)
	if c.CheckAndRecord("d1") {
		t.Error("delivery outside window still flagged; prune failed")
	}
}
