package sandbox

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// TestArgsHardening pins the flags every kibble container runs under. These
// are the boundary a documented command executes inside, and a flag dropped
// by an unrelated edit would not fail any other test: the container would
// still run, the verdicts would still look right, and the isolation would be
// quietly weaker. So the set is asserted rather than assumed.
func TestArgsHardening(t *testing.T) {
	t.Parallel()

	args := Args()
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"--memory=4g",
		"--pids-limit=1024",
		"--security-opt no-new-privileges",
		"--cap-drop ALL",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("hardening lost %q\ngot: %s", want, joined)
		}
	}

	// Capabilities are added back only for what apt needs to install the
	// packages documents depend on. A new entry here is a real decision and
	// should have to be made deliberately, in this list.
	wantCaps := []string{"CHOWN", "DAC_OVERRIDE", "FOWNER", "SETGID", "SETUID"}
	var gotCaps []string
	for i, a := range args {
		if a == "--cap-add" && i+1 < len(args) {
			gotCaps = append(gotCaps, args[i+1])
		}
	}
	slices.Sort(gotCaps)
	slices.Sort(wantCaps)
	if !slices.Equal(wantCaps, gotCaps) {
		t.Errorf("capabilities added back = %v, want exactly %v", gotCaps, wantCaps)
	}
}

// TestMetadataBlackhole checks that every known cloud metadata hostname is
// pointed at an address answering nothing. kibble's own home is a CI runner
// inside exactly the kind of instance these names serve credentials on, and
// the commands it runs come out of a repository it does not control.
func TestMetadataBlackhole(t *testing.T) {
	t.Parallel()

	args := metadataBlackhole()
	for _, host := range cloudMetadataHosts {
		want := host + ":0.0.0.0"
		if !slices.Contains(args, want) {
			t.Errorf("metadata host %q is not blackholed\ngot: %v", host, args)
		}
	}
	if len(args) != len(cloudMetadataHosts)*2 {
		t.Errorf("got %d args for %d hosts, want two each",
			len(args), len(cloudMetadataHosts))
	}
}

// TestBinRespectsOverride checks the drop-in replacement path, since podman
// and its relatives speak docker's command line and a user who set the
// variable expects kibble to use it.
func TestBinRespectsOverride(t *testing.T) {
	t.Setenv("KIBBLE_DOCKER", "podman")
	if got := Bin(); got != "podman" {
		t.Errorf("Bin() = %q, want podman", got)
	}
	t.Setenv("KIBBLE_DOCKER", "")
	if got := Bin(); got != "docker" {
		t.Errorf("Bin() = %q, want the docker default", got)
	}
}

// TestParseDF reads the available column from df -Pk output as the four
// images print it, and refuses output it cannot read.
func TestParseDF(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Out        string
		WantResult int64
		WantShort  bool
		Want       error
	}{{ // Test 0: The Docker Desktop VM at 4 GB free.
		Out: "Filesystem     1024-blocks     Used Available Capacity Mounted on\n" +
			"overlay           61202244 53917240   4143680      93% /\n",
		WantResult: 4143680 * 1024,
	}, { // Test 1: One GiB free reads under the threshold.
		Out: "Filesystem     1024-blocks     Used Available Capacity Mounted on\n" +
			"overlay           61202244 60153668   1048576      99% /\n",
		WantResult: 1 << 30, WantShort: true,
	}, { // Test 2: A header with no data row is refused.
		Out:  "Filesystem     1024-blocks     Used Available Capacity Mounted on\n",
		Want: ErrDF,
	}, { // Test 3: An available column that is not a number is refused.
		Out:  "Filesystem 1024-blocks Used Available Capacity Mounted on\noverlay 1 1 - 9% /\n",
		Want: ErrDF,
	}, { // Test 4: Empty output is refused.
		Out:  "",
		Want: ErrDF,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got, err := parseDF(test.Out)
			if !errors.Is(err, test.Want) {
				t.Fatalf("parseDF error = %v, want %v", err, test.Want)
			}
			if got != test.WantResult {
				t.Errorf("parseDF = %d, want %d", got, test.WantResult)
			}
			if short := test.Want == nil && got < MinFreeBytes; short != test.WantShort {
				t.Errorf("%d free under the %d threshold = %v, want %v",
					got, MinFreeBytes, short, test.WantShort)
			}
		})
	}
}
