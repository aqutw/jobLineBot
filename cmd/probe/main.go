// probe exercises the production 104 retrieval path without invoking an LLM
// or sending a LINE message. Run it from each environment being compared.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/aqutw/jobLineBot/internal/job104"
)

func main() {
	input := flag.String("job", "8tv2k", "104 job ID or HTTPS job URL")
	flag.Parse()
	id, err := job104.ParseID(*input)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid 104 job ID or URL")
		os.Exit(2)
	}
	fetcher, err := job104.NewWithProxy(os.Getenv("JOB104_PROXY_URL"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "104 transport configuration invalid")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start := time.Now()
	doc, err := fetcher.Fetch(ctx, id)
	result := map[string]any{"job_id": id, "proxy_configured": os.Getenv("JOB104_PROXY_URL") != "", "elapsed_ms": time.Since(start).Milliseconds()}
	if err != nil {
		result["status"] = "failed"
		var failure *job104.FetchError
		if errors.As(err, &failure) {
			result["diagnostic"] = failure.Diagnostic
		}
		json.NewEncoder(os.Stdout).Encode(result)
		os.Exit(1)
	}
	result["status"] = "ok"
	result["title"] = doc.Title
	result["extraction"] = doc.Source
	result["characters"] = len([]rune(doc.Text))
	json.NewEncoder(os.Stdout).Encode(result)
}
