---
title: Web search
description: Configure the web_search builtin — SearXNG, Exa, or any engine described in TOML.
---

# Web search

`web_search` hands the agent a query and gets back a list of `{title, url, snippet}` it can cite. Which engine answers is entirely a configuration decision: lobslaw ships compiled drivers for **SearXNG** and **Exa**, and a **template** driver that describes any other engine in TOML with no Go and no rebuild.

No search provider configured means no `web_search` tool. The model does not see it and says so honestly when asked to look something up.

## Self-hosted SearXNG

[SearXNG](https://docs.searxng.org/) is a metasearch front-end that aggregates other engines, runs on your own hardware, and needs no account. It is the only backend here that does not hand the user's query to a third party.

```toml
[compute.web_search]
provider = "searxng"

[[compute.search_providers]]
label      = "searxng"
driver     = "searxng"
endpoint   = "http://searxng:8080/search"
trust_tier = "local"
options    = { language = "en", safesearch = "1" }
```

`endpoint` accepts the base URL or the full search path — `http://searxng:8080` and `http://searxng:8080/search` mean the same thing. A reverse proxy mounting SearXNG under a prefix should give the full path.

Supported `options`, all optional and passed straight through: `engines`, `categories`, `language`, `time_range`, `safesearch`, `pageno`. A blank value means "no preference" and is not sent.

**Think twice before pinning `engines`.** Search engines block metasearch front-ends aggressively — CAPTCHAs and rate limits are routine, not exceptional. Pinning to a short list makes your search only as available as those engines, and a two-engine pin where one is blocked leaves you resting on one. In testing, `engines = "google,duckduckgo"` went to zero results once Google rate-limited, while the default set returned twenty in the same conditions with three of its engines down. Having spares is what a metasearch front-end is *for*.

### Two things to get right

**1 — Enable the JSON API.** It is off by default, and an instance serving only HTML answers lobslaw's request with a bare `403`. In the instance's `settings.yml`:

```yaml
search:
  formats:
    - html
    - json
```

lobslaw detects this case specifically and returns the snippet above rather than a decode error, but it is quicker to set it first.

**Every engine blocked** is a separate case, and lobslaw treats it as a backend failure rather than an answer. SearXNG returns HTTP 200 with `{"results":[]}` when its upstreams all refuse it, and names them in `unresponsive_engines`; the driver surfaces that as a transient error listing each engine and its reason, so a failover chain moves on and you get a diagnosis instead of a silently empty list. A genuinely obscure query that finds nothing still returns an empty result set, not an error.

**2 — Open the network to egress.** `web_search` routes through the smokescreen proxy, which refuses private IP ranges regardless of the hostname ACL. A SearXNG in the same compose file is on a private address by construction:

```toml
[security]
egress_allow_ranges = ["172.16.0.0/12"]   # the compose bridge, not all of RFC1918
```

lobslaw warns at boot if it spots this without the setting. See [Egress and ACL](/security/egress-and-acl) for the full picture.

## Hosted APIs

Exa has a compiled driver:

```toml
[compute.web_search]
provider = "exa"

[[compute.search_providers]]
label       = "exa"
driver      = "exa"
api_key_ref = "env:EXA_API_KEY"
trust_tier  = "public"
```

`endpoint` is optional and defaults to Exa's; the `EXA_API_URL` environment variable overrides both, for pointing a container at a staging instance without editing config.

## Any other engine, without writing Go

`driver = "template"` builds a driver from the description. It covers one request and one response, which is the shape of essentially every search API.

Brave — GET, a custom auth header, results nested under `web.results`:

```toml
[[compute.search_providers]]
label        = "brave"
driver       = "template"
endpoint     = "https://api.search.brave.com/res/v1/web/search"
api_key_ref  = "env:BRAVE_API_KEY"
trust_tier   = "public"
options      = { query_param = "q", count_param = "count",
                 auth_style = "header", auth_name = "X-Subscription-Token" }
extra_params = { safesearch = "moderate" }
response     = { results = "web.results", snippet = "description", published_at = "page_age" }
```

Tavily — POST with a JSON body and bearer auth. Its response already matches the defaults, so it needs no `response` block at all:

```toml
[[compute.search_providers]]
label        = "tavily"
driver       = "template"
endpoint     = "https://api.tavily.com/search"
api_key_ref  = "env:TAVILY_API_KEY"
trust_tier   = "public"
options      = { method = "POST", query_param = "query", count_param = "max_results" }
extra_params = { search_depth = "basic" }
```

### `options`

| Key | Default | Meaning |
|---|---|---|
| `method` | `GET` | `GET` sends parameters as a query string, `POST` as a JSON body |
| `query_param` | `q` | Which parameter carries the query |
| `count_param` | — | Which parameter carries the result count. Omit if the API has none |
| `depth_param` | — | Receives the tool's `type` argument (`auto`/`fast`/`deep`) |
| `auth_style` | `bearer` when a key is set, else `none` | `header`, `bearer`, `query`, or `none` |
| `auth_name` | — | Header or parameter name. Required for `header` and `query` |

`extra_params` are static parameters merged into every request.

### `response`

Values are dot-paths into the decoded JSON. `results` is a path from the root; the rest are paths within one result.

| Key | Default |
|---|---|
| `results` | `results` |
| `title` | `title` |
| `url` | `url` |
| `snippet` | `content` |
| `published_at` | `publishedDate` |
| `score` | `score` |

A path that resolves to nothing is treated as an absent optional field. A `results` path that does not resolve to an array is an error naming the path you configured, because that is the one thing a hand-written mapping gets wrong.

## Telling the agent which backend it has

Two places name the configured backend, and they answer different questions.

The **tool description** carries it — *"This deployment's search backend is `searxng`"* — so the agent knows before it calls anything. Each **response** repeats it in `provider`, with a per-result `engine` where the backend reports one, so the agent can say which backend served a particular search.

Both exist because of a real failure. Asked whether a search had gone through the operator's self-hosted SearXNG, the agent checked the MCP registry, `debug_tools`, `/etc`, and `pgrep` — and answered that there was no SearXNG on the host, while holding results that had come from one. `shell_command` is sandboxed with no `/proc`, so the process table was never going to show it, and nothing else it could reach named the search backend. Adding `provider` to the response fixed the second question; only the description fixes the first, because configuration questions get asked before any search happens.

## Failover

Naming more than one provider makes a chain, tried in order:

```toml
[compute.web_search]
providers = ["searxng", "exa"]
```

The chain advances on a transient failure (a 5xx, a timeout, a refused connection), on an exhausted quota, and on a rejected credential — the next backend has its own key. It does **not** advance on a permanent failure such as a malformed request or a SearXNG instance with the JSON API disabled: every backend would reject it identically, and you would read the last one's error instead of the first one's.

## Trust tiers

`web_search` is checked against the soul's `min_trust_tier` before a backend runs, like every other provider-backed builtin. This matters more than it looks: a search hands the user's own words to whoever answers. A self-hosted SearXNG can honestly declare `trust_tier = "local"`; a hosted API cannot.

A provider that declares no tier fails any floor that is set — an undeclared tier is not evidence of a high one.

## Migrating from the Exa-only config

The pre-driver shape still means what it meant:

```toml
[compute.web_search]
api_key_ref = "env:EXA_API_KEY"
```

That is Exa, unchanged, and needs no edit. The one behaviour change to know about: search now goes through the `web_search` egress role and is subject to the trust floor, so a deployment already running with a floor set must declare a tier on its provider.

## Adding a compiled driver

If an engine needs real behaviour — a second call, a request signature, a pagination loop — it earns a Go driver rather than a template. That is one file implementing `compute.SearchDriver` plus one `RegisterSearch` line in `internal/node/wire_drivers.go`. See [Providers](/configuration/providers) for the wider driver waist this sits in.
