package release

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

const MaxFeed = 24 << 10

// FeedResult never authorizes downloading or installing the release artifact.
type FeedResult struct {
	Channel          string `json:"channel"`
	Installed        string `json:"installed"`
	Version          string `json:"version"`
	UpdateAvailable  bool   `json:"update_available"`
	ArtifactVerified bool   `json:"artifact_verified"`
}

type signedFeed struct {
	Metadata  string `json:"metadata"`
	Signature string `json:"signature"`
}

// CheckFeed reads one operator-configured HTTPS endpoint, without redirects,
// cookies, or proxy configuration from the environment. The public key is
// supplied out of band. The network cannot choose an artifact URL.
func CheckFeed(ctx context.Context, endpoint string, key ed25519.PublicKey, p Policy) (FeedResult, error) {
	transport := releaseTransport()
	defer transport.CloseIdleConnections()
	return checkFeed(ctx, endpoint, key, p, transport)
}

func releaseTransport() *http.Transport {
	return &http.Transport{
		DialContext:            (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		TLSHandshakeTimeout:    5 * time.Second,
		ResponseHeaderTimeout:  5 * time.Second,
		MaxResponseHeaderBytes: 16 << 10,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
		DisableCompression:     true,
	}
}

func checkFeed(ctx context.Context, endpoint string, key ed25519.PublicKey, p Policy, transport http.RoundTripper) (FeedResult, error) {
	var result FeedResult
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Opaque != "" || u.ForceQuery || u.RawQuery != "" || u.Fragment != "" {
		return result, errors.New("feed URL must be HTTPS without credentials, query or fragment")
	}
	if _, err := version(p.Installed); err != nil {
		return result, err
	}
	if len(key) != ed25519.PublicKeySize || (p.Channel != "stable" && p.Channel != "security") || p.Platform == "" {
		return result, errors.New("invalid feed key, channel or platform")
	}
	client := &http.Client{Transport: transport, Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return result, errors.New("invalid feed request")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Cache-Control", "no-cache")
	res, err := client.Do(req)
	if err != nil {
		return result, fmt.Errorf("fetch release metadata: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return result, fmt.Errorf("feed returned HTTP %d", res.StatusCode)
	}
	if res.ContentLength > MaxFeed || res.Header.Get("Content-Encoding") != "" {
		return result, errors.New("unsupported feed encoding or size")
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, MaxFeed+1))
	if err != nil {
		return result, fmt.Errorf("read release feed: %w", err)
	}
	if len(body) > MaxFeed {
		return result, errors.New("release feed exceeds size limit")
	}
	var feed signedFeed
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&feed); err != nil || dec.Decode(new(any)) != io.EOF {
		return result, errors.New("invalid release feed JSON")
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(feed.Metadata)
	if err != nil || len(raw) == 0 || len(raw) > MaxMetadata {
		return result, errors.New("invalid feed metadata encoding or size")
	}
	sig, err := base64.StdEncoding.Strict().DecodeString(feed.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return result, errors.New("invalid feed signature encoding or size")
	}
	if p.Now.IsZero() {
		p.Now = time.Now()
	}
	m, err := Verify(raw, sig, key, p)
	if err != nil && !errors.Is(err, ErrNoUpdate) {
		return result, err
	}
	return FeedResult{Channel: m.Channel, Installed: p.Installed, Version: m.Version, UpdateAvailable: err == nil}, nil
}
