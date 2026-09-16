package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jmylchreest/lobslaw/internal/compute"
	"github.com/jmylchreest/lobslaw/internal/ids"
)

// handleBotChat serves POST /v1/bots/{id}/messages.
//
// Talking to a SPECIFIC bot, which /v1/messages cannot do — that
// endpoint is the coordinator's, shared with Telegram and Slack, and a
// console that could only reach the coordinator would make every specialist
// something you can configure but not converse with.
//
// Streamed as Server-Sent Events. A bot turn can run tools for a
// minute, and a request that returns nothing until it finishes is
// indistinguishable from one that has hung — which is how somebody
// ends up reloading and starting a second turn.
func (s *Server) handleBotChat(w http.ResponseWriter, r *http.Request, botID string) {
	if s.cfg.Turns == nil {
		s.jsonErr(w, http.StatusServiceUnavailable, "this node cannot run turns")
		return
	}
	if r.Method != http.MethodPost {
		s.jsonErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	var body struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.jsonErr(w, http.StatusBadRequest, "malformed JSON: "+err.Error())
		return
	}
	if strings.TrimSpace(body.Message) == "" {
		s.jsonErr(w, http.StatusBadRequest, "message is required")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		// Without flushing, SSE is just a slow JSON response that
		// arrives all at once — worse than admitting it.
		s.jsonErr(w, http.StatusInternalServerError, "this server cannot stream")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Reverse proxies buffer by default and would hold the whole
	// stream until the turn ended, which defeats the point.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	turnID := ids.New()
	sendSSE(w, flusher, "start", map[string]any{"bot": botID, "turn_id": turnID})

	// A heartbeat while the turn runs. The console shows it as
	// "working", and it also keeps an idle-timeout proxy from closing a
	// connection that is legitimately quiet for ninety seconds.
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				sendSSE(w, flusher, "working", map[string]any{"turn_id": turnID})
			}
		}
	}()

	resp, err := s.runBotTurn(r, botID, body.Message, turnID)
	close(done)

	if err != nil {
		// The error goes down the STREAM, not as a status: the 200 and
		// the headers are already on the wire by the time a turn can
		// fail, and a client parsing SSE has nowhere to put a status
		// code that arrives afterwards.
		sendSSE(w, flusher, "error", map[string]any{"message": err.Error()})
		return
	}
	if resp.NeedsConfirmation {
		sendSSE(w, flusher, "needs_confirmation", map[string]any{
			"reason": resp.ConfirmationReason,
			"note": "approve it on the channel you normally use — a confirmation is " +
				"raised for the person who asked, and this console is not that channel",
		})
		return
	}
	sendSSE(w, flusher, "reply", map[string]any{
		"text":       resp.Reply,
		"turn_id":    turnID,
		"tool_calls": len(resp.ToolCalls),
	})
}

func (s *Server) runBotTurn(r *http.Request, botID, message, turnID string) (*compute.ProcessMessageResponse, error) {
	claims, err := s.authenticate(r)
	if err != nil {
		return nil, err
	}
	return s.cfg.Turns.Run(r.Context(), compute.TurnRequest{
		BotID:    botID,
		Prompt:   message,
		Origin:   "console",
		OriginID: turnID,
		// The operator's claims, not the bot's: a turn somebody started
		// from the console is attributed to them, the same way a
		// scheduled routine is attributed to whoever scheduled it.
		Claims:         claims,
		TurnIDOverride: turnID,
		Channel:        botChannel,
		ChannelID:      botID,
	})
}

// sendSSE writes one event. Errors are dropped: the only failure here
// is a client that has gone away, and there is nowhere left to report
// that to.
func sendSSE(w http.ResponseWriter, flusher http.Flusher, event string, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, body)
	flusher.Flush()
}

// BotTurnRunner is how the console starts a turn as a specific bot.
// *compute.TurnRunner satisfies it; an interface so a test can assert
// on what the handler asked for without booting a provider.
type BotTurnRunner interface {
	Run(ctx context.Context, req compute.TurnRequest) (*compute.ProcessMessageResponse, error)
}
