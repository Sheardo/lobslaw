// Package compute_test rather than compute: drivertest imports
// compute, so an in-package test importing drivertest is a cycle. The
// external test package is the standard way out and costs nothing —
// the suite only exercises exported API anyway.
package compute_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jmylchreest/lobslaw/internal/compute"
	"github.com/jmylchreest/lobslaw/internal/compute/drivertest"
)

// The existing OpenAI-compatible client is the first driver, so it has
// to pass the same suite the second one does. Running both against one
// contract is the only thing that shows the contract is not simply a
// description of whichever came first.
//
// This also covers the failure classification added when the client
// moved onto the waist: the sentinels say what went wrong, the class
// says what to do about it, and only the second drives failover.
func TestOpenAIClientConformance(t *testing.T) {
	t.Parallel()

	const ok = `{
	  "choices":[{"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],
	  "usage":{"prompt_tokens":9,"completion_tokens":2,"total_tokens":11}
	}`

	serve := func(status int, body string) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}))
		t.Cleanup(srv.Close)
		return srv
	}

	newClient := func(endpoint string) *compute.LLMClient {
		c, err := compute.NewLLMClient(compute.LLMClientConfig{
			Endpoint: endpoint,
			Model:    "test-model",
			APIKey:   "test-key",
		})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}

	drivertest.Run(t, drivertest.Subject{
		Name: "openai",
		Chat: newClient(serve(http.StatusOK, ok).URL),
		FailingChat: func(status int, body string) compute.ChatDriver {
			return newClient(serve(status, body).URL)
		},
	})
}
