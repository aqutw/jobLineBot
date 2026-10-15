package web

import (
	"context"
	"github.com/aqutw/jobLineBot/internal/job104"
	"strings"
	"testing"
)

var siteCases = []struct{ site, url, id string }{
	{"104", "https://www.104.com.tw/job/8tv2k/", "8tv2k"},
	{"cake", "https://www.cake.me/companies/devcore/jobs/red-team-specialist", "cake:devcore:red-team-specialist"},
	{"yourator", "https://www.yourator.co/companies/Pacston/jobs/40135", "yourator:Pacston:40135"},
	{"1111", "https://www.1111.com.tw/job/132794802", "1111:132794802"},
	{"yes123", "https://www.yes123.com.tw/wk_index/job.asp?p_id=3290783_04377371&job_id=20230324045012_52509038", "yes123:3290783_04377371:20230324045012_52509038"},
	{"518", "https://www.518.com.tw/job-GaQnDn.html", "518:GaQnDn"},
}

func TestSupportedJobReferences(t *testing.T) {
	for _, tc := range siteCases {
		t.Run(tc.site, func(t *testing.T) {
			ref, err := parseJobReference(tc.url)
			if err != nil || ref.ID != tc.id || ref.Site != tc.site || ref.URL != tc.url {
				t.Fatal(ref, err)
			}
			join := "?"
			if strings.Contains(tc.url, "?") {
				join = "&"
			}
			tracked, err := parseJobReference(tc.url + join + "utm_source=test#ignored")
			if err != nil || tracked != ref {
				t.Fatal(tracked, err)
			}
		})
	}
	for _, u := range []string{"https://www.cake.me.evil.test/companies/a/jobs/b", "https://www.1111.com.tw/corp/132794802", "http://www.518.com.tw/job-GaQnDn.html", "https://name:pw@www.yourator.co/companies/Pacston/jobs/40135", "https://www.yes123.com.tw/wk_index/job.asp?job_id=123", "https://www.yes123.com.tw/wk_index/job.asp?p_id=123&job_id=123&job_id=456", "https://www.cake.me/companies/a/jobs/%22bad", "https://www.518.com.tw:444/job-GaQnDn.html"} {
		if ref, err := parseJobReference(u); err == nil {
			t.Fatal(u, ref)
		}
	}
}
func TestAllSiteSnapshotsUseExistingModelWithoutFetching(t *testing.T) {
	for _, tc := range siteCases {
		t.Run(tc.site, func(t *testing.T) {
			calls := 0
			s := extensionServer(t, func(_ context.Context, doc job104.Document, _ string) (string, string, error) {
				calls++
				if doc.ID != tc.id || doc.URL != tc.url {
					t.Fatal(doc)
				}
				return "職缺分析與模擬面試", "private-model", nil
			})
			p := snapshotPayload()
			p.Snapshot.ID = tc.id
			p.Snapshot.URL = tc.url
			if w := extensionRequest(s.Handler(), p, ""); w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			p.Snapshot.ID = "mismatch"
			if w := extensionRequest(s.Handler(), p, ""); w.Code != 400 {
				t.Fatal(w.Code)
			}
			if calls != 1 {
				t.Fatal(calls)
			}
		})
	}
}
