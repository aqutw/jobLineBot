package job104

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	http "github.com/bogdanfinn/fhttp"
)

func TestNavigationSessionCookiesReachContentAPI(t *testing.T) {
	// Exercise the real client over HTTP so this verifies actual Set-Cookie /
	// Cookie handling across navigation and XHR, not just header construction.
	server := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		if r.URL.Path == "/job/8tv2k/" {
			stdhttp.SetCookie(w, &stdhttp.Cookie{Name: "job_session", Value: "navigation-session", Path: "/", HttpOnly: true})
			io.WriteString(w, `<html><div id="app"></div></html>`)
			return
		}
		cookie, err := r.Cookie("job_session")
		if err != nil || cookie.Value != "navigation-session" {
			w.WriteHeader(403)
			return
		}
		if r.Header.Get("Accept") != "application/json, text/plain, */*" || r.Header.Get("Referer") != "https://www.104.com.tw/job/8tv2k/" {
			t.Error("content request lost the verified JSON headers or job referer")
		}
		w.Write(apiFixture())
	}))
	defer server.Close()
	client, err := newHTTPClient("")
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	get := httpGetter(client)
	if _, err := get(context.Background(), server.URL+"/job/8tv2k/"); err != nil {
		t.Fatal(err)
	}
	body, err := get(context.Background(), server.URL+"/job/ajax/content/8tv2k")
	if err != nil {
		t.Fatalf("navigation cookie was not retained: %v", err)
	}
	title, _, _ := extractAPI(body)
	if title != "Go Engineer" {
		t.Fatal("content API response was not usable")
	}
}

func TestChallengeAndForbiddenRemainDistinct(t *testing.T) {
	for _, tc := range []struct {
		name, mitigated, reason string
		status                  int
	}{
		{"cloudflare", "challenge", "challenge", 403},
		{"challenge_with_200", "challenge", "challenge", 200},
		{"forbidden", "", "forbidden", 403},
		{"closed", "", "not_found", 410},
		{"rate_limit", "", "rate_limited", 429},
		{"redirect", "", "redirect", 302},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
				w.Header().Set("CF-Mitigated", tc.mitigated)
				w.Header().Set("CF-Ray", "abc123-SIN")
				w.Header().Set("Set-Cookie", "private_cookie=should-not-be-returned")
				w.Header().Set("Location", "http://127.0.0.1/private")
				w.WriteHeader(tc.status)
				io.WriteString(w, "private response body should-not-be-returned")
			}))
			defer server.Close()
			client, err := newHTTPClient("")
			if err != nil {
				t.Fatal(err)
			}
			defer client.CloseIdleConnections()
			_, err = httpGetter(client)(context.Background(), server.URL+"/job/ajax/content/8tv2k")
			var failure *FetchError
			if !errors.Is(err, ErrUnavailable) || !errors.As(err, &failure) {
				t.Fatalf("not a typed retrieval failure: %v", err)
			}
			if failure.Diagnostic.Reason != tc.reason || failure.Diagnostic.HTTPStatus != tc.status || failure.Diagnostic.Stage != "content_api" {
				t.Fatal(failure.Diagnostic)
			}
			encoded, _ := json.Marshal(failure.Diagnostic)
			if strings.Contains(string(encoded), "should-not-be-returned") || strings.Contains(string(encoded), "127.0.0.1") {
				t.Fatal("diagnostic disclosed response body, cookie or redirect URL")
			}
		})
	}
}

type failingTransport struct{}

func (failingTransport) Do(*http.Request) (*http.Response, error) {
	return nil, errors.New("proxy http://user:secret@example.test rejected request")
}

func TestTransportFailureDoesNotDiscloseProxyCredentials(t *testing.T) {
	_, err := httpGetter(failingTransport{})(context.Background(), "https://www.104.com.tw/job/8tv2k/")
	var failure *FetchError
	if !errors.As(err, &failure) || failure.Diagnostic.Reason != "transport_error" || strings.Contains(err.Error(), "secret") {
		t.Fatal("transport failure did not redact credentials")
	}
}

func TestFetchPreservesChallengeDiagnostic(t *testing.T) {
	f := &Fetcher{Get: func(context.Context, string) ([]byte, error) {
		return nil, &FetchError{Diagnostic{Stage: "content_api", Reason: "challenge", HTTPStatus: 403}}
	}}
	_, err := f.Fetch(context.Background(), "8tv2k")
	var failure *FetchError
	if !errors.As(err, &failure) || failure.Diagnostic.Reason != "challenge" {
		t.Fatal("fetch discarded upstream diagnostic")
	}
}
