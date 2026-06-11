package state

import (
	"testing"
	"time"
)

func TestStoreRoundTrip(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if st, err := s.Read("docs"); err != nil || st != nil {
		t.Fatalf("expected (nil, nil) for unknown site, got (%v, %v)", st, err)
	}

	in := SiteState{
		Slug:       "docs",
		Reason:     "webhook",
		Commit:     "abc123",
		DurationMS: 1234,
		Status:     "success",
		FinishedAt: time.Now().UTC().Truncate(time.Second),
	}
	if err := s.Write(in); err != nil {
		t.Fatal(err)
	}
	out, err := s.Read("docs")
	if err != nil {
		t.Fatal(err)
	}
	if out == nil || *out != in {
		t.Errorf("roundtrip mismatch: %+v != %+v", out, in)
	}
}
