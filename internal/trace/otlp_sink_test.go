package trace

import (
	"context"
	"encoding/hex"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	collectorpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// Exercised against a real in-process collector rather than a mock
// client, because the interesting failures — a collector that hangs, a
// collector that refuses — are transport behaviour, and a stubbed
// client would assert the conversion and none of the properties that
// actually matter.

type fakeCollector struct {
	collectorpb.UnimplementedTraceServiceServer

	mu       sync.Mutex
	received []*tracepb.Span
	// got closes once want spans have arrived. Closed via sync.Once
	// rather than nilled: a test selecting on the field would re-read
	// it and block forever on a nil channel.
	got     chan struct{}
	gotOnce sync.Once
	want    int

	// hang blocks Export until released, standing in for a collector
	// that accepts a connection and then stops reading.
	hang chan struct{}
	// fail makes Export return an error.
	fail bool
}

func (f *fakeCollector) Export(_ context.Context, req *collectorpb.ExportTraceServiceRequest) (*collectorpb.ExportTraceServiceResponse, error) {
	if f.hang != nil {
		<-f.hang
	}
	if f.fail {
		return nil, errors.New("collector is unwell")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, rs := range req.GetResourceSpans() {
		for _, ss := range rs.GetScopeSpans() {
			f.received = append(f.received, ss.GetSpans()...)
		}
	}
	if f.got != nil && f.want > 0 && len(f.received) >= f.want {
		f.gotOnce.Do(func() { close(f.got) })
	}
	return &collectorpb.ExportTraceServiceResponse{}, nil
}

func (f *fakeCollector) spans() []*tracepb.Span {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*tracepb.Span(nil), f.received...)
}

// collector starts an in-process OTLP server and returns a sink wired
// to it.
func collector(t *testing.T, srv *fakeCollector, cfg OTLPConfig) *OTLPSink {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	s := grpc.NewServer()
	collectorpb.RegisterTraceServiceServer(s, srv)
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	cfg.DialOverride = conn
	sink, err := NewOTLPSink(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sink.Close() })
	return sink
}

func TestSpansReachTheCollector(t *testing.T) {
	t.Parallel()
	srv := &fakeCollector{got: make(chan struct{}), want: 1}
	sink := collector(t, srv, OTLPConfig{BatchSize: 1, ServiceName: "lobslaw", NodeID: "node-a"})

	span := aSpan("turn-1", 0)
	span.Usage = Usage{PromptTokens: 1200, CompletionTokens: 80, CachedTokens: 900}
	span.CostUSD = 0.0031
	if err := sink.Write(span); err != nil {
		t.Fatal(err)
	}
	select {
	case <-srv.got:
	case <-time.After(5 * time.Second):
		t.Fatal("nothing reached the collector")
	}

	got := srv.spans()[0]
	if got.GetName() != "llm_call openrouter" {
		t.Errorf("name = %q; a screen of spans all called llm_call has no information on it", got.GetName())
	}
	attrs := attrMap(got.GetAttributes())
	if attrs["lobslaw.tokens.cached"] != int64(900) {
		t.Errorf("cached tokens = %v", attrs["lobslaw.tokens.cached"])
	}
	if attrs["lobslaw.cost_usd"] != 0.0031 {
		t.Errorf("cost = %v", attrs["lobslaw.cost_usd"])
	}
	if attrs["lobslaw.provider"] != "openrouter" {
		t.Errorf("provider = %v", attrs["lobslaw.provider"])
	}
}

// A turn's spans must group into one trace, and on every node and
// every re-export. Hashing rather than generating is what gives that.
func TestOneTurnIsOneTraceEverywhere(t *testing.T) {
	t.Parallel()
	a := traceID("turn-1")
	b := traceID("turn-1")
	if hex.EncodeToString(a) != hex.EncodeToString(b) {
		t.Fatal("the same turn produced two trace ids")
	}
	if hex.EncodeToString(traceID("turn-2")) == hex.EncodeToString(a) {
		t.Error("two turns collided into one trace")
	}
	if len(a) != 16 {
		t.Errorf("trace id is %d bytes, OTLP wants 16", len(a))
	}
	if len(spanID("s1")) != 8 {
		t.Errorf("span id is %d bytes, OTLP wants 8", len(spanID("s1")))
	}
}

// A turn id and a span id that happened to be equal must not hash to
// the same bytes — that would make a span its own parent, and shows up
// as one inexplicable trace six months later.
func TestATurnIDAndSpanIDCannotCollide(t *testing.T) {
	t.Parallel()
	same := "identical"
	if hex.EncodeToString(traceID(same)[:8]) == hex.EncodeToString(spanID(same)) {
		t.Error("a turn id and a span id with the same text produced the same bytes")
	}
}

// --- the failure modes that matter ---------------------------------

// A collector that accepts a connection and then stops reading is the
// failure the export deadline exists for. Without one the exporter
// blocks forever, the recorder's queue fills, and every span is
// dropped for the life of the process.
func TestAHangingCollectorDoesNotBlockTheRecorder(t *testing.T) {
	t.Parallel()
	srv := &fakeCollector{hang: make(chan struct{})}
	sink := collector(t, srv, OTLPConfig{BatchSize: 1})
	r := NewRecorder(quiet(), sink)

	// Released and drained INSIDE the test rather than via t.Cleanup.
	// Cleanups run last-in-first-out, so a deferred Close would run
	// before the hang was released — and Close drains the queue, at
	// one export timeout per span. The test would pass and then take
	// forty minutes to return.
	defer func() {
		close(srv.hang)
		_ = r.Close()
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range DefaultBuffer * 3 {
			r.Record(aSpan("turn-1", 0))
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a hanging collector blocked Record")
	}
}

// The deadline itself. A collector that accepts a connection and then
// stops reading would otherwise block the recorder's single background
// goroutine forever — after which every span is dropped for the life
// of the process, INCLUDING the ones destined for the local file,
// which has nothing wrong with it.
func TestAHangingExportGivesUpAtTheDeadline(t *testing.T) {
	t.Parallel()
	srv := &fakeCollector{hang: make(chan struct{})}
	sink := collector(t, srv, OTLPConfig{BatchSize: 1, Timeout: 100 * time.Millisecond})
	defer close(srv.hang)

	start := time.Now()
	err := sink.Write(aSpan("turn-1", 0))
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a hanging collector reported a successful export")
	}
	if elapsed > 3*time.Second {
		t.Errorf("the export took %s to give up on a 100ms deadline", elapsed)
	}
}

// And the recorder recovers: once the collector answers again, spans
// flow. A deadline that gave up permanently would be no better than
// hanging.
func TestTheSinkRecoversAfterAHang(t *testing.T) {
	t.Parallel()
	srv := &fakeCollector{hang: make(chan struct{}), got: make(chan struct{}), want: 1}
	sink := collector(t, srv, OTLPConfig{BatchSize: 1, Timeout: 100 * time.Millisecond})

	if err := sink.Write(aSpan("turn-1", 0)); err == nil {
		t.Fatal("the hanging export reported success")
	}
	close(srv.hang)
	srv.hang = nil

	if err := sink.Write(aSpan("turn-1", 1)); err != nil {
		t.Fatalf("the sink did not recover: %v", err)
	}
	select {
	case <-srv.got:
	case <-time.After(5 * time.Second):
		t.Fatal("nothing reached the collector after it recovered")
	}
}

// A collector that is down stays down for minutes. A queue that grows
// for the duration is how a telemetry outage becomes a memory
// incident, so a refused batch is dropped rather than requeued.
func TestARefusedBatchIsDroppedNotRequeued(t *testing.T) {
	t.Parallel()
	srv := &fakeCollector{fail: true}
	sink := collector(t, srv, OTLPConfig{BatchSize: 2})

	if err := sink.Write(aSpan("turn-1", 0)); err != nil {
		t.Fatalf("buffering errored: %v", err)
	}
	err := sink.Write(aSpan("turn-1", 1))
	if err == nil {
		t.Fatal("a refused export reported success")
	}

	// The pending buffer is empty: the failed batch went, it did not
	// come back.
	sink.mu.Lock()
	pending := len(sink.pending)
	sink.mu.Unlock()
	if pending != 0 {
		t.Errorf("%d spans were requeued after a refusal", pending)
	}
}

// The recorder counts the failure, which is what makes dropping
// honest.
func TestTheRecorderCountsAFailedExport(t *testing.T) {
	t.Parallel()
	srv := &fakeCollector{fail: true}
	sink := collector(t, srv, OTLPConfig{BatchSize: 1})
	r := NewRecorder(quiet(), sink)

	r.Record(aSpan("turn-1", 0))
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if r.Stats().Failed == 0 {
		t.Error("a refused export was not counted")
	}
}

// --- status ---------------------------------------------------------

// A failed attempt is ERROR so a collector's own filters find it. A
// SKIPPED candidate is not: it did not fail, it was never tried, and
// colouring a protective decision red is how a working trust floor
// gets reported as an outage.
func TestOnlyRealFailuresAreMarkedError(t *testing.T) {
	t.Parallel()
	cases := map[Outcome]tracepb.Status_StatusCode{
		OutcomeOK:       tracepb.Status_STATUS_CODE_OK,
		OutcomeAdvanced: tracepb.Status_STATUS_CODE_ERROR,
		OutcomeAborted:  tracepb.Status_STATUS_CODE_ERROR,
		OutcomeSkipped:  tracepb.Status_STATUS_CODE_UNSET,
	}
	for outcome, want := range cases {
		s := aSpan("turn-1", 0)
		s.Outcome = outcome
		if got := toOTLP(s).GetStatus().GetCode(); got != want {
			t.Errorf("%s mapped to %v, want %v", outcome, got, want)
		}
	}
}

// A skipped candidate has no duration; end must not precede start.
func TestASpanWithNoDurationIsNotNegative(t *testing.T) {
	t.Parallel()
	s := SkippedSpan("turn-1", "s1", "openrouter", "in cooldown", 2)
	s.StartedAt = time.Unix(1700000000, 0)
	out := toOTLP(s)
	if out.GetEndTimeUnixNano() < out.GetStartTimeUnixNano() {
		t.Errorf("end %d precedes start %d", out.GetEndTimeUnixNano(), out.GetStartTimeUnixNano())
	}
}

// NO CONTENT. The conversion reads named fields off a struct that has
// nowhere to put a prompt, and this is the test that fails if somebody
// adds one.
func TestNoAttributeCarriesContent(t *testing.T) {
	t.Parallel()
	s := aSpan("turn-1", 0)
	s.Error = "429 rate limited"
	for _, kv := range toOTLP(s).GetAttributes() {
		switch kv.GetKey() {
		case "lobslaw.messages", "lobslaw.content", "lobslaw.prompt",
			"lobslaw.arguments", "lobslaw.output", "lobslaw.reply":
			t.Errorf("attribute %q carries content", kv.GetKey())
		}
	}
}

func TestAnEndpointIsRequired(t *testing.T) {
	t.Parallel()
	if _, err := NewOTLPSink(OTLPConfig{}); err == nil {
		t.Error("a sink was built with no endpoint")
	}
}

func attrMap(kvs []*commonpb.KeyValue) map[string]any {
	out := map[string]any{}
	for _, kv := range kvs {
		switch v := kv.GetValue().GetValue().(type) {
		case *commonpb.AnyValue_StringValue:
			out[kv.GetKey()] = v.StringValue
		case *commonpb.AnyValue_IntValue:
			out[kv.GetKey()] = v.IntValue
		case *commonpb.AnyValue_DoubleValue:
			out[kv.GetKey()] = v.DoubleValue
		}
	}
	return out
}
