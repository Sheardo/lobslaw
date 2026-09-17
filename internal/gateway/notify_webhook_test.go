package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A bot pinging Slack is one POST, and it has to be the shape Slack
// actually accepts.
//
// The existing callback sink sends {"body": ...} for machine
// receivers. Slack — and Discord and Teams, which copied it — want
// {"text": ...}, so a bot told to "ping Slack when the deploy lands"
// had no route there short of a full Slack app with a bot token and
// socket mode. This asserts the wire shape, because that is the whole
// difference between the two sinks.
func TestWebhookSinkSendsTheShapeSlackAccepts(t *testing.T) {
	t.Parallel()

	var got webhookPayload
	var contentType string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contentType = r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sink := &WebhookSink{Client: srv.Client()}
	if err := sink.Deliver(context.Background(), srv.URL, "Engineering — deploy finished"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if got.Text != "Engineering — deploy finished" {
		t.Errorf(`payload was {"text": %q}, want the message body`, got.Text)
	}
	if contentType != "application/json" {
		t.Errorf("Content-Type = %q", contentType)
	}
}

// A webhook URL is a bearer credential: whoever holds it can post as
// you. Sending one over plaintext http hands it to every hop.
func TestWebhookSinkRefusesPlaintext(t *testing.T) {
	t.Parallel()

	sink := &WebhookSink{Client: http.DefaultClient}
	err := sink.Deliver(context.Background(), "http://hooks.example.com/T/B/xyz-secret", "hello")
	if err == nil {
		t.Fatal("delivered a webhook secret over plaintext http")
	}
	if !strings.Contains(err.Error(), "https") {
		t.Errorf("error should say why: %v", err)
	}
}

// A failure must not echo the URL back.
//
// The path segment IS the secret, and this string lands in the node
// log and in the tool result the model reads — so a 404 that quoted
// the full URL would copy the credential into both.
func TestWebhookSinkErrorDoesNotLeakTheSecret(t *testing.T) {
	t.Parallel()

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	sink := &WebhookSink{Client: srv.Client()}
	err := sink.Deliver(context.Background(), srv.URL+"/services/T000/B000/sup3rs3cr3t", "hello")
	if err == nil {
		t.Fatal("expected an error on 404")
	}
	if strings.Contains(err.Error(), "sup3rs3cr3t") {
		t.Errorf("the webhook secret is in the error text: %v", err)
	}
}
