package job104

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestParseID(t *testing.T) {
	for _, in := range []string{"8tv2k", "https://www.104.com.tw/job/8tv2k/", "https://www.104.com.tw/job/8tv2k?jobsource=x", "www.104.com.tw/job/8tv2k"} {
		if got, err := ParseID(in); err != nil || got != "8tv2k" {
			t.Fatalf("%s => %s %v", in, got, err)
		}
	}
	for _, in := range []string{"https://evil.example/job/8tv2k", "https://www.104.com.tw.evil.test/job/8tv2k", "http://www.104.com.tw/job/8tv2k", "https://www.104.com.tw:443/job/8tv2k", "https://user@www.104.com.tw/job/8tv2k", "../etc/passwd", "https://www.104.com.tw/job/8tv2k/a", "https://www.104.com.tw/job/%2e%2e"} {
		if _, err := ParseID(in); err == nil {
			t.Fatal("accepted", in)
		}
	}
}
func TestClean(t *testing.T) {
	got := Clean(`<div onclick="steal()"><h2>工作內容</h2><p>Go &amp; SQL<br>API</p><script>secret</script><style>secret</style><nav>secret</nav><iframe>secret</iframe><p hidden>secret</p></div>`)
	if strings.Contains(got, "secret") || strings.Contains(got, "<") || !strings.Contains(got, "Go & SQL\nAPI") {
		t.Fatal(got)
	}
}
func apiFixture() []byte {
	b, _ := json.Marshal(map[string]any{"data": map[string]any{"header": map[string]string{"jobName": "Go Engineer", "custName": "Example"}, "jobDetail": map[string]string{"jobDescription": "<p>" + strings.Repeat("設計Go系統與API服務。", 12) + "</p><script>secret</script>", "salary": "面議"}, "condition": map[string]any{"specialty": []any{map[string]string{"description": "Go"}}}}})
	return b
}
func TestFetchShellFallbackAndCache(t *testing.T) {
	var calls []string
	f := &Fetcher{Get: func(ctx context.Context, u string) ([]byte, error) {
		calls = append(calls, u)
		if strings.Contains(u, "ajax") {
			return apiFixture(), nil
		}
		return []byte(`<html><div id="app"></div></html>`), nil
	}}
	doc, err := f.Fetch(context.Background(), "8tv2k")
	if err != nil || doc.Source != "html+104_content_api" || strings.Contains(doc.Text, "secret") || doc.Title != "Go Engineer" {
		t.Fatalf("%+v %v", doc, err)
	}
	if _, err = f.Fetch(context.Background(), "8tv2k"); err != nil || len(calls) != 2 {
		t.Fatalf("calls %v, error %v", calls, err)
	}
}
func TestJSONLD(t *testing.T) {
	raw := `<script type="application/ld+json">{"@graph":[{"@type":"JobPosting","title":"Engineer","description":"<p>Work &amp; Go</p>","hiringOrganization":{"name":"Example"}}]}</script><nav>irrelevant</nav>`
	title, company, text := extractHTML(raw)
	if title != "Engineer" || company != "Example" || text != "Work & Go" {
		t.Fatal(title, company, text)
	}
}
func TestLiveJob104(t *testing.T) {
	if os.Getenv("LIVE_JOB104") == "" {
		t.Skip("explicit live smoke test")
	}
	f, err := New()
	if err != nil {
		t.Fatal(err)
	}
	doc, err := f.Fetch(context.Background(), "8tv2k")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("title=%s source=%s characters=%d", doc.Title, doc.Source, len([]rune(doc.Text)))
}
