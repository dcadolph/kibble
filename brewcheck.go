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
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode, nil
	})
}

// checkBrew confirms that a documented brew formula or cask exists, without
// installing it. Existence is reported as EXISTS, not as a pass, since an
// index entry is not proof that the install works. A name absent from every
// namespace kibble can check, and from every tap the document adds, is a FAIL:
// the reader who runs the documented line gets no such formula. Network
// trouble is never an accusation, only a skip.
func checkBrew(step InstallStep, fetch Fetcher) Result {
	res := Result{Step: step}
	target := step.Module
	// A cask-flagged install is looked up in the cask namespace only, since
	// formulas and casks are separate namespaces that happen to share names.
	if name, ok := strings.CutPrefix(target, "cask:"); ok {
		code, err := fetch.Status(
			fmt.Sprintf("https://formulae.brew.sh/api/cask/%s.json", name))
		switch {
		case err != nil:
			// A network blip says nothing about the docs, so it is never
			// an accusation.
			res.Status = StatusSkipped
			res.Reason = ReasonUnreachable
			res.Detail = "cask lookup unreachable"
		case code >= 200 && code < 300:
			res.Status = StatusExists
			res.Detail = "cask exists (install not attempted)"
		case nameInTaps(name, step.Taps, fetch):
			res.Status = StatusExists
			res.Detail = "cask exists in a documented tap (install not attempted)"
		default:
			res.Status = StatusFail
			res.Detail = fmt.Sprintf("no cask named %q in the cask index or any documented tap", name)
		}
		return res
	}
	parts := strings.Split(target, "/")
	var urls []string
	switch len(parts) {
	case 1:
		// A core name can be a formula or an alias of one: `brew install rich`
		// resolves through homebrew-core's Aliases directory to rich-cli, so a
		// name missing from the formula API can still be exactly what a reader
		// should type. Checking only canonical names called a working documented
		// line broken, in public, which is the one mistake this tool must never
		// make.
		urls = []string{
			fmt.Sprintf("https://formulae.brew.sh/api/formula/%s.json", target),
			fmt.Sprintf("https://raw.githubusercontent.com/Homebrew/homebrew-core/HEAD/Aliases/%s", target),
			fmt.Sprintf("https://formulae.brew.sh/api/cask/%s.json", target),
		}
	case 3:
		base := fmt.Sprintf("https://raw.githubusercontent.com/%s/homebrew-%s/HEAD", parts[0], parts[1])
		urls = []string{
			fmt.Sprintf("%s/Formula/%s.rb", base, parts[2]),
			fmt.Sprintf("%s/Casks/%s.rb", base, parts[2]),
			fmt.Sprintf("%s/%s.rb", base, parts[2]),
			// A tap can alias its formulas the same way homebrew-core does,
			// and an alias is a working documented name.
			fmt.Sprintf("%s/Aliases/%s", base, parts[2]),
		}
	default:
		res.Status = StatusSkipped
		res.Reason = ReasonUnrecognizedTarget
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
			res.Status = StatusExists
			res.Detail = "formula exists (install not attempted)"
			return res
		case code == 404:
			sawNotFound = true
		}
	}
	// A bare name absent from core can still live in a tap the document tells
	// the reader to add, so a documented tap is checked before convicting.
	if sawNotFound && len(parts) == 1 && nameInTaps(target, step.Taps, fetch) {
		res.Status = StatusExists
		res.Detail = "formula exists in a documented tap (install not attempted)"
		return res
	}
	if sawNotFound {
		// The name is absent from every namespace checked and from every tap
		// the document adds. A reader running the documented line gets no such
		// formula, so the line is broken.
		res.Status = StatusFail
		res.Detail = fmt.Sprintf(
			"no formula, cask, or alias named %q in the checked namespaces or any documented tap", target)
		return res
	}
	res.Status = StatusSkipped
	res.Reason = ReasonUnreachable
	res.Detail = "could not verify formula (network)"
	return res
}

// nameInTaps reports whether a bare name resolves to a formula, cask, or alias
// in any of the documented taps. Only a definitive 2xx counts, so a network
// error leaves the name unresolved rather than falsely present.
func nameInTaps(name string, taps []string, fetch Fetcher) bool {
	for _, tap := range taps {
		parts := strings.SplitN(tap, "/", 2)
		if len(parts) != 2 {
			continue
		}
		base := fmt.Sprintf("https://raw.githubusercontent.com/%s/homebrew-%s/HEAD", parts[0], parts[1])
		for _, u := range []string{
			fmt.Sprintf("%s/Formula/%s.rb", base, name),
			fmt.Sprintf("%s/Casks/%s.rb", base, name),
			fmt.Sprintf("%s/%s.rb", base, name),
			fmt.Sprintf("%s/Aliases/%s", base, name),
		} {
			if code, err := fetch.Status(u); err == nil && code >= 200 && code < 300 {
				return true
			}
		}
	}
	return false
}
