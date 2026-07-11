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
		Codes      map[string]int
		Err        error
		WantStatus Status
		WantURL    string
	}{{ // Test 0: a tap formula that exists passes.
		Target:     "dcadolph/tap/slop-chop",
		Codes:      map[string]int{"raw.githubusercontent.com/dcadolph/homebrew-tap/HEAD/Formula/slop-chop.rb": 200},
		WantStatus: StatusPass,
		WantURL:    "homebrew-tap",
	}, { // Test 1: a formula at the tap root is found by the fallback path.
		Target: "dcadolph/whodar/whodar",
		Codes: map[string]int{
			"raw.githubusercontent.com/dcadolph/homebrew-whodar/HEAD/Formula/whodar.rb": 404,
			"raw.githubusercontent.com/dcadolph/homebrew-whodar/HEAD/whodar.rb":         200,
		},
		WantStatus: StatusPass,
	}, { // Test 2: a missing tap formula fails.
		Target:     "dcadolph/tap/nope",
		Codes:      map[string]int{},
		WantStatus: StatusFail,
	}, { // Test 2b: a cask-only tap is found under Casks.
		Target: "dcadolph/whodar/whodar",
		Codes: map[string]int{
			"homebrew-whodar/HEAD/Casks/whodar.rb": 200,
		},
		WantStatus: StatusPass,
		WantURL:    "Casks",
	}, { // Test 3: a bare formula is checked against the core API.
		Target:     "wget",
		Codes:      map[string]int{"formulae.brew.sh/api/formula/wget.json": 200},
		WantStatus: StatusPass,
		WantURL:    "formulae.brew.sh",
	}, { // Test 4: network trouble is a skip, not a failure.
		Target:     "dcadolph/tap/slop-chop",
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
			got := checkBrew(InstallStep{Repo: "r", Kind: "brew", Module: test.Target}, fetch)
			if diff := cmp.Diff(test.WantStatus, got.Status); diff != "" {
				t.Errorf("status mismatch (-want +got):\n%s\nurls hit: %v", diff, hit)
			}
			if test.WantURL != "" && !strings.Contains(strings.Join(hit, " "), test.WantURL) {
				t.Errorf("expected a checked url to contain %q, hit: %v", test.WantURL, hit)
			}
		})
	}
}
