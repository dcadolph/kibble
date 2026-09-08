package main

import (
	"encoding/base64"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dcadolph/kibble/internal/shell"
)

// Rendering a plan as the bash script the container runs. The markers this
// writes are the whole protocol the parent reads results from, and the
// decisions about what may be wrapped in a timeout or moved into its own
// shell live here, because both change what a documented line means.

// redirectDirs returns the parent directories of a line's redirect targets,
// for targets nested under a directory. Docs redirect into standard user
// directories such as ~/.local/share that exist on real systems, so the
// session creates them rather than failing a correct document. Targets with
// expansions beyond a leading ~ are left alone, since their value is unknown.
func redirectDirs(flat string) []string {
	var out []string
	for _, m := range reCreatedToken.FindAllStringSubmatch(flat, -1) {
		for _, tok := range m[1:] {
			if tok == "" || !strings.Contains(tok, "/") {
				continue
			}
			dir := filepath.Dir(tok)
			if strings.ContainsAny(dir, "$`\"'") {
				continue
			}
			if strings.HasPrefix(dir, "~/") {
				dir = `"$HOME"/` + shellSafe(dir[2:])
			} else {
				dir = "'" + shellSafe(dir) + "'"
			}
			out = append(out, dir)
		}
	}
	return out
}

// sessionScript renders the plan as one bash script with markers the parent
// parses. It returns the script and the set of step:line keys that were
// wrapped in a line timeout, so a 124 exit can be read as a hang.
func sessionScript(plan *Plan, installSecs, lineSecs int) (string, map[string]bool) {
	wrapped := map[string]bool{}
	var b strings.Builder
	b.WriteString(`export GOBIN="$(go env GOPATH 2>/dev/null || echo /root/go)/bin"
export PATH="$GOBIN:$HOME/.local/bin:${CARGO_HOME:-$HOME/.cargo}/bin:$PATH"
export EDITOR=true VISUAL=true GIT_EDITOR=true
export GIT_TERMINAL_PROMPT=0
export DEBIAN_FRONTEND=noninteractive
mkdir -p "$GOBIN" /work/repo
tar -xf - -C /work/repo >/dev/null 2>&1 || true
exec </dev/null
cd /work/repo
git init -q >/dev/null 2>&1
git config user.email kibble@localhost >/dev/null 2>&1
git config user.name kibble >/dev/null 2>&1
__pipecode() {
  local last=${!#} i=1 c
  while [ "$i" -lt "$#" ]; do
    c=${!i}
    if [ "$c" -ne 0 ] && [ "$c" -ne 141 ]; then printf '%s' "$c"; return; fi
    i=$((i+1))
  done
  printf '%s' "$last"
}
export -f __pipecode
`)
	if len(plan.Packages) > 0 {
		fmt.Fprintf(&b, `apt-get update -qq >/dev/null 2>&1
apt-get install -y -qq --no-install-recommends %s >/dev/null 2>&1
printf '\nKIBBLE-PKGS CODE=%%d\n' "$?"
`, strings.Join(plan.Packages, " "))
	}
	for _, in := range plan.Installs {
		if in.bootstrap != "" {
			fmt.Fprintf(&b, "%s >/dev/null 2>&1 || true\n", in.bootstrap)
		}
		fmt.Fprintf(&b, `out=$(timeout %d bash -ec '%s' 2>&1); code=$?
printf '\nKIBBLE-BUILD CODE=%%d\n' "$code"
if [ "$code" -ne 0 ]; then printf '%%s\n' "$out" | tail -n 12; printf '\nKIBBLE-ABORT\n'; exit 0; fi
`, installSecs, shellSafe(in.Cmd))
	}
	if len(plan.Binaries) > 0 {
		var quoted []string
		for _, b := range plan.Binaries {
			quoted = append(quoted, "'"+shellSafe(b)+"'")
		}
		fmt.Fprintf(&b, `have=0
for kb in %s; do
  if command -v "$kb" >/dev/null 2>&1; then have=1; printf '\nKIBBLE-HAVE %%s\n' "$kb"; fi
done
if [ "$have" -eq 0 ]; then printf '\nKIBBLE-NOBIN\n'; exit 0; fi
`, strings.Join(quoted, " "))
	}
	for _, f := range plan.Fixtures {
		if dir := filepath.Dir(f.Path); dir != "." {
			fmt.Fprintf(&b, "mkdir -p '%s'\n", shellSafe(dir))
		}
		enc := base64.StdEncoding.EncodeToString([]byte(f.Contents))
		fmt.Fprintf(&b, "printf '%%s' '%s' | base64 -d > '%s'\n", enc, shellSafe(f.Path))
	}
	for _, k := range sortedKeys(plan.Env) {
		fmt.Fprintf(&b, "export %s='%s'\n", k, shellSafe(plan.Env[k]))
	}
	for _, s := range plan.Steps {
		fmt.Fprintf(&b, "printf '"+markerLead+"KIBBLE-STEP %s START\\n'\n", s.ID)
		if s.Background {
			writeBackgroundStep(&b, s)
			continue
		}
		for i, l := range s.Lines {
			if l.Skip != "" {
				fmt.Fprintf(&b, "printf '"+markerLead+"KIBBLE-LINE %s:%d SKIP\\n'\n", s.ID, i)
				continue
			}
			cmd := l.Cmd
			for _, dir := range redirectDirs(flatten(cmd)) {
				fmt.Fprintf(&b, "mkdir -p %s >/dev/null 2>&1 || true\n", dir)
			}
			switch {
			case isSimpleCommand(flatten(cmd)):
				cmd = fmt.Sprintf("timeout %d %s", lineSecs, cmd)
				wrapped[fmt.Sprintf("%s:%d", s.ID, i)] = true
			case isolatable(cmd):
				// The line runs in a shell of its own so timeout has
				// something to kill. Its pipeline status is reduced inside
				// that shell by the same rule the session uses outside, so
				// the exit reported means what an unwrapped line's would.
				cmd = fmt.Sprintf("timeout %d bash -c '%s\n__ps=(\"${PIPESTATUS[@]}\")\n"+
					"exit \"$(__pipecode \"${__ps[@]}\")\"'",
					lineSecs, shellSafe(cmd))
				wrapped[fmt.Sprintf("%s:%d", s.ID, i)] = true
			}
			b.WriteString(cmd + "\n")
			// The pipeline's status is captured before anything else runs, so a
			// producer that fails is not hidden by a consumer that exits zero.
			// A stage killed by SIGPIPE (141) is the benign case of a consumer
			// closing early, such as piping into head, and does not convict.
			b.WriteString("__ps=(\"${PIPESTATUS[@]}\")\n")
			fmt.Fprintf(&b,
				"printf '"+markerLead+"KIBBLE-LINE %s:%d CODE=%%d\\n' \"$(__pipecode \"${__ps[@]}\")\"\n", s.ID, i)
		}
	}
	b.WriteString(`[ -n "${KIBBLE_BG:-}" ] && kill $KIBBLE_BG >/dev/null 2>&1
printf '\nKIBBLE-DONE\n'
`)
	return b.String(), wrapped
}

// writeBackgroundStep renders a background step: its lines run in a subshell
// behind the session, and readiness is a log match when the plan names one.
//
// Each line records its own exit as it finishes. That matters because a
// background block is usually a short setup line or two followed by the one
// command that serves and never returns, and reporting the readiness result
// for all of them claimed evidence for lines nothing had checked: a setup
// line could fail outright while the service still came up, and the step
// reported every line as fine. Only the line still running when readiness is
// judged gets the readiness verdict now, and it is reported as readiness
// rather than as an exit code, because it never produced one.
func writeBackgroundStep(b *strings.Builder, s PlanStep) {
	log := "/tmp/kibble-" + s.ID + ".log"
	status := "/tmp/kibble-" + s.ID + ".status"
	fmt.Fprintf(b, ": > %s\n(\n", status)
	for i, l := range s.Lines {
		if l.Skip != "" {
			continue
		}
		b.WriteString(l.Cmd + "\n")
		fmt.Fprintf(b, "printf 'KIBBLE-BG %s:%d CODE=%%d\\n' \"$?\" >> %s\n", s.ID, i, status)
	}
	fmt.Fprintf(b, ") >%s 2>&1 &\nKIBBLE_BG=\"${KIBBLE_BG:-} $!\"\n", log)
	if s.ReadyLog != "" {
		fmt.Fprintf(b, `ready=1
for i in $(seq 1 30); do grep -q '%s' %s 2>/dev/null && ready=0 && break; sleep 1; done
`, shellSafe(s.ReadyLog), log)
	} else {
		b.WriteString("sleep 2\nready=0\n")
	}
	for i, l := range s.Lines {
		if l.Skip != "" {
			fmt.Fprintf(b, "printf '"+markerLead+"KIBBLE-LINE %s:%d SKIP\\n'\n", s.ID, i)
			continue
		}
		// A line that finished has its own exit. A line with none is the one
		// still running, and readiness is all that is known about it.
		fmt.Fprintf(b, `__c=$(sed -n 's/^KIBBLE-BG %s:%d CODE=//p' %s 2>/dev/null | tail -n1)
if [ -n "$__c" ]; then printf '`+markerLead+`KIBBLE-LINE %s:%d CODE=%%d\n' "$__c"
else printf '`+markerLead+`KIBBLE-LINE %s:%d BG=%%d\n' "$ready"; fi
`, s.ID, i, status, s.ID, i, s.ID, i)
	}
}

// shellSafe escapes single quotes for embedding inside a single-quoted
// shell string.
func shellSafe(s string) string {
	return strings.ReplaceAll(s, "'", `'\''`)
}

// sortedKeys returns a map's keys in stable order.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// isSimpleCommand reports whether a line is one plain command with no shell
// structure, so prefixing it with a timeout does not change what it means.
// Builtins and assignments must run in the session shell unwrapped. The
// judgment comes from a parse rather than a scan for metacharacters, which
// used to refuse `tool --name "a | b"` for a pipe inside a quoted argument.
func isSimpleCommand(flat string) bool {
	line, ok := shell.Parse(flat)
	if !ok {
		return false
	}
	return len(line.Cmds) == 1 && !line.Structured && !line.StateChanging &&
		!line.Background && !line.Heredoc && line.Cmds[0].Name() != ""
}

// isolatable reports whether a line that is not simple can still be run in
// its own shell, which is what lets it carry a timeout. Without this, a line
// with any shell structure had no bound at all: `tool serve | tee log` could
// spend the entire session budget, and every line behind it reported that the
// session ended rather than what it did.
//
// The limits are what a subshell costs. A line that changes the shell cannot
// be isolated, since a cd or an export performed in a subshell is discarded
// when it exits. A heredoc cannot, since rewriting one risks changing the
// data it carries. Nor can a line that expands a variable: an earlier
// documented line may have set it without exporting it, and a subshell would
// see an empty value and fail a document that works. Exporting those
// assignments would fix the visibility and change what the tools receive, so
// the narrower rule is the honest one.
func isolatable(cmd string) bool {
	line, ok := shell.Parse(cmd)
	if !ok {
		return false
	}
	if len(line.Cmds) == 0 || !line.Structured || line.StateChanging ||
		line.Background || line.Heredoc {
		return false
	}
	for _, c := range line.Cmds {
		if c.Expanded {
			return false
		}
	}
	return true
}
