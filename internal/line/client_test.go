package line

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestReplyPreservesLongUnicodeModelText(t *testing.T) {
	text := strings.Repeat("面試😀", 3000)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Messages []struct{ Type, Text string }
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		var combined strings.Builder
		if len(payload.Messages) != 3 {
			t.Errorf("got %d chunks", len(payload.Messages))
		}
		for _, message := range payload.Messages {
			if message.Type != "text" || len(utf16.Encode([]rune(message.Text))) > 5000 {
				t.Error("invalid LINE chunk")
			}
			combined.WriteString(message.Text)
		}
		if combined.String() != text {
			t.Error("model response was changed")
		}
		w.WriteHeader(200)
	}))
	defer server.Close()
	c := New("test-token")
	c.URL = server.URL
	if err := c.Reply(context.Background(), "test-reply-token", text); err != nil {
		t.Fatal(err)
	}
}

func TestPushDestinationAndAuth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("auth missing")
		}
		var payload struct {
			To       string
			Messages []struct{ Type, Text string }
		}
		if json.NewDecoder(r.Body).Decode(&payload) != nil || payload.To != "user-one" || len(payload.Messages) != 1 || payload.Messages[0].Text != "面試答案" {
			t.Error("invalid push payload")
		}
		w.WriteHeader(200)
	}))
	defer server.Close()
	c := New("test-token")
	c.PushURL = server.URL
	if err := c.Push(context.Background(), "user-one", "面試答案"); err != nil {
		t.Fatal(err)
	}
}
