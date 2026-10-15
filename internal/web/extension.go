package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/aqutw/jobLineBot/internal/job104"
)

var extensionOrigin = regexp.MustCompile(`^chrome-extension://[a-p]{32}$`)

type snapshotInput struct {
	Snapshot struct {
		ID         string    `json:"id"`
		URL        string    `json:"url"`
		Title      string    `json:"title"`
		Company    string    `json:"company"`
		Content    string    `json:"content"`
		CapturedAt time.Time `json:"captured_at"`
	} `json:"snapshot"`
	Preferences string `json:"preferences"`
}

func cleanSnapshotText(value string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, job104.Clean(value)))
}

// A narrow anonymous snapshot API, not a general model proxy. Clients cannot
// choose a model, system prompt, URL to fetch, tools, or upstream credentials.
// Global instance limits: 6 burst, 1/min refill, 2 shared in flight. They do not
// depend on spoofable client headers. Snapshots are not persisted or logged.
func (s *Server) extensionQuery(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); origin != "" {
		w.Header().Set("Vary", "Origin")
		if !extensionOrigin.MatchString(origin) {
			respond(w, 403, map[string]string{"error": "origin_not_allowed"})
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
	}
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		respond(w, 415, map[string]string{"error": "json_required"})
		return
	}
	var input snapshotInput
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256*1024))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil {
		respond(w, 400, map[string]string{"error": "invalid_snapshot"})
		return
	}
	var extra any
	if !errors.Is(decoder.Decode(&extra), io.EOF) {
		respond(w, 400, map[string]string{"error": "invalid_snapshot"})
		return
	}
	snap := input.Snapshot
	reference, err := parseJobReference(snap.URL)
	if err != nil || reference.ID != snap.ID || snap.URL != reference.URL ||
		!utf8.ValidString(snap.Content) || len([]rune(snap.Content)) > 40000 ||
		len([]rune(snap.Title)) > 300 || len([]rune(snap.Company)) > 300 || len([]rune(input.Preferences)) > 2000 ||
		snap.CapturedAt.IsZero() || snap.CapturedAt.After(time.Now().Add(5*time.Minute)) {
		respond(w, 400, map[string]string{"error": "invalid_snapshot"})
		return
	}
	doc := job104.Document{ID: reference.ID, URL: reference.URL, Title: cleanSnapshotText(snap.Title), Company: cleanSnapshotText(snap.Company), Text: cleanSnapshotText(snap.Content), FetchedAt: snap.CapturedAt.UTC(), Source: "browser_snapshot_go_cleaned"}
	if doc.Title == "" || len([]rune(doc.Text)) < 80 {
		respond(w, 400, map[string]string{"error": "snapshot_content_required"})
		return
	}
	if !s.Config.ModelEnabled {
		respond(w, 503, map[string]string{"error": "analysis_unavailable"})
		return
	}
	select {
	case s.apiSlots <- struct{}{}:
		defer func() { <-s.apiSlots }()
	default:
		w.Header().Set("Retry-After", "15")
		respond(w, 429, map[string]string{"error": "busy"})
		return
	}
	s.mu.Lock()
	now := time.Now()
	s.extTokens += now.Sub(s.extRefill).Minutes()
	if s.extTokens > 6 {
		s.extTokens = 6
	}
	s.extRefill = now
	allowed := s.extTokens >= 1
	if allowed {
		s.extTokens--
	}
	s.mu.Unlock()
	if !allowed {
		w.Header().Set("Retry-After", "60")
		respond(w, 429, map[string]string{"error": "busy"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 125*time.Second)
	defer cancel()
	text, _, err := s.Bot.Model.Generate(ctx, doc, cleanSnapshotText(input.Preferences))
	if err != nil || strings.TrimSpace(text) == "" || len([]rune(text)) > 40000 {
		respond(w, 503, map[string]string{"error": "analysis_unavailable"})
		return
	}
	respond(w, 200, map[string]string{"text": strings.TrimSpace(text), "created_at": time.Now().UTC().Format(time.RFC3339)})
}
