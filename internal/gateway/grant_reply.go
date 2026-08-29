package gateway

import "strings"

// What an approval reply says it granted.
//
// "I won't ask about this again" does not name "this". That was
// tolerable while a grant covered a tool — the user had just read the
// tool's name in the prompt above. It stopped being tolerable when a
// grant started covering a command: the difference between `git status`
// and every command on the machine is the whole design, and a reply
// that omits it is asking the user to trust that the narrow thing
// happened.
//
// Shared by both channels rather than written twice, because the two
// have to agree about what a grant means and a copy is how they stop
// agreeing.

// grantReplyMaxDisplay bounds what is echoed back. The resource is
// already bounded when it is a shell key, but a skill or MCP tool name
// is not, and a reply is a chat message rather than a document.
const grantReplyMaxDisplay = 160

// alwaysGrantReply is the confirmation shown after a permanent grant.
func alwaysGrantReply(resource string) string {
	if d := displayResource(resource); d != "" {
		return "Approved — I won't ask about " + d +
			" again. Revoke it with `lobslaw policy revoke-approvals`."
	}
	return "Approved — I won't ask about this again. Revoke it with `lobslaw policy revoke-approvals`."
}

// sessionGrantReply is the confirmation shown after a grant scoped to
// the conversation. where is the channel's word for it — "chat" on
// Telegram, "conversation" on Slack.
func sessionGrantReply(resource, where string) string {
	if d := displayResource(resource); d != "" {
		return "Approved — I won't ask again about " + d + " in this " + where + "."
	}
	return "Approved — I won't ask again for this in this " + where + "."
}

// displayResource renders a resource for a reply, or empty when there
// is nothing worth naming.
//
// Backticks are stripped rather than escaped: the resource goes inside
// a code span, and a backtick within it would end the span early and
// let the rest render as markup. Stripping is safe here because the
// result is only ever displayed — the minted rule carries the real
// string.
func displayResource(resource string) string {
	r := strings.TrimSpace(resource)
	if r == "" {
		return ""
	}
	r = strings.ReplaceAll(r, "`", "")
	if r == "" {
		return ""
	}
	if len([]rune(r)) > grantReplyMaxDisplay {
		r = string([]rune(r)[:grantReplyMaxDisplay-1]) + "…"
	}
	return "`" + r + "`"
}
