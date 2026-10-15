package bot

import (
	"context"
	"encoding/json"
	"github.com/aqutw/jobLineBot/internal/job104"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeFetch struct{ err error }

func (f fakeFetch) Fetch(context.Context, string) (job104.Document, error) {
	return job104.Document{Title: "Go Engineer", Text: "clean content"}, f.err
}

type fakeModel struct{ called bool }

func (m *fakeModel) Generate(context.Context, job104.Document, string) (string, string, error) {
	m.called = true
	return "模擬面試題與答案", "test", nil
}
func TestNoModelWithoutDocument(t *testing.T) {
	for _, input := range []string{"8tv2k", "https://evil.example/job/8tv2k"} {
		m := &fakeModel{}
		s := Service{Fetcher: fakeFetch{job104.ErrUnavailable}, Model: m}
		a := s.Answer(context.Background(), input, "")
		if a.Error == "" || m.called {
			t.Fatal(a, m.called)
		}
	}
}
func TestOpenRouterGrounding(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		if b["model"] != "openrouter/free" || b["tools"] != nil || b["plugins"] != nil {
			t.Error(b)
		}
		if !strings.Contains(stringMustJSON(b), "cleaned_text") || r.Header.Get("Authorization") != "Bearer test" {
			t.Error("missing cleaned document or auth")
		}
		w.Write([]byte(`{"model":"provider/model","choices":[{"message":{"content":"模擬題"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()
	c := NewModel("test", "")
	c.URL = srv.URL
	text, model, err := c.Generate(context.Background(), job104.Document{Text: "clean content"}, "")
	if err != nil || text != "模擬題" || model != "provider/model" {
		t.Fatal(text, model, err)
	}
}
func stringMustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
