package node

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jmylchreest/lobslaw/internal/compute"
	"github.com/jmylchreest/lobslaw/internal/gateway"
	"github.com/jmylchreest/lobslaw/internal/memory"
)

// The gateway decides whether to warn or shrug based on this
// translation; if it breaks, every follower-hosted turn logs a scary
// warning for entirely normal behaviour.
func TestTranslateSessionErrMapsNotLeader(t *testing.T) {
	t.Parallel()
	err := translateSessionErr(fmt.Errorf("%w; current leader is 10.0.0.2:7000", memory.ErrNotLeader))
	if !errors.Is(err, gateway.ErrSessionUnavailable) {
		t.Fatalf("got %v, want it to wrap gateway.ErrSessionUnavailable", err)
	}
	// The leader hint has to survive — it's how an operator finds the
	// node to retry against.
	if got := err.Error(); !strings.Contains(got, "10.0.0.2:7000") {
		t.Errorf("leader address lost from %q", got)
	}
}

func TestTranslateSessionErrPassesThroughOtherErrors(t *testing.T) {
	t.Parallel()
	boom := errors.New("bbolt: disk full")
	err := translateSessionErr(boom)
	if errors.Is(err, gateway.ErrSessionUnavailable) {
		t.Error("a real failure was misreported as a leadership issue")
	}
	if !errors.Is(err, boom) {
		t.Errorf("original error lost: %v", err)
	}
}

func TestTranslateSessionErrNilIsNil(t *testing.T) {
	t.Parallel()
	if err := translateSessionErr(nil); err != nil {
		t.Errorf("got %v, want nil", err)
	}
}

func TestToolCallConversionRoundTrips(t *testing.T) {
	t.Parallel()
	in := []compute.ToolCall{
		{ID: "call_1", Name: "shell_command", Arguments: `{"cmd":"ls"}`},
		{ID: "call_2", Name: "read_file", Arguments: `{"path":"/tmp/x"}`},
	}
	got := toComputeToolCalls(toTranscriptToolCalls(in))
	if len(got) != len(in) {
		t.Fatalf("got %d tool calls, want %d", len(got), len(in))
	}
	for i := range in {
		if got[i] != in[i] {
			t.Errorf("tool call %d = %+v, want %+v", i, got[i], in[i])
		}
	}
}

func TestToolCallConversionNilStaysNil(t *testing.T) {
	t.Parallel()
	if got := toTranscriptToolCalls(nil); got != nil {
		t.Errorf("got %+v, want nil", got)
	}
	if got := toComputeToolCalls(nil); got != nil {
		t.Errorf("got %+v, want nil", got)
	}
}
