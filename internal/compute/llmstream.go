package compute

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Streaming is an observability side-channel, not a second code path.
//
// A turn against a real model runs thirty to a hundred and sixty
// seconds and the console showed an animation for all of it. The
// obvious implementation — a streaming variant of the whole tool-call
// loop — would have meant a second copy of failover, budget
// accounting, tool reassembly and error classification, which is
// exactly the duplication this codebase keeps refusing elsewhere.
//
// So: when a caller wants deltas, the client reads the response
// incrementally and hands out text as it arrives, then assembles the
// SAME *ChatResponse it always returned. Everything upstream — the
// loop, the failover chain, the budget, the receipt — is unchanged and
// cannot tell the difference. The only thing streaming adds is that
// somebody gets to watch.

// streamChat performs a streaming completion, emitting content deltas
// through onDelta as they arrive, and returns the assembled response.
func (c *LLMClient) streamChat(ctx context.Context, req ChatRequest, model string, onDelta func(string)) (*ChatResponse, error) {
	wire := toOpenAIRequest(req, c.model)
	wire.Stream = true
	// Without this a streamed response carries no usage block at all,
	// and every streamed turn would silently cost zero — which is
	// worse than not streaming, because the budget would stop counting
	// while continuing to look like it was.
	wire.StreamOptions = &openAIStreamOptions{IncludeUsage: true}

	body, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("llm: marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("llm: build http request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	start := time.Now()
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("llm: http do: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		c.log.Warn("llm: stream response error",
			"status", resp.StatusCode, "endpoint", c.endpoint,
			"model", model, "duration", time.Since(start),
			"body", truncateBody(raw))
		// Same classifier as the non-streaming path, so a 429 on a
		// streamed call still carries its failover class and the
		// provider's Retry-After.
		return nil, classifyHTTPResponse(resp, raw, time.Now())
	}

	out, usage, err := readChatStream(resp.Body, onDelta)
	if err != nil {
		return nil, err
	}
	c.log.Debug("llm: stream complete",
		"endpoint", c.endpoint, "model", model,
		"duration", time.Since(start),
		"content_len", len(out.Content),
		"tool_calls", len(out.ToolCalls),
		"total_tokens", usage.TotalTokens)
	out.Usage = Usage{
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		TotalTokens:      usage.TotalTokens,
		CachedTokens:     usage.cachedTokens(),
	}
	return out, nil
}

// readChatStream consumes an OpenAI-style SSE body.
func readChatStream(body io.Reader, onDelta func(string)) (*ChatResponse, openAIUsage, error) {
	var (
		content strings.Builder
		usage   openAIUsage
		finish  string
		// Tool calls arrive in fragments keyed by index, with the name
		// usually in the first fragment and the arguments dribbled
		// across the rest. Keyed by index rather than appended,
		// because the provider is free to interleave two calls.
		calls = map[int]*ToolCall{}
	)

	scanner := bufio.NewScanner(body)
	// A single SSE line can carry a large argument fragment; the
	// default 64KB ceiling would truncate it mid-JSON and produce a
	// tool call that fails to parse for no visible reason.
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}

		var chunk openAIStreamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			// One malformed frame must not discard a completion that
			// is otherwise fine — providers emit keepalives and
			// vendor-specific frames that are none of our business.
			continue
		}
		if chunk.Usage != nil {
			usage = *chunk.Usage
		}
		for _, ch := range chunk.Choices {
			if ch.FinishReason != "" {
				finish = ch.FinishReason
			}
			if text := ch.Delta.Content; text != "" {
				content.WriteString(text)
				if onDelta != nil {
					onDelta(text)
				}
			}
			for _, tc := range ch.Delta.ToolCalls {
				got, ok := calls[tc.Index]
				if !ok {
					got = &ToolCall{}
					calls[tc.Index] = got
				}
				if tc.ID != "" {
					got.ID = tc.ID
				}
				if tc.Function.Name != "" {
					got.Name = tc.Function.Name
				}
				got.Arguments += tc.Function.Arguments
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, usage, fmt.Errorf("llm: read stream: %w", err)
	}

	// Back into index order: the map lost it, and argument fragments
	// were assembled per call so the order of the calls themselves is
	// the only thing left to restore.
	idxs := make([]int, 0, len(calls))
	for i := range calls {
		idxs = append(idxs, i)
	}
	sort.Ints(idxs)
	out := &ChatResponse{Content: content.String(), FinishReason: finish}
	for _, i := range idxs {
		out.ToolCalls = append(out.ToolCalls, *calls[i])
	}
	return out, usage, nil
}

type openAIStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type openAIStreamChunk struct {
	Choices []openAIStreamChoice `json:"choices"`
	Usage   *openAIUsage         `json:"usage,omitempty"`
}

type openAIStreamChoice struct {
	Delta        openAIStreamDelta `json:"delta"`
	FinishReason string            `json:"finish_reason"`
}

type openAIStreamDelta struct {
	Content   string                 `json:"content"`
	ToolCalls []openAIStreamToolCall `json:"tool_calls"`
}

type openAIStreamToolCall struct {
	Index    int                `json:"index"`
	ID       string             `json:"id"`
	Function openAIToolCallFunc `json:"function"`
}
