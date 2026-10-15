package job104

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"regexp"
	"strings"

	http "github.com/bogdanfinn/fhttp"
	"github.com/bogdanfinn/fhttp/cookiejar"
	tlsclient "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
	"golang.org/x/net/publicsuffix"
)

// Diagnostic deliberately excludes response bodies, cookies, proxy credentials
// and request headers. It can be returned by the authenticated analysis API.
type Diagnostic struct {
	Stage      string `json:"stage"`
	Reason     string `json:"reason"`
	HTTPStatus int    `json:"http_status,omitempty"`
	CFRay      string `json:"cf_ray,omitempty"`
}

type FetchError struct{ Diagnostic Diagnostic }

func (e *FetchError) Error() string {
	return fmt.Sprintf("104 %s: %s (HTTP %d)", e.Diagnostic.Stage, e.Diagnostic.Reason, e.Diagnostic.HTTPStatus)
}
func (e *FetchError) Unwrap() error { return ErrUnavailable }

func newHTTPClient(proxyURL string) (tlsclient.HttpClient, error) {
	// The HTML navigation and following XHR must retain the same first-party
	// cookies. A bare NewHttpClient has no cookie jar by default.
	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		return nil, fmt.Errorf("104 cookie jar initialization failed")
	}
	options := []tlsclient.HttpClientOption{
		tlsclient.WithTimeoutSeconds(12),
		tlsclient.WithClientProfile(profiles.Chrome_131),
		tlsclient.WithRandomTLSExtensionOrder(),
		tlsclient.WithNotFollowRedirects(),
		tlsclient.WithCookieJar(jar),
	}
	if proxyURL != "" {
		u, err := url.Parse(proxyURL)
		if err != nil || u.Hostname() == "" || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5" && u.Scheme != "socks5h") {
			return nil, fmt.Errorf("invalid JOB104_PROXY_URL")
		}
		options = append(options, tlsclient.WithProxyUrl(proxyURL))
	}
	client, err := tlsclient.NewHttpClient(tlsclient.NewNoopLogger(), options...)
	if err != nil {
		return nil, fmt.Errorf("104 transport configuration invalid")
	}
	return client, nil
}

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

func request104(ctx context.Context, rawURL string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	// Preserve the request headers verified against 104. Adding browser client
	// hints/navigation metadata without a real browser session made the same
	// local request receive a challenge during the controlled comparison.
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Accept-Language", "zh-TW,zh;q=0.9")
	req.Header.Set("Referer", "https://www.104.com.tw/jobs/search/")
	req.Header.Set("Accept", "text/html,application/json;q=0.9,*/*;q=0.8")
	if strings.HasPrefix(req.URL.Path, "/job/ajax/content/") {
		req.Header.Set("Accept", "application/json, text/plain, */*")
		req.Header.Set("Referer", "https://www.104.com.tw/job/"+strings.TrimPrefix(req.URL.Path, "/job/ajax/content/")+"/")
	}
	return req, nil
}

var rayPattern = regexp.MustCompile(`^[a-zA-Z0-9-]{1,96}$`)

func httpGetter(client httpDoer) Getter {
	return func(ctx context.Context, rawURL string) ([]byte, error) {
		req, err := request104(ctx, rawURL)
		if err != nil {
			return nil, err
		}
		stage := "html"
		if strings.HasPrefix(req.URL.Path, "/job/ajax/content/") {
			stage = "content_api"
		}
		fail := func(d Diagnostic) ([]byte, error) {
			d.Stage = stage
			slog.Warn("job104 fetch failed", "stage", d.Stage, "reason", d.Reason, "status", d.HTTPStatus, "cf_ray", d.CFRay)
			return nil, &FetchError{Diagnostic: d}
		}
		resp, err := client.Do(req)
		if err != nil {
			reason := "transport_error"
			var networkError net.Error
			if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &networkError) && networkError.Timeout()) {
				reason = "timeout"
			}
			if errors.Is(err, context.Canceled) {
				reason = "canceled"
			}
			return fail(Diagnostic{Reason: reason})
		}
		defer resp.Body.Close()
		d := Diagnostic{HTTPStatus: resp.StatusCode}
		if ray := resp.Header.Get("CF-Ray"); rayPattern.MatchString(ray) {
			d.CFRay = ray
		}
		if strings.EqualFold(resp.Header.Get("CF-Mitigated"), "challenge") {
			d.Reason = "challenge"
			return fail(d)
		}
		if resp.StatusCode != http.StatusOK {
			d.Reason = "http_error"
			switch {
			case resp.StatusCode == 403:
				d.Reason = "forbidden"
			case resp.StatusCode == 404 || resp.StatusCode == 410:
				d.Reason = "not_found"
			case resp.StatusCode == 429:
				d.Reason = "rate_limited"
			case resp.StatusCode >= 300 && resp.StatusCode < 400:
				d.Reason = "redirect"
			}
			return fail(d)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024+1))
		if err != nil {
			d.Reason = "body_read_error"
			return fail(d)
		}
		if len(body) > 2*1024*1024 {
			d.Reason = "body_too_large"
			return fail(d)
		}
		return body, nil
	}
}
