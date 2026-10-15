// Package job104 owns all retrieval. LLMs receive only cleaned text, never raw pages.
package job104

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
)

var ErrUnavailable = errors.New("104 職缺目前無法取得，可能已下架或暫時限制存取，請稍後再試")
var idPattern = regexp.MustCompile(`^[a-z0-9]{3,12}$`)

func ParseID(input string) (string, error) {
	input = strings.TrimSpace(input)
	if idPattern.MatchString(input) {
		return input, nil
	}
	if strings.HasPrefix(input, "www.104.com.tw/") || strings.HasPrefix(input, "104.com.tw/") {
		input = "https://" + input
	}
	u, err := url.Parse(input)
	if err != nil || u.Scheme != "https" || (u.Host != "www.104.com.tw" && u.Host != "104.com.tw") || u.User != nil || u.Fragment != "" {
		return "", fmt.Errorf("請提供 104 職缺網址或 job_id，例如 8tv2k")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 || parts[0] != "job" || !idPattern.MatchString(parts[1]) {
		return "", fmt.Errorf("無效的 104 職缺網址")
	}
	return parts[1], nil
}

type Document struct {
	ID        string    `json:"job_id"`
	URL       string    `json:"source_url"`
	Title     string    `json:"title"`
	Company   string    `json:"company"`
	Text      string    `json:"cleaned_text"`
	Source    string    `json:"extraction"`
	FetchedAt time.Time `json:"fetched_at"`
	Truncated bool      `json:"truncated"`
}
type Getter func(context.Context, string) ([]byte, error)
type cached struct {
	doc     Document
	expires time.Time
}
type Fetcher struct {
	Get   Getter
	mu    sync.Mutex
	cache map[string]cached
	next  time.Time
}

func New() (*Fetcher, error) { return NewWithProxy("") }

// NewWithProxy optionally routes only 104 retrieval through a deployment-owned proxy.
// LLM and LINE traffic keep their normal direct transports.
func NewWithProxy(proxyURL string) (*Fetcher, error) {
	client, err := newHTTPClient(proxyURL)
	if err != nil {
		return nil, err
	}
	return &Fetcher{cache: make(map[string]cached), Get: httpGetter(client)}, nil
}
func (f *Fetcher) load(ctx context.Context, u string) ([]byte, error) {
	// Reserve request slots across concurrent HTTP and LINE requests.
	f.mu.Lock()
	at := time.Now()
	if f.next.After(at) {
		at = f.next
	}
	f.next = at.Add(time.Second)
	f.mu.Unlock()
	timer := time.NewTimer(time.Until(at))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
	}
	return f.Get(ctx, u)
}
func (f *Fetcher) Fetch(ctx context.Context, id string) (Document, error) {
	if !idPattern.MatchString(id) {
		return Document{}, fmt.Errorf("invalid job ID")
	}
	f.mu.Lock()
	item, ok := f.cache[id]
	f.mu.Unlock()
	if ok && time.Now().Before(item.expires) {
		return item.doc, nil
	}
	doc := Document{ID: id, URL: "https://www.104.com.tw/job/" + id + "/", FetchedAt: time.Now().UTC()}
	body, err := f.load(ctx, doc.URL)
	if err == nil {
		doc.Title, doc.Company, doc.Text = extractHTML(string(body))
		doc.Source = "html"
	}
	if len([]rune(doc.Text)) < 80 || doc.Title == "" {
		body, err = f.load(ctx, "https://www.104.com.tw/job/ajax/content/"+id)
		if err != nil {
			return Document{}, err
		}
		doc.Title, doc.Company, doc.Text = extractAPI(body)
		doc.Source = "html+104_content_api"
	}
	if doc.Title == "" || len([]rune(doc.Text)) < 80 {
		slog.Warn("job104 content missing", "job_id", id, "bytes", len(body))
		return Document{}, &FetchError{Diagnostic: Diagnostic{Stage: "content_api", Reason: "invalid_content", HTTPStatus: 200}}
	}
	runes := []rune(doc.Text)
	if len(runes) > 30000 {
		doc.Text = string(runes[:30000])
		doc.Truncated = true
	}
	f.mu.Lock()
	if f.cache == nil {
		f.cache = make(map[string]cached)
	}
	if len(f.cache) >= 128 {
		for k := range f.cache {
			delete(f.cache, k)
			break
		}
	}
	f.cache[id] = cached{doc, time.Now().Add(10 * time.Minute)}
	f.mu.Unlock()
	return doc, nil
}

// Clean parses malformed HTML, drops active/navigation content and normalizes space.
func Clean(input string) string {
	root, err := html.Parse(strings.NewReader(input))
	if err != nil {
		return ""
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "noscript", "iframe", "svg", "nav", "header", "footer", "form", "button", "input":
				return
			}
			for _, a := range n.Attr {
				if a.Key == "hidden" || (a.Key == "aria-hidden" && a.Val == "true") {
					return
				}
			}
			switch n.Data {
			case "p", "div", "section", "article", "li", "br", "h1", "h2", "h3", "tr":
				b.WriteByte('\n')
			}
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
			b.WriteByte(' ')
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == html.ElementNode {
			switch n.Data {
			case "p", "div", "section", "article", "li", "h1", "h2", "h3", "tr":
				b.WriteByte('\n')
			}
		}
	}
	walk(root)
	var lines []string
	for _, line := range strings.Split(b.String(), "\n") {
		line = strings.Join(strings.Fields(line), " ")
		if line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}
func extractHTML(raw string) (title, company, text string) {
	root, err := html.Parse(strings.NewReader(raw))
	if err != nil {
		return
	}
	var main *html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if n.Data == "main" {
				main = n
			}
			if n.Data == "script" {
				for _, a := range n.Attr {
					if a.Key == "type" && a.Val == "application/ld+json" && n.FirstChild != nil {
						var v any
						if json.Unmarshal([]byte(n.FirstChild.Data), &v) == nil {
							t, c, d := findPosting(v)
							if d != "" {
								title, company, text = t, c, d
							}
						}
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	if text == "" && main != nil {
		var b strings.Builder
		html.Render(&b, main)
		text = Clean(b.String())
		var find func(*html.Node)
		find = func(n *html.Node) {
			if n.Type == html.ElementNode && n.Data == "h1" {
				var h strings.Builder
				html.Render(&h, n)
				title = Clean(h.String())
			}
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				find(c)
			}
		}
		find(main)
	}
	return
}
func findPosting(v any) (string, string, string) {
	switch n := v.(type) {
	case []any:
		for _, x := range n {
			t, c, d := findPosting(x)
			if d != "" {
				return t, c, d
			}
		}
	case map[string]any:
		types, _ := n["@type"].(string)
		isJob := types == "JobPosting"
		if a, ok := n["@type"].([]any); ok {
			for _, x := range a {
				if x == "JobPosting" {
					isJob = true
				}
			}
		}
		if isJob {
			t, _ := n["title"].(string)
			d, _ := n["description"].(string)
			c := ""
			if org, ok := n["hiringOrganization"].(map[string]any); ok {
				c, _ = org["name"].(string)
			}
			return Clean(t), Clean(c), Clean(d)
		}
		if graph, ok := n["@graph"]; ok {
			return findPosting(graph)
		}
	}
	return "", "", ""
}
func extractAPI(body []byte) (title, company, text string) {
	var payload struct {
		Data struct {
			Header    map[string]any `json:"header"`
			Detail    map[string]any `json:"jobDetail"`
			Condition map[string]any `json:"condition"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return
	}
	h, d, c := payload.Data.Header, payload.Data.Detail, payload.Data.Condition
	title = Clean(str(h["jobName"]))
	company = Clean(str(h["custName"]))
	description := Clean(str(d["jobDescription"]))
	if len([]rune(description)) < 50 {
		return "", "", ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "職缺：%s\n公司：%s\n工作內容：\n%s\n", title, company, description)
	for _, row := range []struct {
		label string
		value any
	}{
		{"工作待遇", d["salary"]}, {"地區", d["addressRegion"]}, {"經驗", c["workExp"]}, {"學歷", c["edu"]}, {"科系", c["major"]}, {"語言", c["language"]}, {"擅長工具", c["specialty"]}, {"技能", c["skill"]}, {"其他條件", c["other"]},
	} {
		if value := Clean(str(row.value)); value != "" {
			fmt.Fprintf(&b, "%s：%s\n", row.label, value)
		}
	}
	return title, company, strings.TrimSpace(b.String())
}
func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		var a []string
		for _, v := range x {
			if s := str(v); s != "" {
				a = append(a, s)
			}
		}
		return strings.Join(a, "、")
	case map[string]any:
		var a []string
		for _, key := range []string{"language", "ability", "description", "name", "edu", "degree"} {
			if s := str(x[key]); s != "" {
				a = append(a, s)
			}
		}
		return strings.Join(a, " ")
	}
	return ""
}
