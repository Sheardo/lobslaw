// Package config loads lobslaw configuration from TOML plus environment
// variable overrides, using github.com/knadh/koanf/v2 as the layered
// configuration library. Secret refs (env:FOO, file:/path, or a
// [[secrets.providers]] label such as bw:app/key) are
// resolved at load time.
package config
