package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	kplan "github.com/dcadolph/kibble/internal/plan"
	"github.com/dcadolph/kibble/internal/sandbox"
)

// The scripts the container runs for each documented install method, and the
// markers they print for the parent to read. A script here is the closest
// kibble gets to being the reader: it does what the document said, from zero.

// pkgScriptFor builds the container script for a documented package install.
// The documented line runs verbatim, so what kibble verifies is the command a
// reader would actually type.
func pkgScriptFor(step InstallStep, timeoutSecs int) string {
	pk := pkgKinds[step.Kind]
	body := shellCommand(step.Raw)
	if pk.Bootstrap != "" {
		body = pk.Bootstrap + "\n" + body
	}
	body = strings.ReplaceAll(body, "'", `'\''`)
	// The package name came out of a document, so it is interpolated into the
	// owned-bins lookup only when it carries no shell syntax. Without the
	// lookup the script simply falls back to the bin diff.
	own := ""
	if pk.OwnedBins != "" && !strings.ContainsAny(step.Module, "$\x60;&|<>()'\" ") {
		own = fmt.Sprintf(pk.OwnedBins, step.Module)
	}
	return fmt.Sprintf(pkgScript, pk.BinDir, timeoutSecs, body, step.Binary, own)
}

// shellCommand returns a documented line in the form a shell can run: the
// prompt marker many READMEs prefix and any trailing comment are removed, so
// `$ cargo install ripgrep   # from crates.io` runs as written without them.
func shellCommand(raw string) string {
	return strings.TrimSpace(stripComment(strings.TrimPrefix(strings.TrimSpace(raw), "$ ")))
}

// pkgScript installs a documented package and smoke-tests the binary it
// added. When the manager can say which binaries the package owns, only those
// are candidates, wherever they were written: a pip install drops its
// dependencies' entry points into the same directory, a user install writes
// outside it, and a package with no console script of its own once had a
// build dependency's tool smoke-tested in its name. Otherwise the new files in
// the bin directory are the candidates. Either way the documented name is
// preferred, and the first candidate covers a package whose binary carries a
// different name, as ripgrep provides rg.
const pkgScript = `set -u
BINDIR=%[1]s
mkdir -p "$BINDIR" 2>/dev/null || true
before=$(ls "$BINDIR" 2>/dev/null | sort)
out=$(timeout %[2]d bash -ec '%[3]s' 2>&1); code=$?
if [ "$code" -ne 0 ]; then
  printf 'BUILDCODE=%%d\n' "$code"
  printf '%%s\n' "$out" | grep -iE 'error(\[[A-Z]|:| )' | head -n 4
  printf '%%s\n' "$out" | tail -n 10
  exit 0
fi
printf 'BUILDCODE=0\n'
after=$(ls "$BINDIR" 2>/dev/null | sort)
newbins=$(comm -13 <(printf '%%s\n' "$before") <(printf '%%s\n' "$after"))
own=''
ownknown=0
%[5]s
if [ "$ownknown" = 1 ]; then
  runnable=$(printf '%%s\n' "$own" | while IFS= read -r p; do
    if [ -f "$p" ] && [ -x "$p" ]; then printf '%%s\n' "$p"; fi
  done)
  bin=$(printf '%%s\n' "$runnable" | while IFS= read -r p; do
    if [ "${p##*/}" = '%[4]s' ]; then printf '%%s\n' "$p"; break; fi
  done)
  [ -z "$bin" ] && bin=$(printf '%%s\n' "$runnable" | grep -v '^$' | head -n1)
else
  bin=$(printf '%%s\n' "$newbins" | grep -Fx '%[4]s' | head -n1)
  [ -z "$bin" ] && bin=$(printf '%%s\n' "$newbins" | grep -v '^$' | head -n1)
  if [ -n "$bin" ]; then bin="$BINDIR/$bin"; fi
fi
if [ -z "$bin" ]; then
  printf 'NOBIN=1\n'
  exit 0
fi
sout=$(timeout 15 "$bin" --version 2>&1); scode=$?
if [ "$scode" -ne 0 ]; then sout=$(timeout 15 "$bin" --help 2>&1); scode=$?; fi
printf 'SMOKECODE=%%d\n' "$scode"
printf 'SMOKELINE=%%s\n' "$(printf '%%s' "$sout" | head -n1 | cut -c1-70)"
`

// reSSHRemote matches a GitHub SSH remote such as git@github.com:owner/repo.git.
var reSSHRemote = regexp.MustCompile(`git@github\.com:([\w.-]+)/([\w.-]+?)(\.git)?(\s|$)`)

// rewriteSSH converts GitHub SSH remotes to HTTPS, since a clean container
// has no SSH key and a public repository clones fine without one.
func rewriteSSH(line string) string {
	return reSSHRemote.ReplaceAllString(line, "https://github.com/$1/$2.git$4")
}

// cloneScriptFor builds the container script for a git-clone install recipe:
// the documented lines run in order, and whatever binary lands in GOBIN is
// smoke-tested.
func cloneScriptFor(step InstallStep, timeoutSecs int) string {
	recipe := make([]string, 0, len(step.Block))
	for _, l := range step.Block {
		recipe = append(recipe, rewriteSSH(l))
	}
	// A recipe that ends by sourcing a file may define its product as a shell
	// function, as nvm's does, which no search for a file finds. The function
	// exists only in the shell that sourced it, so it is asked for there.
	if n := len(recipe); n > 0 && kplan.IsSimpleWord(step.Repo) &&
		(strings.HasPrefix(recipe[n-1], ". ") || strings.HasPrefix(recipe[n-1], "source ")) {
		recipe = append(recipe,
			fmt.Sprintf(`printf 'KIBBLE-FNKIND=%%s\n' "$(type -t '%s' 2>/dev/null)"`, step.Repo),
			fmt.Sprintf(`'%s' --version 2>&1 | head -n1 | sed 's/^/KIBBLE-FNVER=/'`, step.Repo))
	}
	body := strings.Join(recipe, "\n")
	body = strings.ReplaceAll(body, "'", `'\''`)
	// The repository name lands inside a single-quoted shell string, so the
	// lookup is written only for a name that cannot break out of one.
	lookup := ""
	if kplan.IsSimpleWord(step.Repo) {
		lookup = fmt.Sprintf(newShellLookup, step.Repo)
	}
	return fmt.Sprintf(cloneScript, timeoutSecs, body, step.Repo, lookup)
}

// newShellLookup asks a new interactive shell where the binary is, for a recipe
// whose installer put it on PATH through the shell's startup file. fzf's does
// exactly that, prints "Restart your shell", and leaves the binary in ~/.fzf/bin,
// where neither GOBIN nor a search of /work finds it. Only an absolute path is
// taken, since an rc file can define an alias or a function of the same name.
const newShellLookup = `if [ -z "$bin" ]; then
  bin=$(timeout 10 bash -ic 'command -v "%s"' 2>/dev/null </dev/null | tail -n1)
  case "$bin" in /*) ;; *) bin='' ;; esac
fi
`

// cloneScript runs a documented clone recipe and smoke-tests the result. It
// prints the same markers as installScript, plus NOBIN when the recipe
// produced no binary to test, and BINKIND=function when the recipe's own shell
// reported its product to be a shell function.
const cloneScript = `set -u
export GOBIN=/root/gobin
mkdir -p "$GOBIN" /work
cd /work
out=$(timeout %[1]d bash -ec '%[2]s' 2>&1); code=$?
if [ "$code" -ne 0 ]; then
  printf 'BUILDCODE=%%d\n' "$code"
  printf '%%s\n' "$out" | grep -iE 'error(\[[A-Z]|:| )' | head -n 4
  printf '%%s\n' "$out" | tail -n 10
  exit 0
fi
printf 'BUILDCODE=0\n'
if printf '%%s\n' "$out" | grep -q '^KIBBLE-FNKIND=function$'; then
  printf 'BINKIND=function\nSMOKECODE=0\n'
  printf 'SMOKELINE=%%s\n' "$(printf '%%s\n' "$out" | sed -n 's/^KIBBLE-FNVER=//p' | head -n1 | cut -c1-70)"
  exit 0
fi
bin=''
if [ -x "$GOBIN/%[3]s" ]; then
  bin="$GOBIN/%[3]s"
else
  bin=$(find /work -maxdepth 5 -type f -perm -u+x -name "%[3]s" 2>/dev/null | head -n1)
  if [ -z "$bin" ]; then b=$(ls "$GOBIN" 2>/dev/null | head -n1); [ -n "$b" ] && bin="$GOBIN/$b"; fi
fi
%[4]sif [ -z "$bin" ]; then
  printf 'NOBIN=1\n'
  exit 0
fi
sout=$(timeout 15 "$bin" --version 2>&1); scode=$?
if [ "$scode" -ne 0 ]; then sout=$(timeout 15 "$bin" --help 2>&1); scode=$?; fi
printf 'SMOKECODE=%%d\n' "$scode"
printf 'SMOKELINE=%%s\n' "$(printf '%%s' "$sout" | head -n1 | cut -c1-70)"
`

// installScript builds, then smoke-tests, one module inside the container. It
// prints BUILDCODE, SMOKECODE, and SMOKELINE markers for the parent to read. A
// build timeout surfaces as BUILDCODE=124 so it is not mistaken for a failure.
// When the module builds but kibble's guess at the binary name finds nothing,
// whatever landed in GOBIN is smoke-tested instead, and an empty GOBIN prints
// NOBIN, so a wrong guess never reads as the documented install failing.
const installScript = `set -u
export GOBIN=/root/gobin
mkdir -p "$GOBIN"
out=$(timeout %d go install '%s' 2>&1); code=$?
if [ "$code" -ne 0 ]; then
  printf 'BUILDCODE=%%d\n' "$code"
  printf '%%s\n' "$out" | grep -iE 'error(\[[A-Z]|:| )' | head -n 4
  printf '%%s\n' "$out" | tail -n 10
  exit 0
fi
printf 'BUILDCODE=0\n'
bin="$GOBIN/%s"
if [ ! -x "$bin" ]; then
  b=$(ls "$GOBIN" 2>/dev/null | head -n1)
  if [ -n "$b" ]; then bin="$GOBIN/$b"; fi
fi
if [ ! -x "$bin" ]; then
  printf 'NOBIN=1\n'
  exit 0
fi
sout=$(timeout 15 "$bin" --version 2>&1); scode=$?
if [ "$scode" -ne 0 ]; then sout=$(timeout 15 "$bin" --help 2>&1); scode=$?; fi
printf 'SMOKECODE=%%d\n' "$scode"
printf 'SMOKELINE=%%s\n' "$(printf '%%s' "$sout" | head -n1 | cut -c1-70)"
`

// brewImage is the Homebrew project's own Linux image, so a documented brew
// install runs against the real package manager rather than a guess about it.
const brewImage = "homebrew/brew"

// brewInstallScript runs the documented formula install and smoke-tests the
// binary the formula itself installed. Brew is asked which file that is, since
// a formula pulls in dependencies that install binaries of their own: taking
// whatever appeared in the bin directory smoke-tested a compiler shipped with
// rich's dependencies instead of rich. The bin diff remains as the fallback for
// a formula brew will not list.
const brewInstallScript = `set -u
before=$(ls "$(brew --prefix)/bin" 2>/dev/null | sort)
out=$(timeout %d brew install %s 2>&1); code=$?
if [ "$code" -ne 0 ]; then
  printf 'BUILDCODE=%%d\n' "$code"
  printf '%%s\n' "$out" | grep -iE 'error(\[[A-Z]|:| )' | head -n 4
  printf '%%s\n' "$out" | tail -n 10
  exit 0
fi
printf 'BUILDCODE=0\n'
bin=$(brew list --verbose %s 2>/dev/null | grep -E '/bin/[^/]+$' | head -n1)
if [ -z "$bin" ]; then
  after=$(ls "$(brew --prefix)/bin" 2>/dev/null | sort)
  b=$(comm -13 <(printf '%%s\n' "$before") <(printf '%%s\n' "$after") | head -n1)
  [ -n "$b" ] && bin="$(brew --prefix)/bin/$b"
fi
if [ -z "$bin" ]; then
  printf 'NOBIN=1\n'
  exit 0
fi
sout=$(timeout 15 "$bin" --version 2>&1); scode=$?
if [ "$scode" -ne 0 ]; then sout=$(timeout 15 "$bin" --help 2>&1); scode=$?; fi
printf 'SMOKECODE=%%d\n' "$scode"
printf 'SMOKELINE=%%s\n' "$(printf '%%s' "$sout" | head -n1 | cut -c1-70)"
`

// runBrewInstall installs a documented formula for real. Only an install that
// actually ran can call a documented brew line broken: the formula namespace
// has aliases, casks, and taps, and asking an index about a name instead of
// installing it is how a working line gets accused.
func (d *DockerRunner) runBrewInstall(ctx context.Context, step InstallStep) Result {
	start := time.Now()
	// A cask installs applications on macOS. A Linux container cannot judge
	// one, and saying so is the honest answer.
	if name, ok := strings.CutPrefix(step.Module, "cask:"); ok {
		return Result{
			Step: step, Status: StatusSkipped, Reason: ReasonOtherPlatform, Duration: time.Since(start),
			Detail: fmt.Sprintf("%s is a cask, which installs on macOS rather than in this container", name),
		}
	}
	if strings.ContainsAny(step.Module, "$`;&|<>()") {
		return Result{
			Step: step, Status: StatusSkipped, Reason: ReasonUnrecognizedTarget, Duration: time.Since(start),
			Detail: "formula name has shell characters, so it is not run",
		}
	}
	ctx, cancel := context.WithTimeout(ctx, d.Timeout+2*time.Minute)
	defer cancel()
	script := fmt.Sprintf(brewInstallScript, int(d.Timeout.Seconds()), step.Module, step.Module)
	name := sandbox.Name()
	args := append([]string{"run", "--rm", "--name", name},
		append(sandbox.Args(), brewImage, "bash", "-c", script)...)
	cmd := exec.CommandContext(ctx, sandbox.Bin(), args...)
	cmd.Cancel = sandbox.RemoveFunc(cmd, name)
	out, _ := cmd.CombinedOutput()
	if ctx.Err() != nil && errors.Is(context.Cause(ctx), context.Canceled) {
		return Result{
			Step: step, Status: StatusError, Duration: time.Since(start),
			Detail: "run was interrupted, so this step has no verdict",
		}
	}
	res := classify(step, string(out), time.Since(start))
	res.Image = brewImage
	return res
}
