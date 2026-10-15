package web

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

type jobReference struct{ ID, Site, URL string }

var (
	job104Path   = regexp.MustCompile(`^/job/([a-z0-9]{3,12})$`)
	cakePath     = regexp.MustCompile(`^/(?:[a-z]{2}(?:-[A-Za-z]{2})?/)?companies/([A-Za-z0-9_-]{1,100})/jobs/([A-Za-z0-9_-]{1,150})$`)
	youratorPath = regexp.MustCompile(`^/companies/([A-Za-z0-9_-]{1,100})/jobs/([0-9]{1,16})$`)
	job1111Path  = regexp.MustCompile(`^/job/([0-9]{1,16})$`)
	job518Path   = regexp.MustCompile(`^/job-([A-Za-z0-9]{1,30})\.html$`)
	yesID        = regexp.MustCompile(`^[0-9_]{1,60}$`)
)

// Only identifies allowlisted job pages; it never performs a network request.
// 104 IDs deliberately remain unchanged for compatibility with saved snapshots.
func parseJobReference(raw string) (jobReference, error) {
	invalid := fmt.Errorf("invalid supported job URL")
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.RawPath != "" {
		return jobReference{}, invalid
	}
	host := strings.TrimPrefix(u.Host, "www.")
	path := strings.TrimSuffix(u.Path, "/")
	switch host {
	case "104.com.tw":
		if m := job104Path.FindStringSubmatch(path); m != nil {
			return jobReference{m[1], "104", "https://www.104.com.tw/job/" + m[1] + "/"}, nil
		}
	case "cake.me":
		if m := cakePath.FindStringSubmatch(path); m != nil {
			return jobReference{"cake:" + m[1] + ":" + m[2], "cake", "https://www.cake.me/companies/" + m[1] + "/jobs/" + m[2]}, nil
		}
	case "yourator.co":
		if m := youratorPath.FindStringSubmatch(path); m != nil {
			return jobReference{"yourator:" + m[1] + ":" + m[2], "yourator", "https://www.yourator.co/companies/" + m[1] + "/jobs/" + m[2]}, nil
		}
	case "1111.com.tw":
		if m := job1111Path.FindStringSubmatch(path); m != nil {
			return jobReference{"1111:" + m[1], "1111", "https://www.1111.com.tw/job/" + m[1]}, nil
		}
	case "yes123.com.tw":
		q, err := url.ParseQuery(u.RawQuery)
		if err != nil {
			break
		}
		a, b := q["p_id"], q["job_id"]
		if path == "/wk_index/job.asp" && len(a) == 1 && len(b) == 1 && yesID.MatchString(a[0]) && yesID.MatchString(b[0]) {
			return jobReference{"yes123:" + a[0] + ":" + b[0], "yes123", "https://www.yes123.com.tw/wk_index/job.asp?p_id=" + a[0] + "&job_id=" + b[0]}, nil
		}
	case "518.com.tw":
		if m := job518Path.FindStringSubmatch(path); m != nil {
			return jobReference{"518:" + m[1], "518", "https://www.518.com.tw/job-" + m[1] + ".html"}, nil
		}
	}
	return jobReference{}, invalid
}
