package release

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxHealthBody = 64 << 10

type HealthOptions struct {
	Endpoint       string
	BearerToken    string
	ExpectedStatus int
	RequiredMarker string
	Timeout        time.Duration
}

// CheckHealth is intentionally limited to a local updater-owned endpoint.
func CheckHealth(ctx context.Context, options HealthOptions) error {
	u, err := url.Parse(options.Endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" || u.Hostname() == "" {
		return errors.New("health endpoint must be an HTTP(S) loopback URL without credentials, query or fragment")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return errors.New("health endpoint must use a loopback IP address")
	}
	status := options.ExpectedStatus
	if status == 0 {
		status = http.StatusOK
	}
	if status < 100 || status > 599 {
		return errors.New("invalid expected health status")
	}
	if options.Timeout <= 0 || options.Timeout > 30*time.Second {
		options.Timeout = 10 * time.Second
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "text/plain, application/json")
	request.Header.Set("Cache-Control", "no-cache")
	if options.BearerToken != "" {
		if strings.ContainsAny(options.BearerToken, "\r\n") {
			return errors.New("invalid health bearer token")
		}
		request.Header.Set("Authorization", "Bearer "+options.BearerToken)
	}
	client := &http.Client{Timeout: options.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("health request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != status {
		return fmt.Errorf("health returned HTTP %d, want %d", response.StatusCode, status)
	}
	if response.ContentLength > maxHealthBody || response.Header.Get("Content-Encoding") != "" {
		return errors.New("unsupported health response")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxHealthBody+1))
	if err != nil {
		return err
	}
	if len(body) > maxHealthBody {
		return errors.New("health response exceeds size limit")
	}
	if options.RequiredMarker != "" && !strings.Contains(string(body), options.RequiredMarker) {
		return errors.New("health response marker missing")
	}
	return nil
}
