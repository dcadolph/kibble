package shell

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestSetupOnly checks which lines only arrange the session. headscale's
// docs passed thirteen such lines and no line of its own tool, and the run
// read as tested.
func TestSetupOnly(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Cmd  string
		Want bool
	}{
		{Cmd: "cd ./headscale", Want: true},                                    // Test 0: a cd.
		{Cmd: `export HEADSCALE_DERP_URLS='[]'`, Want: true},                   // Test 1: an export.
		{Cmd: "mkdir -p ./headscale/{config,lib}", Want: true},                 // Test 2: a mkdir.
		{Cmd: "cat > 'config.yaml' <<'EOF'\nkey: 1\nEOF", Want: true},          // Test 3: a heredoc write.
		{Cmd: "mkdir -p out && cd out", Want: true},                            // Test 4: a list of setup.
		{Cmd: "FOO=bar", Want: true},                                           // Test 5: a bare assignment.
		{Cmd: `echo "x: 1" > config.yaml`, Want: true},                         // Test 6: an echo into a file.
		{Cmd: "headscale users list", Want: false},                             // Test 7: the tool.
		{Cmd: "cd repo && make build", Want: false},                            // Test 8: a cd then work.
		{Cmd: "export VERSION=$(headscale version)", Want: false},              // Test 9: a substitution runs the tool.
		{Cmd: "cat config.yaml", Want: false},                                  // Test 10: a cat that only shows.
		{Cmd: "echo hello | headscale", Want: false},                           // Test 11: a pipe into the tool.
		{Cmd: "cat <<'EOF' | kubectl apply -f -\nkind: Pod\nEOF", Want: false}, // Test 12: a heredoc fed to a tool.
		{Cmd: `if [ -f x ]; then cd x; fi`, Want: false},                       // Test 13: a conditional.
		{Cmd: "source .venv/bin/activate", Want: false},                        // Test 14: source runs a script.
		{Cmd: "cd \"$(", Want: false},                                          // Test 15: unparseable.
	}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if diff := cmp.Diff(test.Want, SetupOnly(test.Cmd)); diff != "" {
				t.Errorf("SetupOnly(%q) mismatch (-want +got):\n%s", test.Cmd, diff)
			}
		})
	}
}
