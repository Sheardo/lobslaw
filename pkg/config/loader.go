package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	koanftoml "github.com/knadh/koanf/parsers/toml/v2"
	koanfenv "github.com/knadh/koanf/providers/env/v2"
	koanffile "github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"

	"github.com/jmylchreest/lobslaw/pkg/types"
)

const (
	envPrefix     = "LOBSLAW__" // prefix + section separator collapsed; no trailing-underscore pitfall
	keyDelim      = "."
	envSectionSep = "__" // double underscore separates sections; single stays inside a key name
)

// LoadOptions controls how Load resolves its config source.
type LoadOptions struct {
	Path    string // explicit path; wins over all other sources
	SkipEnv bool   // disable env-var overrides (tests)
}

// Load reads lobslaw configuration in priority order (highest wins):
// opts.Path, $LOBSLAW_CONFIG, ./config.toml, $XDG_CONFIG_HOME/lobslaw/config.toml,
// $HOME/.config/lobslaw/config.toml. Missing file is OK — env-only is valid.
//
// System-wide paths like /etc/lobslaw/ are deliberately NOT in the
// fallback chain: lobslaw is container-first, and in containers the
// config root is the working directory or a mounted volume, not /etc.
// Dev workflows use the CWD-relative or XDG paths.
//
// Env-var overrides use double underscore (__) as the section
// separator and preserve single underscores inside keys:
//
//	LOBSLAW__MEMORY__RAFT_PORT=9999          → memory.raft_port
//	LOBSLAW__MEMORY__ENCRYPTION__KEY_REF=... → memory.encryption.key_ref
//
// The prefix is lowercased and stripped; what remains is split on
// __ into a hierarchy path.
func Load(opts LoadOptions) (*Config, error) {
	k := koanf.New(keyDelim)

	path, err := findConfigPath(opts.Path)
	if err != nil {
		return nil, err
	}
	if path != "" {
		if err := k.Load(koanffile.Provider(path), koanftoml.Parser()); err != nil {
			return nil, fmt.Errorf("%w: read %s: %w", types.ErrInvalidConfig, path, err)
		}
	}

	if !opts.SkipEnv {
		if err := k.Load(koanfenv.Provider(".", koanfenv.Opt{
			Prefix: envPrefix,
			TransformFunc: func(key, value string) (string, any) {
				key = strings.TrimPrefix(key, envPrefix)
				key = strings.ToLower(key)
				key = strings.ReplaceAll(key, envSectionSep, keyDelim)
				return key, value
			},
		}), nil); err != nil {
			return nil, fmt.Errorf("%w: env overlay: %w", types.ErrInvalidConfig, err)
		}
	}

	cfg := &Config{}
	if err := k.UnmarshalWithConf("", cfg, koanf.UnmarshalConf{Tag: "koanf"}); err != nil {
		return nil, fmt.Errorf("%w: unmarshal: %w", types.ErrInvalidConfig, err)
	}
	cfg.resolvedPath = path

	// LOBSLAW_PROVIDER_<LABEL>_<FIELD> env vars merge with the TOML-
	// sourced providers slice before validation, so operators can
	// declare providers entirely via env (container-first workflow)
	// or override individual fields on a TOML-declared provider.
	if !opts.SkipEnv {
		if err := applyEnvProviders(cfg); err != nil {
			return nil, fmt.Errorf("%w: env providers: %w", types.ErrInvalidConfig, err)
		}
	}

	if cfg.Security.BinaryInstallPrefix == "" {
		cfg.Security.BinaryInstallPrefix = "/lobslaw/usr/local"
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Validate checks required-key invariants that cross subsystem
// boundaries and must hold before any node starts. Subsystem-
// specific invariants (e.g. cert file existence, provider label
// uniqueness) are validated by their owning packages.
func (c *Config) Validate() error {
	if c.Memory.Enabled && c.Memory.Encryption.KeyRef == "" {
		return fmt.Errorf("%w: memory.enabled=true requires memory.encryption.key_ref (e.g. env:LOBSLAW_MEMORY_KEY)", types.ErrInvalidConfig)
	}
	if c.Memory.Enabled && !c.Storage.Enabled {
		return fmt.Errorf("%w: memory.enabled=true requires storage.enabled=true on the same node (snapshot-export targets resolve via local storage mounts)", types.ErrInvalidConfig)
	}
	if err := validateProviderBackups(c.Compute.Providers); err != nil {
		return err
	}
	if err := validateContextConfig(c.Compute.Context); err != nil {
		return err
	}
	return nil
}

// validateContextConfig rejects context-budget settings that would
// misbehave quietly rather than loudly. Every field here is optional,
// so only explicitly-set values are checked.
func validateContextConfig(c ContextConfig) error {
	for _, f := range []struct {
		name string
		val  *int
	}{
		{"tail_tokens", c.TailTokens},
		{"history_tool_result_bytes", c.HistoryToolResultBytes},
		{"tail_messages", c.TailMessages},
		{"compact_keep_messages", c.CompactKeepMessages},
		{"compact_trigger_tokens", c.CompactTriggerTokens},
		{"compact_max_summary_tokens", c.CompactMaxSummaryTokens},
		{"compact_max_completion_tokens", c.CompactMaxCompletionTokens},
		{"compact_tool_result_bytes", c.CompactToolResultBytes},
	} {
		if f.val != nil && *f.val < 0 {
			return fmt.Errorf("%w: compute.context.%s must not be negative (got %d)",
				types.ErrInvalidConfig, f.name, *f.val)
		}
	}
	// A summary larger than the whole verbatim budget means the
	// compacted head crowds out the recent exchange it was supposed
	// to make room for — the opposite of what compaction is for.
	if c.CompactMaxSummaryTokens != nil && c.TailTokens != nil &&
		*c.TailTokens > 0 && *c.CompactMaxSummaryTokens >= *c.TailTokens {
		return fmt.Errorf("%w: compute.context.compact_max_summary_tokens (%d) must be smaller than tail_tokens (%d), else the summary crowds out the conversation it summarises",
			types.ErrInvalidConfig, *c.CompactMaxSummaryTokens, *c.TailTokens)
	}
	return nil
}

// validateProviderBackups enforces two invariants on the implicit
// chain built from ProviderConfig.Backup pointers: every Backup
// value must reference an existing label, and walking the chain
// from any starting provider must terminate — no cycles.
func validateProviderBackups(providers []ProviderConfig) error {
	labels := make(map[string]bool, len(providers))
	for _, p := range providers {
		if p.Label == "" {
			continue
		}
		labels[p.Label] = true
	}
	for _, p := range providers {
		if p.Backup == "" {
			continue
		}
		if !labels[p.Backup] {
			return fmt.Errorf("%w: provider %q has backup=%q which is not a defined provider label", types.ErrInvalidConfig, p.Label, p.Backup)
		}
	}
	// Cycle detection: walk each starting point and bail if we
	// revisit. Bound the walk by the provider count to handle the
	// pathological case defensively.
	indexByLabel := make(map[string]int, len(providers))
	for i, p := range providers {
		indexByLabel[p.Label] = i
	}
	for _, start := range providers {
		if start.Backup == "" {
			continue
		}
		seen := map[string]bool{start.Label: true}
		cur := start.Backup
		for step := 0; step < len(providers); step++ {
			if seen[cur] {
				return fmt.Errorf("%w: provider backup chain has a cycle starting at %q (revisits %q)", types.ErrInvalidConfig, start.Label, cur)
			}
			seen[cur] = true
			idx, ok := indexByLabel[cur]
			if !ok {
				break
			}
			next := providers[idx].Backup
			if next == "" {
				break
			}
			cur = next
		}
	}
	return nil
}

func findConfigPath(explicit string) (string, error) {
	if explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			return "", fmt.Errorf("%w: config file %q: %w", types.ErrInvalidConfig, explicit, err)
		}
		return explicit, nil
	}
	if p := os.Getenv("LOBSLAW_CONFIG"); p != "" {
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("%w: LOBSLAW_CONFIG=%q: %w", types.ErrInvalidConfig, p, err)
		}
		return p, nil
	}
	candidates := []string{"./config.toml"}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		candidates = append(candidates, filepath.Join(xdg, "lobslaw", "config.toml"))
	} else if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".config", "lobslaw", "config.toml"))
	}

	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("%w: stat %s: %w", types.ErrInvalidConfig, c, err)
		}
	}
	return "", nil
}
