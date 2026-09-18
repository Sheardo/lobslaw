package gateway

import (
	"context"
	"net/http"
	"testing"

	lobslawv1 "github.com/jmylchreest/lobslaw/pkg/proto/lobslaw/v1"
)

type stubBots struct {
	rec *lobslawv1.BotRecord
	err error
}

func (s stubBots) List(context.Context) ([]*lobslawv1.BotRecord, error) { return nil, nil }
func (s stubBots) Get(context.Context, string) (*lobslawv1.BotRecord, error) {
	return s.rec, s.err
}
func (s stubBots) Put(context.Context, *lobslawv1.BotRecord, uint64) (*lobslawv1.BotRecord, error) {
	return s.rec, s.err
}
func (s stubBots) Delete(context.Context, string) error { return s.err }

type stubGroups struct {
	rec *lobslawv1.GroupRecord
	err error
}

func (s stubGroups) List(context.Context) ([]*lobslawv1.GroupRecord, error) { return nil, nil }
func (s stubGroups) Get(context.Context, string) (*lobslawv1.GroupRecord, error) {
	return s.rec, s.err
}
func (s stubGroups) Put(context.Context, *lobslawv1.GroupRecord, uint64) (*lobslawv1.GroupRecord, error) {
	return s.rec, s.err
}
func (s stubGroups) Delete(context.Context, string) error { return s.err }

func TestMayModifyBotFailsClosedOnGroupLookupMiss(t *testing.T) {
	t.Parallel()
	srv := &Server{cfg: RESTConfig{
		Bots: stubBots{rec: &lobslawv1.BotRecord{Id: "eng", GroupId: "missing"}},
		Groups: stubGroups{err: errGroupMissing},
	}}
	req, _ := http.NewRequest(http.MethodDelete, "/v1/bots/eng", nil)
	if srv.mayModifyBot(req, "eng") {
		t.Fatal("group lookup miss must fail closed")
	}
}

func TestMayUseGroupFailsClosedOnMissingDestination(t *testing.T) {
	t.Parallel()
	srv := &Server{cfg: RESTConfig{
		Groups: stubGroups{err: errGroupMissing},
	}}
	req, _ := http.NewRequest(http.MethodPost, "/v1/bots", nil)
	if srv.mayUseGroup(req, "someone-elses-team") {
		t.Fatal("POST into an unreadable team must fail closed")
	}
	if srv.mayUseGroup(req, "") {
		t.Fatal("empty destination must not fall through to a shared default")
	}
}

type missingGroupError struct{}

func (missingGroupError) Error() string { return "groups: not found: missing" }

var errGroupMissing = missingGroupError{}
