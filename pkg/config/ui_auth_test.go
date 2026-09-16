package config

import (
	"strings"
	"testing"
)

// The console can rewrite a bot's instructions, read every
// conversation and assign the team work. [auth] require_auth defaults
// false because that is right for an API behind a reverse proxy; it is
// not right for an admin console, and the difference has to be
// enforced rather than documented.
//
// A refusal rather than a warning: a warning at boot is a line in a
// log nobody reads until afterwards, and the failure it precedes is
// somebody else's browser.

func TestUIOnEveryInterfaceWithoutAuthRefusesToStart(t *testing.T) {
	t.Parallel()
	cfg := Config{
		Gateway: GatewayConfig{UI: UIConfig{Enabled: true}},
		Auth:    AuthConfig{RequireAuth: false},
	}
	err := validateUIAuth(cfg.Gateway, cfg.Auth)
	if err == nil {
		t.Fatal("an unauthenticated console on every interface was accepted")
	}
	// The message has to say what to do, because the operator reading
	// it wanted a console and now has a node that will not boot.
	for _, want := range []string{"require_auth", "localhost"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}

func TestUIWithAuthIsAccepted(t *testing.T) {
	t.Parallel()
	err := validateUIAuth(
		GatewayConfig{UI: UIConfig{Enabled: true}, BindAddress: "0.0.0.0"},
		AuthConfig{RequireAuth: true},
	)
	if err != nil {
		t.Errorf("an authenticated console was refused: %v", err)
	}
}

// Loopback is exempt: an operator running the console on their own
// machine is the case the default was written for, and making them
// stand up a JWT issuer to see their own bots would push them towards
// binding everything to avoid the hassle.
func TestUIOnLoopbackWithoutAuthIsAccepted(t *testing.T) {
	t.Parallel()
	for _, bind := range []string{"127.0.0.1", "127.0.0.1:8080", "localhost:8080", "[::1]:8080"} {
		err := validateUIAuth(
			GatewayConfig{UI: UIConfig{Enabled: true}, BindAddress: bind},
			AuthConfig{RequireAuth: false},
		)
		if err != nil {
			t.Errorf("loopback bind %q was refused: %v", bind, err)
		}
	}
}

// An empty bind is every interface, not loopback — and it is the value
// an operator is most likely to leave unset.
func TestEmptyBindIsNotTreatedAsLoopback(t *testing.T) {
	t.Parallel()
	if isLoopbackBind("") {
		t.Error("an empty bind was treated as loopback; that is every interface")
	}
	if err := validateUIAuth(
		GatewayConfig{UI: UIConfig{Enabled: true}},
		AuthConfig{RequireAuth: false},
	); err == nil {
		t.Error("an unset bind with no auth was accepted")
	}
}

func TestRoutableBindsAreNotLoopback(t *testing.T) {
	t.Parallel()
	for _, bind := range []string{"0.0.0.0:8080", "192.168.1.10:8080", "10.0.0.5", "example.com:8080"} {
		if isLoopbackBind(bind) {
			t.Errorf("%q was treated as loopback", bind)
		}
	}
}

// The rule only applies when the console is on. An API-only node keeps
// the documented default.
func TestConsoleOffLeavesTheAuthDefaultAlone(t *testing.T) {
	t.Parallel()
	if err := validateUIAuth(GatewayConfig{}, AuthConfig{RequireAuth: false}); err != nil {
		t.Errorf("a node with no console was refused: %v", err)
	}
}

// And it runs as part of the real Validate, not only when called
// directly — a guard nothing invokes is a guard that does not exist.
func TestValidateEnforcesTheConsoleAuthRule(t *testing.T) {
	t.Parallel()
	cfg := &Config{Gateway: GatewayConfig{UI: UIConfig{Enabled: true}}}
	if err := cfg.Validate(); err == nil {
		t.Error("Validate accepted an unauthenticated console on every interface")
	}
}
