package compute

import (
	"context"
	"strings"
)

// Title defaults.
const (
	// DefaultTitleMaxChars keeps titles scannable in a list. Models
	// reliably overshoot "short", so this is enforced by truncation
	// rather than trusted.
	DefaultTitleMaxChars = 60
	// titleMaxCompletionTokens is generous relative to the length
	// limit — a model that pads its answer still yields a usable
	// first line rather than a truncated word.
	titleMaxCompletionTokens = 64
)

const titlerSystemPrompt = `You name conversations. Given a summary of a conversation, reply with a short title — at most 8 words — that identifies it in a list.

Name the specific subject, not the activity: "Raft snapshot corruption on node B", not "Technical discussion" or "Debugging session". No quotes, no trailing punctuation, no preamble. Reply with the title alone.`

// Titler names a conversation so it can be found in a list.
type Titler interface {
	// Title returns a short label derived from a conversation
	// summary. An empty result means "no opinion" — the caller
	// leaves the session untitled rather than storing a placeholder.
	Title(ctx context.Context, summary string) (string, error)
}

type llmTitler struct {
	provider LLMProvider
	model    string
	maxChars int
}

// NewLLMTitler wires a titler to a provider, typically the same cheap
// RoleSummariser model compaction uses. Nil provider → nil titler,
// which leaves sessions untitled rather than failing anything.
func NewLLMTitler(provider LLMProvider, model string, maxChars int) Titler {
	if provider == nil {
		return nil
	}
	if maxChars <= 0 {
		maxChars = DefaultTitleMaxChars
	}
	return &llmTitler{provider: provider, model: model, maxChars: maxChars}
}

func (t *llmTitler) Title(ctx context.Context, summary string) (string, error) {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return "", nil
	}
	resp, err := t.provider.Chat(ctx, ChatRequest{
		Model:       t.model,
		MaxTokens:   titleMaxCompletionTokens,
		Temperature: 0.2,
		Messages: []Message{
			{Role: "system", Content: titlerSystemPrompt},
			{Role: "user", Content: summary},
		},
	})
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", nil
	}
	return cleanTitle(resp.Content, t.maxChars), nil
}

// cleanTitle strips what models add despite being told not to:
// surrounding quotes, a "Title:" prefix, trailing punctuation, and
// any second line.
func cleanTitle(raw string, maxChars int) string {
	title := strings.TrimSpace(raw)
	if i := strings.IndexAny(title, "\r\n"); i >= 0 {
		title = title[:i]
	}
	for _, prefix := range []string{"Title:", "title:", "Summary:"} {
		title = strings.TrimSpace(strings.TrimPrefix(title, prefix))
	}
	title = strings.Trim(title, `"'`+"`")
	title = strings.TrimRight(title, ".!,;: ")
	title = strings.TrimSpace(title)
	if len(title) > maxChars {
		cut := title[:maxChars]
		// Prefer a word boundary so the label doesn't end mid-word.
		if i := strings.LastIndex(cut, " "); i > maxChars/2 {
			cut = cut[:i]
		}
		title = strings.TrimRight(cut, ".!,;: ") + "…"
	}
	return title
}
