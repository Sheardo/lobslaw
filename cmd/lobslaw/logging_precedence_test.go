package main

import (
	"testing"

	"github.com/jmylchreest/lobslaw/pkg/config"
)

// [logging] level and format were parsed, documented, shipped in
// examples/config.toml — and read by nothing. The logger was built
// from the flags alone, so an operator who set level = "debug" in
// their config file got info-level logs and no explanation.
//
// The order matters as much as the wiring: the file holds a
// deployment's normal level, the flag is what somebody types while
// debugging THIS boot, and the file must never overrule them.

func TestConfigSuppliesTheLevelWhenNobodyPassedAFlag(t *testing.T) {
	t.Parallel()
	level, format, changed := effectiveLogging("info", "auto", map[string]bool{},
		config.LoggingConfig{Level: "debug", Format: "json"})
	if !changed {
		t.Fatal("config had a level and a format; nothing changed")
	}
	if level != "debug" || format != "json" {
		t.Errorf("got %q/%q, want debug/json", level, format)
	}
}

// The whole reason somebody types --log-level=debug is that the file
// says something else.
func TestAnExplicitFlagBeatsTheConfigFile(t *testing.T) {
	t.Parallel()
	level, format, _ := effectiveLogging("debug", "text",
		map[string]bool{"log-level": true, "log-format": true},
		config.LoggingConfig{Level: "error", Format: "json"})
	if level != "debug" {
		t.Errorf("level = %q; the config file overruled an explicit flag", level)
	}
	if format != "text" {
		t.Errorf("format = %q; the config file overruled an explicit flag", format)
	}
}

// Precedence is per-key. Passing --log-level must not discard a format
// the file specified — they are separate decisions.
func TestPrecedenceIsPerKey(t *testing.T) {
	t.Parallel()
	level, format, changed := effectiveLogging("debug", "auto",
		map[string]bool{"log-level": true},
		config.LoggingConfig{Level: "error", Format: "json"})
	if !changed {
		t.Fatal("the config format should still have applied")
	}
	if level != "debug" {
		t.Errorf("level = %q, want the flag to win", level)
	}
	if format != "json" {
		t.Errorf("format = %q, want the file to supply it", format)
	}
}

// Nothing set means nothing moves — and the caller must be told, or it
// rebuilds the logger on every boot for no reason.
func TestAnEmptyConfigChangesNothing(t *testing.T) {
	t.Parallel()
	level, format, changed := effectiveLogging("warn", "text", map[string]bool{},
		config.LoggingConfig{})
	if changed {
		t.Error("an empty [logging] reported a change")
	}
	if level != "warn" || format != "text" {
		t.Errorf("got %q/%q, want the flags untouched", level, format)
	}
}

// Whitespace is not a value. " " in a config file is a key somebody
// started typing, and treating it as a level would fail the parse and
// silently fall back.
func TestWhitespaceIsNotAValue(t *testing.T) {
	t.Parallel()
	level, _, changed := effectiveLogging("info", "auto", map[string]bool{},
		config.LoggingConfig{Level: "   "})
	if changed || level != "info" {
		t.Errorf("level = %q changed = %v; blank should not count", level, changed)
	}
}
