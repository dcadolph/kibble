package shell

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

// TestInvoked pins which command a wrapped line runs. A value option read as
// a flag makes its value look like the command, and a flag read as a value
// option swallows the command, so each wrapper's value options are exercised.
func TestInvoked(t *testing.T) {
	t.Parallel()

	tests := []struct {
		In        string
		WantWords []string
	}{{ // Test 0: an unwrapped command is returned as it is.
		In: "git log", WantWords: []string{"git", "log"},
	}, { // Test 1: xargs runs the command after it.
		In: "xargs git branch -D", WantWords: []string{"git", "branch", "-D"},
	}, { // Test 2: xargs -I takes the next word as its replacement string.
		In: "xargs -I {} git branch -D {}", WantWords: []string{"git", "branch", "-D", "{}"},
	}, { // Test 3: an attached value takes nothing from the next word.
		In: "xargs -n1 git log", WantWords: []string{"git", "log"},
	}, { // Test 4: several value options in a row.
		In: "xargs -n 1 -P 4 git log", WantWords: []string{"git", "log"},
	}, { // Test 5: a flag-only option is not given a value.
		In: "xargs -0 git log", WantWords: []string{"git", "log"},
	}, { // Test 6: xargs -i takes its value attached or not at all.
		In: "xargs -i git log", WantWords: []string{"git", "log"},
	}, { // Test 7: sudo -u consumes the user, so git here is a user name.
		In: "sudo -u git ls -la", WantWords: []string{"ls", "-la"},
	}, { // Test 8: sudo -E is a flag.
		In: "sudo -E git status", WantWords: []string{"git", "status"},
	}, { // Test 9: sudo accepts assignments before the command.
		In: "sudo FOO=bar git status", WantWords: []string{"git", "status"},
	}, { // Test 10: sudo -l lists permissions rather than running anything.
		In: "sudo -l git", WantWords: nil,
	}, { // Test 11: env assignments are stepped over.
		In: "env K=V git log", WantWords: []string{"git", "log"},
	}, { // Test 12: env -u takes the variable to unset.
		In: "env -u HOME git log", WantWords: []string{"git", "log"},
	}, { // Test 13: env -S carries the command inside one string.
		In: `env -S "git log"`, WantWords: nil,
	}, { // Test 14: env -i is a flag, followed by an assignment.
		In: "env -i PATH=/bin git log", WantWords: []string{"git", "log"},
	}, { // Test 15: command runs what follows it.
		In: "command git log", WantWords: []string{"git", "log"},
	}, { // Test 16: command -v reports on a command rather than running it.
		In: "command -v git", WantWords: nil,
	}, { // Test 17: nohup takes no options.
		In: "nohup git log", WantWords: []string{"git", "log"},
	}, { // Test 18: timeout reads a duration before the command.
		In: "timeout 5 git log", WantWords: []string{"git", "log"},
	}, { // Test 19: timeout -s takes a signal before the duration.
		In: "timeout -s KILL 5 git log", WantWords: []string{"git", "log"},
	}, { // Test 20: an attached long value takes nothing from the next word.
		In: "timeout --signal=KILL 5 git log", WantWords: []string{"git", "log"},
	}, { // Test 21: nice -n takes the adjustment.
		In: "nice -n 10 git log", WantWords: []string{"git", "log"},
	}, { // Test 22: wrappers nest.
		In: "sudo env K=V git log", WantWords: []string{"git", "log"},
	}, { // Test 23: xargs with no command names nothing kibble can see.
		In: "xargs", WantWords: nil,
	}, { // Test 24: a double dash ends the wrapper's options.
		In: "sudo -- git log", WantWords: []string{"git", "log"},
	}, { // Test 25: a command inside a quoted string is not stepped into.
		In: `sh -c "git log"`, WantWords: []string{"sh", "-c", "git log"},
	}}

	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			line, ok := Parse(test.In)
			if !ok || len(line.Cmds) == 0 {
				t.Fatalf("Parse(%q) did not yield a command", test.In)
			}
			got := Invoked(line.Cmds[0])
			if diff := cmp.Diff(test.WantWords, got.Words, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("words mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
