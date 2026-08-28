package compute

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// SearXNG: the privacy-preserving metasearch front-end, and the reason
// this whole seam exists. It aggregates other engines, runs on the
// operator's own hardware, and needs no account — which makes it the
// only search backend that does not hand the user's query to a third
// party, and therefore the one a "privacy- and security-first" agent
// should be able to use.

// searxngJSONHint is the fix for the single most common way a SearXNG
// integration fails. The JSON API is OFF by default; an instance
// serving only HTML answers a format=json request with a 403 and no
// explanation of which knob was missed.
const searxngJSONHint = `SearXNG returned a non-JSON response. Its JSON API is disabled by default — ` +
	`add json to search.formats in the instance's settings.yml and restart it:

search:
  formats:
    - html
    - json`

// SearxngSearchFactory builds the SearXNG driver.
func SearxngSearchFactory(cfg SearchDriverConfig) (SearchDriver, error) {
	if bad := unknownOptions(cfg.Options,
		"engines", "categories", "language", "time_range", "safesearch", "pageno",
	); len(bad) > 0 {
		return nil, fmt.Errorf("searxng search: unknown option(s) %v; supported: "+
			"engines, categories, language, time_range, safesearch, pageno", bad)
	}
	if strings.TrimSpace(cfg.Endpoint) == "" {
		return nil, errors.New(`searxng search: endpoint required ` +
			`(e.g. endpoint = "http://searxng:8080/search") — a self-hosted instance has no default address`)
	}
	endpoint, err := searxngEndpoint(cfg.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("searxng search: %w", err)
	}
	return &searxngDriver{
		endpoint: endpoint,
		// Nil is the normal case: a private instance authenticates
		// nobody. A credential is still honoured because operators do
		// put SearXNG behind an authenticating reverse proxy.
		cred:   cfg.Credential,
		opts:   cfg.Options,
		client: searchHTTPClient(cfg.HTTPClient),
	}, nil
}

// searxngEndpoint accepts either the base URL or the full search path.
// Operators reach for the address they paste into a browser, and
// "http://searxng:8080" silently returning the landing page instead of
// results is a worse outcome than accepting both.
func searxngEndpoint(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	if u.Host == "" {
		return "", fmt.Errorf("endpoint %q has no host", raw)
	}
	if p := strings.TrimSuffix(u.Path, "/"); p == "" {
		u.Path = "/search"
	}
	return u.String(), nil
}

type searxngDriver struct {
	endpoint string
	cred     Credential
	opts     map[string]string
	client   *http.Client
}

func (d *searxngDriver) Search(ctx context.Context, req SearchRequest) ([]SearchResult, error) {
	u, err := url.Parse(d.endpoint)
	if err != nil {
		return nil, Permanent(err)
	}
	q := u.Query()
	q.Set("q", req.Query)
	q.Set("format", "json")
	for _, opt := range []string{"engines", "categories", "language", "time_range", "safesearch", "pageno"} {
		if v := option(d.opts, opt); v != "" {
			q.Set(opt, v)
		}
	}
	u.RawQuery = q.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, Permanent(err)
	}
	httpReq.Header.Set("Accept", "application/json")
	if d.cred != nil {
		if err := d.cred.Apply(ctx, httpReq); err != nil {
			return nil, CredentialRejected(err)
		}
	}

	resp, err := d.client.Do(httpReq)
	if err != nil {
		return nil, Transient(fmt.Errorf("searxng search: http: %w", err))
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		// A 403 from SearXNG is almost never an auth problem — it is
		// how the instance refuses a format it was not configured to
		// serve. Saying "credentials rejected" would send the operator
		// looking for a key that does not exist.
		if resp.StatusCode == http.StatusForbidden && !looksLikeJSON(raw) {
			return nil, Permanent(fmt.Errorf("searxng search: HTTP 403. %s", searxngJSONHint))
		}
		return nil, &DriverError{
			Class: ClassifyHTTPStatus(resp.StatusCode, string(raw)),
			Err:   fmt.Errorf("searxng search: HTTP %d: %s", resp.StatusCode, truncateBodyFor(raw, 512)),
		}
	}
	if !looksLikeJSON(raw) {
		return nil, Permanent(errors.New("searxng search: " + searxngJSONHint))
	}

	var decoded searxngResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, Permanent(fmt.Errorf("searxng search: decode: %w", err))
	}
	// SearXNG paginates rather than taking a count, so the cap is
	// applied here.
	results := decoded.Results
	if req.NumResults > 0 && len(results) > req.NumResults {
		results = results[:req.NumResults]
	}
	out := make([]SearchResult, 0, len(results))
	for _, r := range results {
		out = append(out, SearchResult{
			Title:         r.Title,
			URL:           r.URL,
			PublishedDate: r.PublishedDate,
			Text:          r.Content,
			Score:         r.Score,
			Engine:        r.Engine,
		})
	}
	return out, nil
}

// looksLikeJSON checks the first non-space byte. Content-Type is not
// trustworthy here: a reverse proxy in front of SearXNG will happily
// return an HTML error page labelled application/json.
func looksLikeJSON(body []byte) bool {
	trimmed := strings.TrimLeft(string(body), " \t\r\n")
	return strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")
}

type searxngResponse struct {
	Results []searxngResult `json:"results"`
}

type searxngResult struct {
	Title         string  `json:"title"`
	URL           string  `json:"url"`
	Content       string  `json:"content"`
	Engine        string  `json:"engine"`
	Score         float64 `json:"score"`
	PublishedDate string  `json:"publishedDate"`
}
