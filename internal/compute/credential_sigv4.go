package compute

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Per-request signing, for providers that authenticate the request
// rather than the caller.
//
// Bedrock is the case R22 names. A static key cannot express it: the
// signature covers the method, the path, the query, a chosen set of
// headers AND a hash of the body, so it can only be computed once the
// request exists — which is exactly why Credential mutates a
// *http.Request rather than returning a string.
//
// WRITTEN OUT RATHER THAN IMPORTED. The AWS SDK brings a large
// dependency tree for one function, and this is a published, frozen
// algorithm — the conditions under which writing it yourself is
// reasonable rather than reckless.
//
// THE TESTS DO NOT ASSERT AWS'S PUBLISHED SIGNATURE CONSTANTS. Writing
// one down from memory risks a wrong expected value that then gets
// "fixed" to match the implementation, which is worse than no test:
// it looks verified and proves nothing. What they check instead is
// that every component the specification covers actually changes the
// signature, that the header block is canonicalised as described, and
// that the body survives being hashed.
//
// So INTEROP WITH A REAL AWS ENDPOINT IS UNPROVEN HERE. It should be
// confirmed against a live Bedrock call, or against the published
// vectors copied in verbatim from the documentation, before this is
// relied on.

const (
	sigV4Algorithm  = "AWS4-HMAC-SHA256"
	sigV4Terminator = "aws4_request"
	// amzDateFormat is ISO8601 basic. AWS rejects anything else.
	amzDateFormat   = "20060102T150405Z"
	shortDateFormat = "20060102"
)

// SigV4Credential signs each request with AWS Signature Version 4.
type SigV4Credential struct {
	AccessKeyID     string
	SecretAccessKey string
	// SessionToken is set for temporary credentials (STS, instance
	// roles). Empty for long-lived keys.
	SessionToken string
	Region       string
	Service      string

	// Now is injectable because a signature covers the timestamp:
	// without a fixed clock no two runs could be compared.
	Now func() time.Time
}

// NewSigV4Credential builds a signer for one region and service.
func NewSigV4Credential(accessKeyID, secretAccessKey, region, service string) *SigV4Credential {
	return &SigV4Credential{
		AccessKeyID:     accessKeyID,
		SecretAccessKey: secretAccessKey,
		Region:          region,
		Service:         service,
	}
}

// Apply signs req in place.
func (c *SigV4Credential) Apply(_ context.Context, req *http.Request) error {
	if c == nil {
		return errors.New("sigv4: no credential")
	}
	if c.AccessKeyID == "" || c.SecretAccessKey == "" {
		return errors.New("sigv4: access key id and secret access key are both required")
	}
	if c.Region == "" || c.Service == "" {
		return errors.New("sigv4: region and service are both required")
	}

	body, err := requestBody(req)
	if err != nil {
		return fmt.Errorf("sigv4: read body: %w", err)
	}
	payloadHash := hexSHA256(body)

	now := time.Now().UTC()
	if c.Now != nil {
		now = c.Now().UTC()
	}
	amzDate := now.Format(amzDateFormat)
	shortDate := now.Format(shortDateFormat)

	// Host is signed but lives on the URL rather than in Header, so it
	// has to be put there before the header set is canonicalised — a
	// signature over a header the server sees differently is a
	// signature that does not verify.
	if req.Host == "" {
		req.Host = req.URL.Host
	}
	req.Header.Set("Host", req.Host)
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if c.SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", c.SessionToken)
	}

	signedHeaders, canonicalHeaders := canonicalHeaders(req)
	canonicalRequest := strings.Join([]string{
		req.Method,
		canonicalURI(req),
		canonicalQuery(req),
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	scope := strings.Join([]string{shortDate, c.Region, c.Service, sigV4Terminator}, "/")
	stringToSign := strings.Join([]string{
		sigV4Algorithm,
		amzDate,
		scope,
		hexSHA256([]byte(canonicalRequest)),
	}, "\n")

	signature := hex.EncodeToString(hmacSHA256(c.signingKey(shortDate), []byte(stringToSign)))

	req.Header.Set("Authorization", fmt.Sprintf("%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		sigV4Algorithm, c.AccessKeyID, scope, signedHeaders, signature))
	return nil
}

// signingKey derives the date/region/service-scoped key.
//
// The chain is the reason a leaked signature does not leak the secret:
// each step is one-way, and the result is only good for one service in
// one region on one day.
func (c *SigV4Credential) signingKey(shortDate string) []byte {
	k := hmacSHA256([]byte("AWS4"+c.SecretAccessKey), []byte(shortDate))
	k = hmacSHA256(k, []byte(c.Region))
	k = hmacSHA256(k, []byte(c.Service))
	return hmacSHA256(k, []byte(sigV4Terminator))
}

// requestBody reads the body for hashing and puts it back.
//
// Via GetBody where possible, which http.NewRequest populates for the
// byte-backed readers every driver here uses. A body consumed and not
// restored would be signed correctly and then sent empty.
func requestBody(req *http.Request) ([]byte, error) {
	if req.Body == nil {
		return nil, nil
	}
	if req.GetBody != nil {
		rc, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		defer func() { _ = rc.Close() }()
		return io.ReadAll(rc)
	}
	// No GetBody: read it and hand back a fresh reader, or the request
	// goes out with nothing in it.
	b, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	_ = req.Body.Close()
	req.Body = io.NopCloser(strings.NewReader(string(b)))
	return b, nil
}

// canonicalHeaders returns the signed-header list and the canonical
// block, both lowercase and sorted as the specification requires.
func canonicalHeaders(req *http.Request) (signed, canonical string) {
	names := make([]string, 0, len(req.Header))
	values := make(map[string]string, len(req.Header))
	for name, vs := range req.Header {
		lower := strings.ToLower(name)
		names = append(names, lower)
		// Values are joined with commas and internally whitespace-
		// collapsed. A header the client sends with two spaces and the
		// server normalises to one would otherwise not verify.
		joined := make([]string, len(vs))
		for i, v := range vs {
			joined[i] = strings.Join(strings.Fields(v), " ")
		}
		values[lower] = strings.Join(joined, ",")
	}
	sort.Strings(names)

	var b strings.Builder
	for _, n := range names {
		b.WriteString(n)
		b.WriteString(":")
		b.WriteString(values[n])
		b.WriteString("\n")
	}
	return strings.Join(names, ";"), b.String()
}

// canonicalURI is the path, empty becoming "/".
func canonicalURI(req *http.Request) string {
	p := req.URL.EscapedPath()
	if p == "" {
		return "/"
	}
	return p
}

// canonicalQuery sorts parameters by name, then by value.
func canonicalQuery(req *http.Request) string {
	// url.Values.Encode already sorts by key and escapes as AWS wants.
	return req.URL.Query().Encode()
}

func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

func hexSHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
