package promptgen

import (
	"fmt"
	"strings"
)

// TrustLevel classifies how much we trust the content we're about
// to show the model. The delimiter shape tells the model whether to
// treat the content as authoritative instructions or as untrusted
// data (memory recall, tool output, fetched web pages, message
// content from untrusted users).
//
// Per the safety principles in BuildSafety: content inside
// <untrusted> delimiters is DATA, not orders. The model is trained
// (via the safety block) to refuse embedded instructions within
// untrusted regions.
type TrustLevel int

const (
	// TrustTrusted — operator-authored content (e.g. the assembled
	// system prompt itself). No delimiters needed; included here
	// only so callers can pass a single enum through helpers.
	TrustTrusted TrustLevel = iota

	// TrustUntrusted — anything the caller doesn't vouch for.
	// Default for tool output, memory recall, fetched content,
	// skill output. Rendered inside <untrusted> ... </untrusted>.
	TrustUntrusted

	// TrustUntrustedUser — a specialised subset of TrustUntrusted
	// for messages that came from a human over a channel. Same
	// delimiter shape + an attribution attr so the model can
	// distinguish "user said this" from "a tool returned this text".
	TrustUntrustedUser
)

// ContextCategory separates short-term (this-session) reference
// from long-term (vector-recalled) reference. Generate groups blocks
// by category and renders two distinct sections ("Recent Context"
// vs "Recalled Memory") so the model can weigh them differently:
// short-term wins on conflict ("user just said they moved to Y"),
// long-term informs when short-term is silent.
//
// Empty → treated as short-term (safe default — current session
// context is almost always more relevant than long-term recall).
type ContextCategory string

// The context categories. Budgeting trims long-term blocks before
// short-term ones when the context window is under pressure.
const (
	// CategoryShortTerm is current-session context.
	CategoryShortTerm ContextCategory = "short-term"
	// CategoryLongTerm is recalled context from earlier sessions.
	CategoryLongTerm ContextCategory = "long-term"
)

// ContextBlock is a labelled chunk of data the agent wants to
// expose to the model. Source labels the origin (e.g. "memory:recall",
// "tool:bash:stdout", "channel:telegram"); Content is the raw bytes
// (verbatim into the prompt after delimiter wrapping).
type ContextBlock struct {
	Source   string
	Category ContextCategory // short-term (session) | long-term (recalled)
	Trust    TrustLevel
	Content  string
}

// WrapContext renders one or more ContextBlocks into a single
// string block with delimiter wrapping per trust level. Multiple
// blocks are emitted in input order — callers have already decided
// priority (e.g. memory recall before tool output).
//
// Empty blocks are elided. Empty total output yields "" (not an
// empty <untrusted></untrusted> tag pair — those would just confuse
// a reader).
//
// Delimiter choice: explicit XML-like tags (<untrusted>, </untrusted>)
// because LLMs handle them reliably at tokenization boundaries and
// they're distinctive enough to escape from nested content via
// source attribute. We do NOT attempt to escape < > in user content
// — a user who includes `</untrusted>` in their message CAN close the
// block; the safety training on the model side treats this as
// attempted injection and surfaces it to the user rather than
// obeying.
func WrapContext(blocks []ContextBlock) string {
	var b strings.Builder
	for _, block := range blocks {
		if block.Content == "" {
			continue
		}
		switch block.Trust {
		case TrustTrusted:
			fmt.Fprintf(&b, "<!-- source:%s -->\n%s\n", block.Source, block.Content)
		case TrustUntrusted:
			fmt.Fprintf(&b, "<untrusted source=%q>\n%s\n</untrusted>\n",
				block.Source, NeutraliseDelimiters(block.Content))
		case TrustUntrustedUser:
			fmt.Fprintf(&b, "<untrusted-user source=%q>\n%s\n</untrusted-user>\n",
				block.Source, NeutraliseDelimiters(block.Content))
		default:
			// Unknown trust levels → fall through to untrusted.
			// Never up-trust an unknown level to trusted.
			fmt.Fprintf(&b, "<untrusted source=%q>\n%s\n</untrusted>\n",
				block.Source, NeutraliseDelimiters(block.Content))
		}
	}
	return b.String()
}

// Delimiters are the tags this package emits around untrusted content.
//
// EXPORTED BECAUSE THE SCANNER NEEDS THE SAME LIST. promptguard kept
// its own copy, which is two authorities for one fact: adding a
// wrapper tag here would leave the scanner silently not covering it,
// and nothing would fail.
//
// Only the tags THIS package writes. The chat-template control tokens
// several models honour — im_start, INST and the rest — are the
// scanner's own business, because nothing here emits them.
func Delimiters() []string {
	return []string{
		"</untrusted-user",
		"<untrusted-user",
		"</untrusted",
		"<untrusted",
	}
}

// NeutraliseDelimiters makes a wrapper tag inside untrusted content
// unable to close the block it is in.
//
// THE INGEST SCANNER IS NOT ENOUGH ON ITS OWN. It quarantines records
// carrying these fragments, which covers everything that went through
// ingest — and R5 asks that a record cannot escape "on any path". A
// record stored before the scanner existed, imported by another route,
// or content that reaches this function from somewhere other than
// recall would otherwise close its own block, and everything after it
// would read as instructions rather than as data.
//
// Only the delimiter's opening angle bracket is escaped, and only
// where it begins one of these tags. Escaping every "<" would mangle
// any memory containing code, which is most of them here, and a
// defence that corrupts ordinary content gets turned off.
func NeutraliseDelimiters(content string) string {
	if content == "" {
		return content
	}
	out := content
	for _, d := range Delimiters() {
		// Case-insensitively: a model reading "</UNTRUSTED>" is not
		// obviously less confused than one reading the lowercase form,
		// and the scanner already matches without regard to case.
		out = replaceFold(out, d, "&lt;"+strings.TrimPrefix(d, "<"))
	}
	return out
}

// replaceFold replaces every case-insensitive occurrence of old.
func replaceFold(s, old, new string) string {
	if old == "" {
		return s
	}
	var b strings.Builder
	lower, lowerOld := strings.ToLower(s), strings.ToLower(old)
	for {
		i := strings.Index(lower, lowerOld)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		b.WriteString(new)
		s, lower = s[i+len(old):], lower[i+len(lowerOld):]
	}
}
