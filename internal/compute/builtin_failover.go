package compute

import (
	"context"
	"fmt"
	"log/slog"
)

// failoverBuiltin turns an ordered list of handlers for one modality
// into a single handler that walks them.
//
// The ordering is not invented here. Operators already express it —
// [[compute.providers]] entries carry capability tags and a priority,
// and SelectByCapability has always returned them sorted, with a
// comment saying the order existed "for the future fallback-chain
// layer". This is that layer; the resolver simply stopped at the first
// match and threw the rest away.
//
// The advance decision is isRetryableProviderError, the same predicate
// the chat backup chain uses. That is deliberate: a second failover
// policy would drift from the first, and the point of the driver waist
// is that "should I try the next provider" has one answer per failure
// class no matter which modality asked.
//
// Argument errors need no special case. A bad path or a missing
// required field fails before any HTTP call, is unclassified, and so
// classifies permanent — the chain stops on the first handler rather
// than re-validating the same bad input against every provider.
func failoverBuiltin(modality string, log *slog.Logger, handlers ...BuiltinFunc) BuiltinFunc {
	if len(handlers) == 1 {
		// One provider is the common case and deserves no wrapper: no
		// extra frame, and nothing in the logs implying a chain exists.
		return handlers[0]
	}
	if log == nil {
		log = slog.Default()
	}
	return func(ctx context.Context, args map[string]string) ([]byte, int, error) {
		var (
			lastOut  []byte
			lastCode int
			lastErr  error
		)
		for i, h := range handlers {
			out, code, err := h(ctx, args)
			if err == nil {
				if i > 0 {
					log.Info("compute: modality backup succeeded",
						"modality", modality, "provider_index", i,
						"prior_error", lastErr.Error())
				}
				return out, code, nil
			}
			if !isRetryableProviderError(ctx, err) {
				return out, code, err
			}
			log.Warn("compute: modality provider failed; walking chain",
				"modality", modality, "provider_index", i,
				"class", ClassifyFailure(err).String(), "err", err)
			lastOut, lastCode, lastErr = out, code, err
		}
		// Every provider was tried and every one failed retryably. The
		// last error is the most recent evidence, and the count tells an
		// operator this was a chain-wide outage rather than one endpoint
		// having a bad minute.
		return lastOut, lastCode, fmt.Errorf(
			"%s: all %d providers in the chain failed; last error: %w",
			modality, len(handlers), lastErr)
	}
}
