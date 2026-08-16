package node

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jmylchreest/lobslaw/internal/memory"
	lobslawv1 "github.com/jmylchreest/lobslaw/pkg/proto/lobslaw/v1"
)

// Approving on a RUNNING cluster. The offline CLI needs bbolt's
// exclusive lock and therefore a stopped node, which is fine for
// forensics and fatal for approval: a workflow that begins "stop the
// cluster" is one nobody performs, after which propose mode is a queue
// that only fills.

func codeOf(err error) codes.Code { return status.Code(err) }

// --- validation, which runs before the store is consulted ----------

// An approval nobody is named on is one nobody can be asked about, and
// "the cluster approved it" is not an answer.
func TestApprovalMustBeAttributed(t *testing.T) {
	t.Parallel()
	_, _, err := validateApprove(&lobslawv1.ApproveArtefactRequest{Id: "skill:tidy"})
	if codeOf(err) != codes.InvalidArgument {
		t.Fatalf("code = %v (err = %v)", codeOf(err), err)
	}
	if !strings.Contains(err.Error(), "approved_by") {
		t.Errorf("err = %q; it does not name the missing field", err)
	}
}

// Whitespace is not attribution.
func TestABlankApproverIsRefused(t *testing.T) {
	t.Parallel()
	_, _, err := validateApprove(&lobslawv1.ApproveArtefactRequest{
		Id: "skill:tidy", ApprovedBy: "   ",
	})
	if codeOf(err) != codes.InvalidArgument {
		t.Errorf("code = %v", codeOf(err))
	}
}

// A rejection changes nothing about what the agent follows — the live
// artefact carries on exactly as it was, and the thing discarded was
// never in force. So it needs no attribution, and demanding one would
// be ceremony that discourages the safe answer.
func TestARejectionNeedsNoAttribution(t *testing.T) {
	t.Parallel()
	id, by, err := validateDecide(&lobslawv1.DecideRevisionRequest{
		Id: "skill:tidy", Accept: false,
	})
	if err != nil {
		t.Fatalf("a rejection was refused for lack of a name: %v", err)
	}
	if id != "skill:tidy" || by != "" {
		t.Errorf("id = %q, by = %q", id, by)
	}
}

// An acceptance does, because it changes what the agent follows.
func TestAnAcceptanceNeedsAttribution(t *testing.T) {
	t.Parallel()
	_, _, err := validateDecide(&lobslawv1.DecideRevisionRequest{
		Id: "skill:tidy", Accept: true,
	})
	if codeOf(err) != codes.InvalidArgument {
		t.Fatalf("code = %v (err = %v)", codeOf(err), err)
	}
	if !strings.Contains(err.Error(), "decided_by") {
		t.Errorf("err = %q", err)
	}
}

// The archive listing exists to say why each thing is in it. A blank
// reason turns that into a list of things that stopped mattering for
// reasons nobody wrote down.
func TestArchivingRequiresAReason(t *testing.T) {
	t.Parallel()
	_, _, err := validateArchive(&lobslawv1.ArchiveArtefactRequest{Id: "skill:tidy"})
	if codeOf(err) != codes.InvalidArgument {
		t.Fatalf("code = %v (err = %v)", codeOf(err), err)
	}
	if !strings.Contains(err.Error(), "why each artefact is in it") {
		t.Errorf("err = %q; it does not say why a reason is wanted", err)
	}
}

func TestAMissingIDIsAnArgumentError(t *testing.T) {
	t.Parallel()
	checks := map[string]error{
		"approve": func() error { _, _, e := validateApprove(&lobslawv1.ApproveArtefactRequest{}); return e }(),
		"decide":  func() error { _, _, e := validateDecide(&lobslawv1.DecideRevisionRequest{}); return e }(),
		"archive": func() error { _, _, e := validateArchive(&lobslawv1.ArchiveArtefactRequest{}); return e }(),
	}
	for name, err := range checks {
		if codeOf(err) != codes.InvalidArgument {
			t.Errorf("%s: code = %v (err = %v)", name, codeOf(err), err)
		}
	}
}

// --- the store guard -----------------------------------------------

// With self-learning off there is no store, and every method has to
// say so in a way that names the setting — an operator who disabled it
// and then wonders why approval fails should not be left concluding
// the build is missing the feature.
func TestEveryMethodRefusesWhenSelfLearningIsOff(t *testing.T) {
	t.Parallel()
	svc := &selfLearningService{}
	ctx := context.Background()

	calls := map[string]func() error{
		"list": func() error {
			_, err := svc.ListArtefacts(ctx, &lobslawv1.ListArtefactsRequest{})
			return err
		},
		"approve": func() error {
			_, err := svc.ApproveArtefact(ctx, &lobslawv1.ApproveArtefactRequest{
				Id: "skill:tidy", ApprovedBy: "user:john"})
			return err
		},
		"decide": func() error {
			_, err := svc.DecideRevision(ctx, &lobslawv1.DecideRevisionRequest{
				Id: "skill:tidy", Accept: true, DecidedBy: "user:john"})
			return err
		},
		"archive": func() error {
			_, err := svc.ArchiveArtefact(ctx, &lobslawv1.ArchiveArtefactRequest{
				Id: "skill:tidy", Reason: "no longer wanted"})
			return err
		},
		"restore": func() error {
			_, err := svc.RestoreArtefact(ctx, &lobslawv1.RestoreArtefactRequest{Id: "skill:tidy"})
			return err
		},
	}
	for name, call := range calls {
		err := call()
		if codeOf(err) != codes.FailedPrecondition {
			t.Errorf("%s: code = %v, want FailedPrecondition (err = %v)", name, codeOf(err), err)
		}
		if err == nil || !strings.Contains(err.Error(), "self_learning.mode") {
			t.Errorf("%s: err = %v; it does not name the setting", name, err)
		}
	}
}

// A malformed request is malformed whether or not the store exists,
// and answering "self-learning is off" to a request that names no
// artefact sends the operator to the wrong problem.
func TestABadRequestIsReportedBeforeTheStoreGuard(t *testing.T) {
	t.Parallel()
	svc := &selfLearningService{}
	_, err := svc.ApproveArtefact(context.Background(),
		&lobslawv1.ApproveArtefactRequest{Id: "skill:tidy"})
	if codeOf(err) != codes.InvalidArgument {
		t.Errorf("code = %v (err = %v); the store guard answered a malformed request",
			codeOf(err), err)
	}
}

// --- error mapping -------------------------------------------------

// The difference between "no such artefact" and "that artefact is not
// awaiting approval" is the difference between a typo and a
// misunderstanding, and a CLI can only say which if the code carries
// it.
func TestStoreErrorsMapToDistinctCodes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   error
		want codes.Code
	}{
		{fmt.Errorf("wrapped: %w", memory.ErrArtefactNotFound), codes.NotFound},
		{fmt.Errorf("wrapped: %w", memory.ErrNotProposed), codes.FailedPrecondition},
		{fmt.Errorf("wrapped: %w", memory.ErrNoPendingRevision), codes.FailedPrecondition},
		{errors.New("the disk caught fire"), codes.Internal},
	}
	for _, c := range cases {
		if got := codeOf(artefactError(c.in)); got != c.want {
			t.Errorf("%v mapped to %v, want %v", c.in, got, c.want)
		}
	}
}
