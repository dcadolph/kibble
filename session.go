package main

import (
	"encoding/base64"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dcadolph/kibble/internal/docblock"
	kplan "github.com/dcadolph/kibble/internal/plan"
	"github.com/dcadolph/kibble/internal/shell"
)

// Rendering a plan as the bash script the container runs. The markers this
// writes are the whole protocol the parent reads results from, and the
// decisions about what may be wrapped in a timeout or moved into its own
// shell live here, because both change what a documented line means.

// sessionRepo creates the repository a document runs in, in the current
// directory, with the identity a documented commit needs.
const sessionRepo = `git init -q >/dev/null 2>&1
git config user.email kibble@localhost >/dev/null 2>&1
git config user.name kibble >/dev/null 2>&1
`

// documentEnd closes a document's shell. It first stops the background blocks
// the document started, by process group, and waits up to five seconds for
// each group to exit before killing what remains, so a later document that
// serves on the same port finds it free rather than racing a server that is
// still shutting down. A document that ran set -e cannot cut the wait short.
const documentEnd = `for __p in ${KIBBLE_BG:-}; do kill -- -"$__p" >/dev/null 2>&1; done
for __p in ${KIBBLE_BG:-}; do
  wait "$__p" >/dev/null 2>&1 || true
  __w=0
  while kill -0 -- -"$__p" 2>/dev/null && [ "$__w" -lt 50 ]; do sleep 0.1; __w=$((__w+1)); done
  kill -9 -- -"$__p" >/dev/null 2>&1 || true
done
)
`

// binaryStash is where the session keeps a copy of each documented binary as
// the install left it.
const binaryStash = "/tmp/kibble-bin"

// binaryRestore puts each documented binary back as the install left it,
// when a document has replaced it. It runs at every document boundary after
// the first, since lla's getting-started page runs `lla upgrade --version
// v0.5.1` and every later document then tested 0.5.1 instead of the tool the
// install produced. Home-directory state is not restored: no measured case
// needs it, and restoring it would hide a document that depends on another.
const binaryRestore = `for __kp in "${__kbin[@]}"; do
  __ks="` + binaryStash + `/${__kp##*/}"
  cmp -s "$__ks" "$__kp" || cp -p "$__ks" "$__kp" >/dev/null 2>&1 || true
done
`

// treeStash is where the session keeps the working tree as the install and
// the fixtures left it.
const treeStash = "/tmp/kibble-tree.tar"

// treeSave archives the working tree before the first document runs, leaving
// .git to the repository reset. A failed archive is removed, so no document
// boundary restores a partial one.
const treeSave = `tar -cf ` + treeStash + ` --exclude=./.git -C /work/repo . >/dev/null 2>&1 ||
  rm -f ` + treeStash + `
`

// treeRestore puts the working tree back as the install and the fixtures left
// it, at every document boundary after the first, since cspell's first
// document wrote a cache inside the repository that the next one read. The
// tree is cleared in place and never removed, because the parent shell's
// working directory is /work/repo and must stay the same directory.
// Home-directory state is not restored.
const treeRestore = `if [ -f ` + treeStash + ` ]; then
  find /work/repo -mindepth 1 -maxdepth 1 ! -name .git -exec rm -rf {} +
  tar -xf ` + treeStash + ` -C /work/repo >/dev/null 2>&1
fi
`

// gitEditorPath is where the session writes the editor git opens.
const gitEditorPath = "/tmp/kibble-git-editor"

// commitEditor is the editor git opens in the session. A reader asked for a
// commit message types one, and an editor that saves nothing makes git abort
// a documented `git commit` that works for every reader. When the message is
// empty it writes kibble as the subject. Only the part above the scissors line
// that commit -v adds is the message, since the diff below it is discarded.
// Any other file git hands over, such as the patch add -e edits, is left as
// it was, which is all the editor true ever did.
const commitEditor = `#!/bin/sh
case "$1" in
COMMIT_EDITMSG|*/COMMIT_EDITMSG)
  if ! sed '/^# -* >8 -*$/,$d' "$1" | grep -q '^[[:space:]]*[^#[:space:]]'; then
    { printf 'kibble\n'; cat "$1"; } > "$1.kibble" && mv "$1.kibble" "$1"
  fi ;;
esac
`

// redirectDirs returns the parent directories of a line's redirect targets,
// for targets nested under a directory. Docs redirect into standard user
// directories such as ~/.local/share that exist on real systems, so the
// session creates them rather than failing a correct document. Targets with
// expansions beyond a leading ~ are left alone, since their value is unknown.
func redirectDirs(flat string) []string {
	var out []string
	for _, m := range kplan.CreatesToken(flat) {
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
func sessionScript(plan *kplan.Plan, installSecs, lineSecs int) (string, map[string]bool) {
	wrapped := map[string]bool{}
	var b strings.Builder
	// Standard error joins standard output before anything runs. Docker
	// carries the two as separate streams and does not keep their order, so a
	// line's errors could arrive after the marker that closes it. The line
	// then read as silent and settled nothing, and its errors were charged to
	// the line after it: ripgrep's pre-rg line came back GAP on one run and
	// BLOCKED on the next from the same code.
	b.WriteString(`exec 2>&1
export GOBIN="$(go env GOPATH 2>/dev/null || echo /root/go)/bin"
export PATH="$GOBIN:$HOME/.local/bin:${CARGO_HOME:-$HOME/.cargo}/bin:$PATH"
export EDITOR=true VISUAL=true GIT_EDITOR=` + gitEditorPath + `
cat > ` + gitEditorPath + ` <<'KIBBLE-EDITOR'
` + commitEditor + `KIBBLE-EDITOR
chmod +x ` + gitEditorPath + `
export GIT_TERMINAL_PROMPT=0
export DEBIAN_FRONTEND=noninteractive
mkdir -p "$GOBIN" /work/repo
tar -xf - -C /work/repo >/dev/null 2>&1 || true
exec </dev/null
cd /work/repo
` + sessionRepo + `__pipecode() {
  local last=${!#} i=1 c
  for c in "$@"; do
    if [ "$c" -eq 127 ] || [ "$c" -eq 126 ]; then printf '%s' "$c"; return; fi
  done
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
		if in.Bootstrap != "" {
			fmt.Fprintf(&b, "%s >/dev/null 2>&1 || true\n", in.Bootstrap)
		}
		cmd := in.Cmd
		if in.Dir != "" {
			cmd = "cd " + in.Dir + "\n" + cmd
		}
		fmt.Fprintf(&b, `out=$(timeout %d bash -ec '%s' 2>&1); code=$?
printf '\nKIBBLE-BUILD CODE=%%d\n' "$code"
if [ "$code" -ne 0 ]; then printf '%%s\n' "$out" | tail -n 12; printf '\nKIBBLE-ABORT\n'; exit 0; fi
`, installSecs, shellSafe(cmd))
	}
	if len(plan.Installs) > 0 {
		b.WriteString(newShellPath)
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
		// The copy is only of use at a document boundary, so a session with
		// one document is left exactly as it was. Only a regular file on an
		// absolute path is kept. A function's name is not a path, and a link
		// points into a package tree that an upgrade replaces whole, where
		// putting one file back would leave a mix of two versions.
		if documentCount(plan) > 1 {
			fmt.Fprintf(&b, `__kbin=()
mkdir -p %s
for kb in %s; do
  __kp=$(command -v "$kb" 2>/dev/null) || continue
  case "$__kp" in /*) ;; *) continue ;; esac
  [ -f "$__kp" ] && [ ! -L "$__kp" ] || continue
  cp -p "$__kp" "%s/${__kp##*/}" >/dev/null 2>&1 && __kbin+=("$__kp")
done
`, binaryStash, strings.Join(quoted, " "), binaryStash)
		}
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
	// The archive is only of use at a document boundary, so a session with one
	// document is left exactly as it was.
	if documentCount(plan) > 1 {
		b.WriteString(treeSave)
	}
	// Each document runs in a shell of its own, because the planner judges
	// every document as if it started the session. A cd, a variable, a shell
	// option or an exit in one document used to carry into the next, and a
	// document after the first met the branches, remotes and services an
	// earlier one left behind: a second `git remote add origin` exited 3 and a
	// second server found its port taken. The parent shell never leaves the
	// repository, and each document after the first starts with a repository
	// as new as the one the first was given, in a working tree as the install
	// and the fixtures left it.
	doc, opened := "", false
	for _, s := range plan.Steps {
		if !opened || s.Doc != doc {
			if opened {
				b.WriteString(documentEnd + "(\ncd /work/repo\n")
				if len(plan.Binaries) > 0 {
					b.WriteString(binaryRestore)
				}
				b.WriteString(treeRestore + "rm -rf .git\n" + sessionRepo)
			} else {
				b.WriteString("(\ncd /work/repo\n")
			}
			doc, opened = s.Doc, true
		}
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
			for _, dir := range redirectDirs(docblock.Flatten(cmd)) {
				fmt.Fprintf(&b, "mkdir -p %s >/dev/null 2>&1 || true\n", dir)
			}
			simple := isSimpleCommand(docblock.Flatten(cmd))
			prefixed, expanded := assignPrefix(cmd)
			switch {
			case simple && !prefixed:
				cmd = fmt.Sprintf("timeout %d %s", lineSecs, cmd)
				wrapped[fmt.Sprintf("%s:%d", s.ID, i)] = true
			case simple && expanded:
				// timeout cannot run an assignment, and a shell of its own
				// would read an unexported variable empty, so the line runs
				// unbounded in the session as written.
			case simple, isolatable(cmd):
				// The line runs in a shell of its own so timeout has
				// something to kill. Its pipeline status is reduced inside
				// that shell by the same rule the session uses outside, so
				// the exit reported means what an unwrapped line's would.
				cmd = fmt.Sprintf("timeout %d bash -c '%s\n__ps=(\"${PIPESTATUS[@]}\")\n"+
					"exit \"$(__pipecode \"${__ps[@]}\")\"'",
					lineSecs, shellSafe(cmd))
				wrapped[fmt.Sprintf("%s:%d", s.ID, i)] = true
			}
			// Whether a file the line reads exists is asked just before it
			// runs, so a missing file is judged where it can be proven.
			if l.Need != "" {
				fmt.Fprintf(&b, "if %s; then __kn=1; else __kn=0; fi\n", existsTest(l.Need))
				fmt.Fprintf(&b, "printf '"+markerLead+"KIBBLE-NEED %s:%d %%d\\n' \"$__kn\"\n", s.ID, i)
			}
			// The directory is read before the line runs, so the marker names
			// where the line ran rather than where a cd in it went.
			b.WriteString("__cwd=$PWD\n" + cmd + "\n")
			// The pipeline's status is captured before anything else runs, so a
			// producer that fails is not hidden by a consumer that exits zero.
			// A stage killed by SIGPIPE (141) is the benign case of a consumer
			// closing early, such as piping into head, and does not convict.
			b.WriteString("__ps=(\"${PIPESTATUS[@]}\")\n")
			fmt.Fprintf(&b,
				"printf '"+markerLead+"KIBBLE-LINE %s:%d CODE=%%d CWD=%%s\\n' "+
					"\"$(__pipecode \"${__ps[@]}\")\" \"$__cwd\"\n", s.ID, i)
		}
	}
	if opened {
		b.WriteString(documentEnd)
	}
	b.WriteString(`printf '\nKIBBLE-DONE\n'
`)
	return b.String(), wrapped
}

// documentCount returns how many documents the plan's steps come from.
func documentCount(plan *kplan.Plan) int {
	n, doc := 0, ""
	for i, s := range plan.Steps {
		if i == 0 || s.Doc != doc {
			n++
			doc = s.Doc
		}
	}
	return n
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
func writeBackgroundStep(b *strings.Builder, s kplan.PlanStep) {
	log := "/tmp/kibble-" + s.ID + ".log"
	status := "/tmp/kibble-" + s.ID + ".status"
	// Monitor mode puts the block in a process group of its own, which is
	// what lets the document's end stop the server inside it. Killing the
	// block's own pid stopped only the subshell, and the server it was
	// waiting on kept running and kept its port.
	fmt.Fprintf(b, ": > %s\nset -m\n(\n", status)
	for i, l := range s.Lines {
		if l.Skip != "" {
			continue
		}
		b.WriteString(l.Cmd + "\n")
		fmt.Fprintf(b, "printf 'KIBBLE-BG %s:%d CODE=%%d\\n' \"$?\" >> %s\n", s.ID, i, status)
	}
	fmt.Fprintf(b, ") >%s 2>&1 &\nKIBBLE_BG=\"${KIBBLE_BG:-} $!\"\nset +m\n", log)
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

// existsTest returns a shell test for whether path exists. A path under the
// home directory keeps its expansion, and every other character is quoted.
func existsTest(path string) string {
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		return fmt.Sprintf(`[ -e "$HOME"/'%s' ]`, shellSafe(rest))
	}
	return fmt.Sprintf("[ -e '%s' ]", shellSafe(path))
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

// newShellPath gives the session the PATH a new interactive shell would have
// once the installs are done, which is what a reader gets on opening a terminal
// afterward. An installer that adds its directory to PATH from ~/.bashrc, as
// fzf's does, is otherwise invisible to the session, and its examples could
// not run. The new shell inherits the session's PATH, so in an image whose rc
// files nobody edited the result is unchanged. Any entry it dropped is put
// back at the end, and an rc file that blocks is cut off by the timeout. Only
// PATH is taken: functions and aliases an rc file defines stay out.
const newShellPath = `if p=$(timeout 10 bash -ic 'printf "%s" "$PATH"' 2>/dev/null </dev/null) &&
  [ -n "$p" ]; then
  IFS=: read -ra cur <<<"$PATH"
  for d in "${cur[@]}"; do case ":$p:" in *":$d:"*) ;; *) p="$p:$d" ;; esac; done
  export PATH="$p"
fi
`

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

// assignPrefix reports whether a line's single command carries NAME=value
// prefixes, which timeout would try to execute as a program, and whether the
// command expands anything, which a shell of its own could not see.
func assignPrefix(cmd string) (prefixed, expanded bool) {
	line, ok := shell.Parse(cmd)
	if !ok || len(line.Cmds) != 1 {
		return false, false
	}
	return len(line.Cmds[0].Assigns) > 0, line.Cmds[0].Expanded
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
