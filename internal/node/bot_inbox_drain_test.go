package node

import (
	"strings"
	"testing"

	"github.com/jmylchreest/lobslaw/internal/compute"
)

// A turn can succeed and say nothing — the model spends the whole turn
// on tool calls and never writes a closing message. That empty string
// used to be stored verbatim, so the item showed up DONE with a blank
// body: in the console, identical to an item that never ran. The queue
// exists so that nothing disappears, and a silent success is the one
// disappearance that looks like everything is fine.
func TestInboxResultIsNeverEmptyOnSuccess(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		resp *compute.ProcessMessageResponse
		want string // substring
	}{
		{
			name: "a real reply is passed through untouched",
			resp: &compute.ProcessMessageResponse{Reply: "Cluster is healthy."},
			want: "Cluster is healthy.",
		},
		{
			name: "whitespace is not a reply",
			resp: &compute.ProcessMessageResponse{Reply: "   \n\t "},
			want: "without doing anything",
		},
		{
			name: "silent but busy says what it ran",
			resp: &compute.ProcessMessageResponse{
				Reply: "",
				ToolCalls: []compute.ToolInvocation{
					{ToolName: "shell_command"},
					{ToolName: "shell_command"},
					{ToolName: "notify"},
				},
			},
			// InvokedToolNames sorts, so the summary is stable
			// regardless of the order the model happened to call in.
			want: "notify, shell_command",
		},
		{
			name: "silent and idle admits it",
			resp: &compute.ProcessMessageResponse{},
			want: "without doing anything",
		},
		{
			name: "no response at all still records something",
			resp: nil,
			want: "returned nothing",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := inboxResult(tc.resp)
			if strings.TrimSpace(got) == "" {
				t.Fatal("a successful item was resolved with an empty result")
			}
			if !strings.Contains(got, tc.want) {
				t.Errorf("result = %q, want it to contain %q", got, tc.want)
			}
		})
	}
}
