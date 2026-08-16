package node

import (
	"fmt"
	"path/filepath"

	"github.com/jmylchreest/lobslaw/internal/trace"
)

// Turn tracing.
//
// Off unless asked for, and off means ABSENT: with tracing disabled
// n.traces stays nil, and a nil *trace.Recorder is usable — every
// method tolerates it. So the instrumented code paths call
// rec.Record(...) unconditionally rather than branching on whether
// tracing exists, and a deployment that never enabled it carries no
// runtime check at all.

// traceDirName sits under DataDir alongside the skill cache, not in a
// storage mount. A mount is shared and durable; this is neither. It is
// per-node telemetry that can be deleted at any time, and putting it
// somewhere an operator might back up would invite restoring stale
// traces over a node that had moved on.
const traceDirName = "traces"

// startTracing builds the recorder. Returns nil having built nothing
// when tracing is off.
func (n *Node) startTracing() error {
	if !n.cfg.Trace.Enabled {
		return nil
	}
	dir := n.cfg.Trace.Dir
	if dir == "" {
		if n.cfg.DataDir == "" {
			// Not an error. A node with no data dir has nowhere
			// per-node to put anything, and refusing to boot over
			// telemetry would be the wrong trade. Said out loud,
			// because "I turned tracing on and got nothing" is
			// otherwise indistinguishable from a quiet deployment.
			n.log.Warn("trace: enabled but there is no data dir and no trace.dir; nothing will be recorded")
			return nil
		}
		dir = filepath.Join(n.cfg.DataDir, traceDirName)
	}

	// Constructed eagerly so a directory that cannot be written fails
	// HERE, while an operator is looking at a boot error, rather than
	// silently dropping every span for the life of the process.
	sink, err := trace.NewFileSink(dir, n.cfg.Trace.MaxBytes)
	if err != nil {
		return fmt.Errorf("trace: %w", err)
	}
	n.traces = trace.NewRecorder(n.log, sink)
	n.log.Info("trace: recording turns", "dir", dir,
		"max_bytes", n.cfg.Trace.MaxBytes, "content_recorded", false)
	return nil
}

// stopTracing drains and closes. Safe when tracing was never on.
func (n *Node) stopTracing() {
	if n.traces == nil {
		return
	}
	_ = n.traces.Close()
}
