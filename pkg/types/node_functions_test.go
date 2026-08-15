package types

import "testing"

// policy never selected anything of its own — both gRPC services sit
// behind "this node hosts raft", which memory already implies — so it
// is normalised away. The alias exists so configs written against the
// old spelling keep booting; these tests make removing it a
// deliberate act rather than a silent break.
func TestNormalizeFunctions(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		in      []NodeFunction
		want    []NodeFunction
		rewrote int
	}{
		{
			name:    "policy alone becomes memory",
			in:      []NodeFunction{FunctionPolicy},
			want:    []NodeFunction{FunctionMemory},
			rewrote: 1,
		},
		{
			name:    "policy alongside memory collapses to one",
			in:      []NodeFunction{FunctionMemory, FunctionPolicy},
			want:    []NodeFunction{FunctionMemory},
			rewrote: 1,
		},
		{
			name:    "order is preserved",
			in:      []NodeFunction{FunctionCompute, FunctionPolicy, FunctionStorage},
			want:    []NodeFunction{FunctionCompute, FunctionMemory, FunctionStorage},
			rewrote: 1,
		},
		{
			name:    "a set without policy is untouched",
			in:      []NodeFunction{FunctionMemory, FunctionCompute, FunctionGateway, FunctionStorage},
			want:    []NodeFunction{FunctionMemory, FunctionCompute, FunctionGateway, FunctionStorage},
			rewrote: 0,
		},
		{
			name:    "duplicates collapse",
			in:      []NodeFunction{FunctionCompute, FunctionCompute},
			want:    []NodeFunction{FunctionCompute},
			rewrote: 0,
		},
		{
			name: "empty stays empty",
			in:   nil,
			want: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, rewrote := NormalizeFunctions(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
			if len(rewrote) != tc.rewrote {
				t.Errorf("rewrote %v, want %d entries — the caller warns from this",
					rewrote, tc.rewrote)
			}
		})
	}
}

// The alias must never silently drop a role. A config naming policy
// and nothing else still has to end up hosting raft, or an upgrade
// turns a working state node into a node that serves nothing.
func TestPolicyAliasKeepsTheNodeOnRaft(t *testing.T) {
	t.Parallel()
	got, _ := NormalizeFunctions([]NodeFunction{FunctionPolicy})
	var hasMemory bool
	for _, f := range got {
		if f == FunctionMemory {
			hasMemory = true
		}
		if f == FunctionPolicy {
			t.Error("policy survived normalisation; gates would see a value that selects nothing")
		}
	}
	if !hasMemory {
		t.Fatalf("got %v, want memory — a policy-only node must still host raft", got)
	}
}
