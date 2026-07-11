package main

import (
	"fmt"
	"testing"
)

// TestAnyFail checks the exit-code logic, including strict mode.
func TestAnyFail(t *testing.T) {
	t.Parallel()
	res := func(s Status) Result { return Result{Status: s} }
	tests := []struct {
		Results []Result
		Strict  bool
		Want    bool
	}{{ // Test 0: all pass, not strict.
		Results: []Result{res(StatusPass), res(StatusSkipped)}, Want: false,
	}, { // Test 1: a build failure always fails.
		Results: []Result{res(StatusPass), res(StatusFail)}, Want: true,
	}, { // Test 2: a timeout does not fail by default.
		Results: []Result{res(StatusTimeout)}, Strict: false, Want: false,
	}, { // Test 3: a timeout fails under strict.
		Results: []Result{res(StatusTimeout)}, Strict: true, Want: true,
	}, { // Test 4: a smoke failure fails under strict.
		Results: []Result{res(StatusPassBuild)}, Strict: true, Want: true,
	}, { // Test 5: doc drift does not fail by default.
		Results: []Result{res(StatusDrift)}, Strict: false, Want: false,
	}, { // Test 6: doc drift fails under strict.
		Results: []Result{res(StatusDrift)}, Strict: true, Want: true,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if got := anyFail(test.Results, test.Strict); got != test.Want {
				t.Errorf("want %v got %v", test.Want, got)
			}
		})
	}
}

// TestHasRunnable checks detection of executable steps.
func TestHasRunnable(t *testing.T) {
	t.Parallel()
	if hasRunnable([]InstallStep{{Run: false}}) {
		t.Errorf("want false when no step is runnable")
	}
	if !hasRunnable([]InstallStep{{Run: false}, {Run: true}}) {
		t.Errorf("want true when a runnable step exists")
	}
}
