package web

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/aqutw/jobLineBot/internal/bot"
	"github.com/aqutw/jobLineBot/internal/line"
)

type Replier interface {
	Reply(context.Context, string, string) error
	Push(context.Context, string, string) error
}
type Config struct {
	Secret       string
	LINEEnabled  bool
	ModelEnabled bool
	APIToken     string
}
type job struct {
	event line.Event
}
type Server struct {
	Bot       *bot.Service
	LINE      Replier
	Config    Config
	Log       *slog.Logger
	ctx       context.Context
	queue     chan job
	mu        sync.Mutex
	seen      map[string]time.Time
	wg        sync.WaitGroup
	apiSlots  chan struct{}
	extTokens float64
	extRefill time.Time
}

func New(ctx context.Context, service *bot.Service, replier Replier, cfg Config, logger *slog.Logger) *Server {
	s := &Server{Bot: service, LINE: replier, Config: cfg, Log: logger, ctx: ctx, queue: make(chan job, 16), seen: make(map[string]time.Time), apiSlots: make(chan struct{}, 2), extTokens: 6, extRefill: time.Now()}
	s.wg.Add(1)
	go s.run()
	return s
}
func (s *Server) Wait() { s.wg.Wait() }
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("POST /line/webhook", s.webhook)
	mux.HandleFunc("POST /api/analyze", s.query)
	mux.HandleFunc("POST /api/extension/analyze", s.extensionQuery)
	mux.HandleFunc("OPTIONS /api/extension/analyze", s.extensionQuery)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, homepage)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'unsafe-inline'; script-src 'self'; frame-ancestors 'none'; base-uri 'none'")
		mux.ServeHTTP(w, r)
	})
}
func respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	respond(w, 200, map[string]any{"status": "ok", "line_configured": s.Config.LINEEnabled,
		"openrouter_configured": s.Config.ModelEnabled, "extension_analysis": s.Config.ModelEnabled, "extension_sites": []string{"104", "cake", "yourator", "1111", "yes123", "518"}, "response_mode": "go_fetch_clean_then_llm"})
}
func eventID(e line.Event) string {
	if e.ID != "" {
		return e.ID
	}
	h := sha256.Sum256([]byte(e.ReplyToken))
	return hex.EncodeToString(h[:])
}
func (s *Server) webhook(w http.ResponseWriter, r *http.Request) {
	if s.Config.Secret == "" {
		respond(w, 503, map[string]string{"error": "line_not_configured"})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024*1024))
	if err != nil {
		respond(w, 413, map[string]string{"error": "body_too_large"})
		return
	}
	if !line.VerifySignature(body, r.Header.Get("X-Line-Signature"), s.Config.Secret) {
		respond(w, 403, map[string]string{"error": "invalid_signature"})
		return
	}
	var payload struct {
		Events []line.Event `json:"events"`
	}
	if json.Unmarshal(body, &payload) != nil {
		respond(w, 400, map[string]string{"error": "invalid_json"})
		return
	}
	if len(payload.Events) > 100 {
		respond(w, 400, map[string]string{"error": "too_many_events"})
		return
	}
	if len(payload.Events) == 0 {
		respond(w, 200, map[string]bool{"ok": true})
		return
	}
	if !s.Config.LINEEnabled {
		respond(w, 503, map[string]string{"error": "line_not_configured"})
		return
	}
	s.mu.Lock()
	now := time.Now()
	for key, at := range s.seen {
		if now.Sub(at) > 20*time.Minute {
			delete(s.seen, key)
		}
	}
	batch := make(map[string]bool)
	var jobs []job
	for _, e := range payload.Events {
		if e.Type != "message" || e.Message.Type != "text" || e.ReplyToken == "" {
			continue
		}
		key := eventID(e)
		if _, ok := s.seen[key]; ok {
			continue
		}
		if batch[key] {
			continue
		}
		batch[key] = true
		jobs = append(jobs, job{event: e})
	}
	if len(s.seen)+len(jobs) > 2048 || len(s.queue)+len(jobs) > cap(s.queue) {
		s.mu.Unlock()
		w.Header().Set("Retry-After", "5")
		respond(w, 503, map[string]string{"error": "busy"})
		return
	}
	for _, j := range jobs {
		s.seen[eventID(j.event)] = now
		s.queue <- j
	}
	s.mu.Unlock()
	respond(w, 200, map[string]bool{"ok": true})
}
func (s *Server) answer(ctx context.Context, text string) bot.Answer {
	return s.Bot.Answer(ctx, text, "")
}
func (s *Server) run() {
	defer s.wg.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case j := <-s.queue:
			to := j.event.Source.UserID
			if j.event.Source.RoomID != "" {
				to = j.event.Source.RoomID
			}
			if j.event.Source.GroupID != "" {
				to = j.event.Source.GroupID
			}
			replyCtx, replyCancel := context.WithTimeout(s.ctx, 8*time.Second)
			prompt := "已收到職缺，正在整理內容並產生模擬面試題，完成後會傳送結果。"
			if to == "" {
				prompt = "無法識別回覆對象，請在與機器人的一對一聊天中重試。"
			}
			err := s.LINE.Reply(replyCtx, j.event.ReplyToken, prompt)
			replyCancel()
			if err != nil {
				s.Log.Warn("LINE acknowledgement failed", "error", err.Error())
			}
			if to == "" {
				continue
			}
			ctx, cancel := context.WithTimeout(s.ctx, 150*time.Second)
			answer := s.answer(ctx, j.event.Message.Text)
			cancel()
			pushCtx, pushCancel := context.WithTimeout(s.ctx, 8*time.Second)
			if err := s.LINE.Push(pushCtx, to, answer.Text); err != nil {
				s.Log.Warn("LINE result delivery failed", "error", err.Error())
			}
			pushCancel()
		}
	}
}
func (s *Server) query(w http.ResponseWriter, r *http.Request) {
	if s.Config.APIToken == "" {
		respond(w, 404, map[string]string{"error": "not_found"})
		return
	}
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		respond(w, 401, map[string]string{"error": "unauthorized"})
		return
	}
	provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	a, b := sha256.Sum256([]byte(provided)), sha256.Sum256([]byte(s.Config.APIToken))
	if subtle.ConstantTimeCompare(a[:], b[:]) != 1 {
		respond(w, 401, map[string]string{"error": "unauthorized"})
		return
	}
	var input struct {
		Text   string `json:"text"`
		JobID  string `json:"job_id"`
		URL    string `json:"url"`
		Prompt string `json:"prompt"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024))
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil {
		respond(w, 400, map[string]string{"error": "invalid_json"})
		return
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		respond(w, 400, map[string]string{"error": "invalid_json"})
		return
	}
	if input.Text == "" {
		input.Text = input.JobID
	}
	if input.Text == "" {
		input.Text = input.URL
	}
	if strings.TrimSpace(input.Text) == "" || len([]rune(input.Text)) > 2000 || len([]rune(input.Prompt)) > 2000 {
		respond(w, 400, map[string]string{"error": "job_required_max_2000_characters"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 150*time.Second)
	defer cancel()
	select {
	case s.apiSlots <- struct{}{}:
		defer func() { <-s.apiSlots }()
	default:
		respond(w, 429, map[string]string{"error": "busy"})
		return
	}
	answer := s.Bot.Answer(ctx, input.Text, input.Prompt)
	status := 200
	switch answer.Error {
	case "invalid_job":
		status = 400
	case "job_unavailable":
		status = 502
	case "model_unavailable":
		status = 503
	}
	respond(w, status, answer)
}
