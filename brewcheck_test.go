package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestCheckBrew checks formula verification against a fake fetcher.
func TestCheckBrew(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Target     string
		Taps       []string
		Codes      map[string]int
		Err        error
		WantStatus Status
		WantURL    string
	}{{ // Test 0c: a documented cask install is looked up as a cask, not read
		// as a formula named --cask. Existence is EXISTS, not a pass, since the
		// index says nothing about whether the install works.
		Target: "cask:alacritty",
		Codes: map[string]int{
			"formulae.brew.sh/api/cask/alacritty.json": 200,
		},
		WantStatus: StatusExists,
	}, { // Test 0d: a cask lookup the network drops is a skip, never a failure.
		Target:     "cask:alacritty",
		Err:        errors.New("dial tcp: timeout"),
		WantStatus: StatusSkipped,
	}, { // Test 0e: a tap alias is a working documented name, the same as a
		// core alias.
		Target: "example/tap/short",
		Codes: map[string]int{
			"raw.githubusercontent.com/example/homebrew-tap/HEAD/Aliases/short": 200,
		},
		WantStatus: StatusExists,
	}, { // Test 0a: a core alias is a working documented name. `brew install
		// rich` resolves through Aliases/rich to rich-cli, and calling it
		// broken was a public false positive this case pins against.
		Target: "rich",
		Codes: map[string]int{
			"formulae.brew.sh/api/formula/rich.json":                             404,
			"raw.githubusercontent.com/Homebrew/homebrew-core/HEAD/Aliases/rich": 200,
		},
		WantStatus: StatusExists,
	}, { // Test 0: a tap formula that exists is reported EXISTS.
		Target:     "example/tap/mytool",
		Codes:      map[string]int{"raw.githubusercontent.com/example/homebrew-tap/HEAD/Formula/mytool.rb": 200},
		WantStatus: StatusExists,
		WantURL:    "homebrew-tap",
	}, { // Test 1: a formula at the tap root is found by the fallback path.
		Target: "example/toolbox/mytool",
		Codes: map[string]int{
			"raw.githubusercontent.com/example/homebrew-toolbox/HEAD/Formula/mytool.rb": 404,
			"raw.githubusercontent.com/example/homebrew-toolbox/HEAD/mytool.rb":         200,
		},
		WantStatus: StatusExists,
	}, { // Test 2: a tap name missing from every namespace is broken: a reader
		// running the documented line gets no such formula.
		Target:     "example/tap/nope",
		Codes:      map[string]int{},
		WantStatus: StatusFail,
	}, { // Test 2c: a missing cask with no documented tap is broken.
		Target:     "cask:nope",
		Codes:      map[string]int{},
		WantStatus: StatusFail,
	}, { // Test 2d: a bare formula missing from core but present in a documented
		// tap is EXISTS, not a false failure.
		Target: "widget",
		Taps:   []string{"acme/tools"},
		Codes: map[string]int{
			"raw.githubusercontent.com/acme/homebrew-tools/HEAD/Formula/widget.rb": 200,
		},
		WantStatus: StatusExists,
		WantURL:    "homebrew-tools",
	}, { // Test 2e: a bare formula missing from core and from every documented
		// tap is broken.
		Target:     "widget",
		Taps:       []string{"acme/tools"},
		Codes:      map[string]int{},
		WantStatus: StatusFail,
	}, { // Test 2b: a cask-only tap is found under Casks.
		Target: "example/toolbox/mytool",
		Codes: map[string]int{
			"homebrew-toolbox/HEAD/Casks/mytool.rb": 200,
		},
		WantStatus: StatusExists,
		WantURL:    "Casks",
	}, { // Test 3: a bare formula is checked against the core API.
		Target:     "wget",
		Codes:      map[string]int{"formulae.brew.sh/api/formula/wget.json": 200},
		WantStatus: StatusExists,
		WantURL:    "formulae.brew.sh",
	}, { // Test 4: network trouble is a skip, not a failure.
		Target:     "example/tap/mytool",
		Err:        errors.New("dial timeout"),
		WantStatus: StatusSkipped,
	}, { // Test 5: an unrecognized target shape is a skip.
		Target:     "a/b",
		WantStatus: StatusSkipped,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			var hit []string
			fetch := FetcherFunc(func(url string) (int, error) {
				hit = append(hit, url)
				if test.Err != nil {
					return 0, test.Err
				}
				for frag, code := range test.Codes {
					if strings.Contains(url, frag) {
						return code, nil
					}
				}
				return 404, nil
			})
			got := checkBrew(InstallStep{Repo: "r", Kind: "brew", Module: test.Target, Taps: test.Taps}, fetch)
			if diff := cmp.Diff(test.WantStatus, got.Status); diff != "" {
				t.Errorf("status mismatch (-want +got):\n%s\nurls hit: %v", diff, hit)
			}
			if test.WantURL != "" && !strings.Contains(strings.Join(hit, " "), test.WantURL) {
				t.Errorf("expected a checked url to contain %q, hit: %v", test.WantURL, hit)
			}
		})
	}
}
