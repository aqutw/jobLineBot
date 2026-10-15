package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aqutw/jobLineBot/internal/bot"
	"github.com/aqutw/jobLineBot/internal/job104"
)

type snapshotModel func(context.Context, job104.Document, string) (string, string, error)

func (f snapshotModel) Generate(ctx context.Context, doc job104.Document, prefs string) (string, string, error) {
	return f(ctx, doc, prefs)
}

type noFetch struct{}

func (noFetch) Fetch(context.Context, string) (job104.Document, error) {
	panic("snapshot endpoint must never re-fetch 104")
}
func extensionServer(t *testing.T, generator snapshotModel) *Server {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s := New(ctx, &bot.Service{Fetcher: noFetch{}, Model: generator}, nil, Config{ModelEnabled: true}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { cancel(); s.Wait() })
	return s
}
func snapshotPayload() snapshotInput {
	var input snapshotInput
	input.Snapshot.ID = "8tv2k"
	input.Snapshot.URL = "https://www.104.com.tw/job/8tv2k/"
	input.Snapshot.Title = "<b>Engineer</b>"
	input.Snapshot.Company = "Company"
	input.Snapshot.Content = "<script>SECRET_SCRIPT</script><form>PRIVATE_FORM</form><p>" + strings.Repeat("工作內容與條件要求，負責API設計及測試。", 10) + "</p>"
	input.Snapshot.CapturedAt = time.Now().Add(-time.Hour).UTC()
	input.Preferences = "著重API"
	return input
}
func extensionRequest(h http.Handler, body any, origin string) *httptest.ResponseRecorder {
	data, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", "/api/extension/analyze", bytes.NewReader(data))
	r.Header.Set("Content-Type", "application/json")
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestExtensionSnapshotNoCredentialsNoFetch(t *testing.T) {
	calls := 0
	s := extensionServer(t, func(ctx context.Context, doc job104.Document, prefs string) (string, string, error) {
		calls++
		if doc.Title != "Engineer" || doc.Source != "browser_snapshot_go_cleaned" || strings.Contains(doc.Text, "SECRET") || strings.Contains(doc.Text, "PRIVATE") || prefs != "著重API" {
			t.Fatalf("unclean snapshot: %+v %s", doc, prefs)
		}
		return "完整職缺分析與模擬面試題", "private-provider-name", nil
	})
	origin := "chrome-extension://" + strings.Repeat("a", 32)
	w := extensionRequest(s.Handler(), snapshotPayload(), origin)
	if w.Code != 200 || calls != 1 || w.Header().Get("Access-Control-Allow-Origin") != origin || strings.Contains(w.Body.String(), "private-provider") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	req := httptest.NewRequest("OPTIONS", "/api/extension/analyze", nil)
	req.Header.Set("Origin", origin)
	pre := httptest.NewRecorder()
	s.Handler().ServeHTTP(pre, req)
	if pre.Code != 204 || pre.Header().Get("Access-Control-Allow-Headers") != "Content-Type" {
		t.Fatal(pre.Code)
	}
}
func TestExtensionRejectsInvalidAndUntrustedPayloads(t *testing.T) {
	calls := 0
	s := extensionServer(t, func(context.Context, job104.Document, string) (string, string, error) { calls++; return "ok", "", nil })
	h := s.Handler()
	for _, mutate := range []func(*snapshotInput){
		func(p *snapshotInput) { p.Snapshot.URL = "https://evil.test/job/8tv2k/" },
		func(p *snapshotInput) { p.Snapshot.ID = "wrong" },
		func(p *snapshotInput) { p.Snapshot.Content = "empty" },
		func(p *snapshotInput) { p.Snapshot.Content = strings.Repeat("職", 40001) },
		func(p *snapshotInput) { p.Preferences = strings.Repeat("字", 2001) },
		func(p *snapshotInput) { p.Snapshot.CapturedAt = time.Now().Add(time.Hour) },
	} {
		p := snapshotPayload()
		mutate(&p)
		if w := extensionRequest(h, p, ""); w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	p := snapshotPayload()
	if w := extensionRequest(h, p, "https://evil.test"); w.Code != 403 {
		t.Fatal(w.Code)
	}
	data, _ := json.Marshal(p)
	var object map[string]any
	json.Unmarshal(data, &object)
	object["model"] = "arbitrary"
	if w := extensionRequest(h, object, ""); w.Code != 400 {
		t.Fatal(w.Code)
	}
	delete(object, "model")
	object["notes"] = "private"
	if w := extensionRequest(h, object, ""); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if calls != 0 {
		t.Fatal("invalid input reached model")
	}
}
func TestExtensionRateLimitAndUnavailable(t *testing.T) {
	calls := 0
	s := extensionServer(t, func(context.Context, job104.Document, string) (string, string, error) {
		calls++
		return "分析", "", nil
	})
	h := s.Handler()
	for i := 0; i < 7; i++ {
		w := extensionRequest(h, snapshotPayload(), "")
		want := 200
		if i == 6 {
			want = 429
		}
		if w.Code != want {
			t.Fatal(i, w.Code)
		}
	}
	if calls != 6 {
		t.Fatal(calls)
	}
	failed := extensionServer(t, func(context.Context, job104.Document, string) (string, string, error) {
		return "", "", errors.New("private upstream detail")
	})
	if w := extensionRequest(failed.Handler(), snapshotPayload(), ""); w.Code != 503 || strings.Contains(w.Body.String(), "private") {
		t.Fatal(w.Code, w.Body.String())
	}
	failed.Config.ModelEnabled = false
	if w := extensionRequest(failed.Handler(), snapshotPayload(), ""); w.Code != 503 {
		t.Fatal(w.Code)
	}
	busy := extensionServer(t, func(context.Context, job104.Document, string) (string, string, error) {
		t.Fatal("busy request reached model")
		return "", "", nil
	})
	busy.apiSlots <- struct{}{}
	busy.apiSlots <- struct{}{}
	if w := extensionRequest(busy.Handler(), snapshotPayload(), ""); w.Code != 429 {
		t.Fatal(w.Code)
	}
}
