package trace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// R24's design said "exported, not stored — no raft bucket and no
// reporting command". That was right about RAFT: a trace is
// high-volume, short-lived and not agreed-upon state. A per-node file
// is not raft, and it gives `lobslaw trace <id>` without any of it.
//
// The honest cost is that a turn served on node A is not queryable
// from node B. The trace is local because the turn was.

func sink(t *testing.T, maxBytes int64) (*FileSink, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := NewFileSink(dir, maxBytes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, dir
}

func aSpan(turnID string, attempt int) Span {
	return Span{
		TurnID:    turnID,
		SpanID:    "span-" + turnID + "-" + string(rune('a'+attempt)),
		Kind:      KindLLMCall,
		Provider:  "openrouter",
		Name:      "some-model",
		StartedAt: time.Unix(1700000000, 0).UTC(),
		Duration:  120 * time.Millisecond,
		Outcome:   OutcomeOK,
		Attempt:   attempt,
	}
}

func TestSpansRoundTripThroughTheFile(t *testing.T) {
	t.Parallel()
	s, dir := sink(t, 0)
	want := aSpan("turn-1", 0)
	want.Usage = Usage{PromptTokens: 1200, CompletionTokens: 80, CachedTokens: 900}
	want.CostUSD = 0.0031
	if err := s.Write(want); err != nil {
		t.Fatal(err)
	}

	got, err := ReadTurn(dir, "turn-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("read %d spans", len(got))
	}
	if got[0].Provider != "openrouter" || got[0].Duration != 120*time.Millisecond {
		t.Errorf("span = %+v", got[0])
	}
	if got[0].Usage.CachedTokens != 900 {
		t.Errorf("cached tokens lost: %+v", got[0].Usage)
	}
	if got[0].CostUSD != 0.0031 {
		t.Errorf("cost = %v", got[0].CostUSD)
	}
}

func TestReadTurnFiltersToOneTurn(t *testing.T) {
	t.Parallel()
	s, dir := sink(t, 0)
	for _, id := range []string{"turn-1", "turn-2", "turn-1"} {
		if err := s.Write(aSpan(id, 0)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := ReadTurn(dir, "turn-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("read %d spans for turn-1", len(got))
	}
}

// A node that has never traced has no file, and a rotation that has
// not happened has no predecessor. Both are the normal state, not a
// problem to report.
func TestReadingAnEmptyTraceDirIsNotAnError(t *testing.T) {
	t.Parallel()
	got, err := ReadTurn(t.TempDir(), "turn-1")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d spans", len(got))
	}
}

// --- rotation ------------------------------------------------------

// An unbounded telemetry file on a long-running node is a disk-full
// incident waiting for a quiet week.
func TestTheFileRotatesAtTheBound(t *testing.T) {
	t.Parallel()
	s, dir := sink(t, 400)
	for i := range 20 {
		if err := s.Write(aSpan("turn-1", i%26)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, rotatedName)); err != nil {
		t.Fatalf("nothing rotated: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > 400 {
		t.Errorf("current file is %d bytes, past the 400 bound", info.Size())
	}
}

// A turn that straddles a rotation must come back whole. A trace
// missing its opening spans reads as a turn that started at its third
// model call.
func TestATurnStraddlingARotationComesBackWhole(t *testing.T) {
	t.Parallel()
	s, dir := sink(t, 400)
	const spans = 20
	for i := range spans {
		if err := s.Write(aSpan("turn-1", i%26)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := ReadTurn(dir, "turn-1")
	if err != nil {
		t.Fatal(err)
	}
	// Exactly one rotation is kept, so spans older than the
	// predecessor are legitimately gone. What must hold is that the
	// read spans BOTH files: the current file alone cannot hold this
	// many at a 400-byte bound.
	currentOnly, err := readFileSpans(filepath.Join(dir, FileName), "turn-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) <= len(currentOnly) {
		t.Fatalf("read %d spans, current file alone has %d — the rotated file was not read",
			len(got), len(currentOnly))
	}
}

// Two files is a ceiling somebody can reason about. An ever-growing
// numbered series is the same disk-full problem with extra steps.
func TestOnlyOnePredecessorIsKept(t *testing.T) {
	t.Parallel()
	_, dir := sink(t, 300)
	s, err := NewFileSink(dir, 300)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	for i := range 60 {
		if err := s.Write(aSpan("turn-1", i%26)); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) > 2 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("trace dir holds %d files: %s", len(entries), strings.Join(names, ", "))
	}
}

// --- robustness ----------------------------------------------------

// A trace file is append-only from a process that may have been killed
// mid-write, so a truncated final line is expected rather than
// exceptional. One bad line must not hide the rest.
func TestATruncatedLineDoesNotHideTheRest(t *testing.T) {
	t.Parallel()
	s, dir := sink(t, 0)
	if err := s.Write(aSpan("turn-1", 0)); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, FileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// A half-written record, as a kill mid-write would leave.
	if err := os.WriteFile(path, append(raw, []byte(`{"turn_id":"turn-1","ki`)...), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := ReadTurn(dir, "turn-1")
	if err != nil {
		t.Fatalf("a truncated line failed the read: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("read %d spans; the intact one should survive", len(got))
	}
}

// --- listing -------------------------------------------------------

func TestListTurnsIsNewestFirst(t *testing.T) {
	t.Parallel()
	s, dir := sink(t, 0)
	for _, id := range []string{"turn-1", "turn-2", "turn-3"} {
		if err := s.Write(aSpan(id, 0)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := ListTurns(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0] != "turn-3" {
		t.Errorf("ListTurns = %v; want newest first", got)
	}
}

func TestListTurnsHonoursTheLimit(t *testing.T) {
	t.Parallel()
	s, dir := sink(t, 0)
	for _, id := range []string{"a", "b", "c", "d"} {
		if err := s.Write(aSpan(id, 0)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := ListTurns(dir, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("got %d, want 2", len(got))
	}
}

// A sink that cannot write should fail at wiring time, when an
// operator is looking at a boot error, rather than silently drop every
// span.
func TestAnUnwritableDirFailsAtConstruction(t *testing.T) {
	t.Parallel()
	if _, err := NewFileSink("", 0); err == nil {
		t.Error("an empty dir built a sink")
	}
	blocked := filepath.Join(t.TempDir(), "notadir")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileSink(filepath.Join(blocked, "traces"), 0); err == nil {
		t.Error("a dir under a regular file built a sink")
	}
}

// NO SPAN CARRIES CONTENT. The serialised form is what leaves the
// process, so this asserts against the bytes rather than the struct.
func TestTheSerialisedSpanHasNowhereToPutContent(t *testing.T) {
	t.Parallel()
	s, dir := sink(t, 0)
	span := aSpan("turn-1", 0)
	span.Error = "429 rate limited"
	if err := s.Write(span); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	// The field set is the guarantee: if somebody adds a content field
	// to Span this fails, which is the point.
	for _, forbidden := range []string{
		"\"messages\"", "\"content\"", "\"prompt\"", "\"arguments\"",
		"\"output\"", "\"reply\"", "\"text\"", "\"body\"",
	} {
		if strings.Contains(string(raw), forbidden) {
			t.Errorf("the serialised span carries %s:\n%s", forbidden, raw)
		}
	}
}
