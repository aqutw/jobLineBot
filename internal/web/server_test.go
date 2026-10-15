package web

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"github.com/aqutw/jobLineBot/internal/bot"
	"github.com/aqutw/jobLineBot/internal/job104"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type fetch struct{}

func (fetch) Fetch(context.Context, string) (job104.Document, error) {
	return job104.Document{URL: "https://www.104.com.tw/job/8tv2k/", Title: "Engineer", Text: "valid text"}, nil
}

type model struct{}

func (model) Generate(context.Context, job104.Document, string) (string, string, error) {
	return "測試題目與參考答案", "test", nil
}

type replier struct {
	calls atomic.Int32
	done  chan bool
}

func (r *replier) Reply(context.Context, string, string) error {
	r.calls.Add(1)
	r.done <- true
	return nil
}
func TestAPIAndWebhook(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &replier{done: make(chan bool, 4)}
	s := New(ctx, &bot.Service{Fetcher: fetch{}, Model: model{}}, r, Config{Secret: "secret", LINEEnabled: true, ModelEnabled: true, DemoToken: "api-token"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer func() { cancel(); s.Wait() }()
	h := s.Handler()
	for _, tc := range []struct {
		body, token string
		want        int
	}{{`{"job_id":"8tv2k"}`, "", 401}, {`{"job_id":"8tv2k"}`, "api-token", 200}, {`{"job_id":"8tv2k"}{}`, "api-token", 400}, {`{"url":"https://evil.test/job/8tv2k"}`, "api-token", 400}} {
		req := httptest.NewRequest("POST", "/api/analyze", bytes.NewBufferString(tc.body))
		if tc.token != "" {
			req.Header.Set("Authorization", "Bearer "+tc.token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != tc.want {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	payload := []byte(`{"events":[{"type":"message","webhookEventId":"one","replyToken":"reply","message":{"type":"text","text":"8tv2k"}}]}`)
	mac := hmac.New(sha256.New, []byte("secret"))
	mac.Write(payload)
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	for _, signature := range []string{"bad", sig, sig} {
		req := httptest.NewRequest(http.MethodPost, "/line/webhook", bytes.NewReader(payload))
		req.Header.Set("X-Line-Signature", signature)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		want := 200
		if signature == "bad" {
			want = 403
		}
		if w.Code != want {
			t.Fatal(w.Code)
		}
	}
	select {
	case <-r.done:
	case <-time.After(time.Second):
		t.Fatal("no reply")
	}
	if r.calls.Load() != 1 {
		t.Fatal("duplicate reply")
	}
}
