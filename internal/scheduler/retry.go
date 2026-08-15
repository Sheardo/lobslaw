package scheduler

import (
	"errors"
	"fmt"
	"time"
)

// RetryAfter is returned by a commitment handler that has not
// finished and wants to be called again later.
//
// It exists because a polling handler has a third outcome the
// original two did not cover. "Done" and "failed" are both terminal;
// a job that is still generating is neither, and closing its
// commitment on either of them loses work that is already running and
// already being billed.
//
// The scheduler treats it as: leave the commitment pending, move
// DueAt to At, release the claim so any node can pick it up next
// time. Deliberately NOT an error condition — it is logged at debug,
// because a video taking four minutes is not a problem.
//
// The retry POLICY belongs to the handler rather than here. The
// scheduler does not know how long a job may run, how many transient
// poll failures are tolerable, or what the vendor's cadence is; the
// handler does, and it stops asking by returning nil or a real error.
type RetryAfter struct {
	At     time.Time
	Reason string
}

func (r *RetryAfter) Error() string {
	return fmt.Sprintf("retry after %s: %s", r.At.Format(time.RFC3339), r.Reason)
}

// RetryAfterIn is the common case: come back in d.
func RetryAfterIn(d time.Duration, reason string) *RetryAfter {
	return &RetryAfter{At: time.Now().Add(d), Reason: reason}
}

// AsRetryAfter reports whether err is a retry request, and what it
// asked for. Uses errors.As so a handler may wrap it.
func AsRetryAfter(err error) (*RetryAfter, bool) {
	var r *RetryAfter
	if errors.As(err, &r) {
		return r, true
	}
	return nil, false
}
