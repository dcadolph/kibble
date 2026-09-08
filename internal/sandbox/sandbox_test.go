package sandbox

import (
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
