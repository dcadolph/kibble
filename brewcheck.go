package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Fetcher returns the HTTP status code for a URL. It exists so tests can
// stand in for the network.
type Fetcher interface {
	// Status returns the response status code for url.
	Status(url string) (int, error)
}

// FetcherFunc adapts a function to the Fetcher interface.
type FetcherFunc func(url string) (int, error)

// Status calls f.
func (f FetcherFunc) Status(url string) (int, error) {
	return f(url)
}

// defaultFetcher issues a HEAD request with a short timeout.
func defaultFetcher() Fetcher {
	client := &http.Client{Timeout: 10 * time.Second}
	return FetcherFunc(func(url string) (int, error) {
		resp, err := client.Head(url)
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
		return resp.StatusCode, nil
	})
}

// checkBrew verifies that the documented brew formula exists, without
// installing it. A tap target is checked against the tap repository on
// GitHub, and a bare formula against the Homebrew core API. Network trouble
// is a skip, never a failure.
func checkBrew(step InstallStep, fetch Fetcher) Result {
	res := Result{Step: step}
	target := step.Module
	parts := strings.Split(target, "/")
	var urls []string
	switch len(parts) {
	case 1:
		urls = []string{fmt.Sprintf("https://formulae.brew.sh/api/formula/%s.json", target)}
	case 3:
		base := fmt.Sprintf("https://raw.githubusercontent.com/%s/homebrew-%s/HEAD", parts[0], parts[1])
		urls = []string{
			fmt.Sprintf("%s/Formula/%s.rb", base, parts[2]),
			fmt.Sprintf("%s/Casks/%s.rb", base, parts[2]),
			fmt.Sprintf("%s/%s.rb", base, parts[2]),
		}
	default:
		res.Status = StatusSkipped
		res.Detail = "unrecognized brew target"
		return res
	}
	sawNotFound := false
	for _, u := range urls {
		code, err := fetch.Status(u)
		if err != nil {
			continue
		}
		switch {
		case code >= 200 && code < 300:
			res.Status = StatusPass
			res.Detail = "formula exists (install not attempted)"
			return res
		case code == 404:
			sawNotFound = true
		}
	}
	if sawNotFound {
		res.Status = StatusFail
		res.Detail = fmt.Sprintf("formula not found for %q", target)
		return res
	}
	res.Status = StatusSkipped
	res.Detail = "could not verify formula (network)"
	return res
}
