package main

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"

	"github.com/dcadolph/kibble/internal/docblock"
	kplan "github.com/dcadolph/kibble/internal/plan"
)

// InstallStep is one documented install command found in a repo README.
type InstallStep struct {
	// Repo is the repository directory name.
	Repo string
	// Kind classifies the command: go-install, brew, or git-clone.
	Kind string
	// Raw is the command line as written in the docs.
	Raw string
	// Module is the go module path with version, for go-install steps.
	Module string
	// Binary is the expected installed binary name, for go-install steps.
	Binary string
	// Run reports whether kibble actually executes this kind of step.
	Run bool
	// Line is the 1-based README line the command sits on, 0 when unknown.
	Line int
	// Usage is what the README cites for this binary, when anything is cited.
	Usage *Usage
	// Block is the install recipe for a git-clone step: the clone line and the
	// lines that follow it in the same code block, such as cd and make.
	Block []string
	// Taps are the owner/name tap repositories the document tells the reader
	// to add before installing, for brew steps. A bare formula missing from
	// core can legitimately live in one of these, so a documented tap keeps a
	// missing name from being convicted.
	Taps []string
	// plan is the example plan attached to an example step.
	plan *kplan.Plan
	// dir is the local repo path an example step streams into its session.
	dir string
	// readme is the README file name the step was extracted from, so an
	// annotation points at the file the repository actually has.
	readme string
	// skippedDocs, when set, marks a placeholder step standing for documents the
	// run's budget left unread. It carries no plan and runs nothing; it exists
	// so a reader is told the coverage was bounded rather than left to assume
	// every document passed.
	skippedDocs int
	// doc is the document an example step replays, relative to the repository
	// root. Empty on every other kind of step.
	doc string
	// loaderNote is the loader step, such as ldconfig, that the document names
	// outside a source-build recipe. Empty when it names none there.
	loaderNote string
	// skipNote is why a step recorded unrun is not run, when the reason is
	// more specific than a placeholder, such as a release asset built for
	// another platform. Empty otherwise.
	skipNote string
	// skipReason is the machine-readable reason that goes with skipNote.
	skipReason Reason
}

// Extractor finds install steps in README markdown.
type Extractor interface {
	// Extract returns the install steps documented for repo.
	Extract(repo, markdown string) []InstallStep
}

// ExtractorFunc adapts a function to the Extractor interface.
type ExtractorFunc func(repo, markdown string) []InstallStep

// Extract calls f.
func (f ExtractorFunc) Extract(repo, markdown string) []InstallStep {
	return f(repo, markdown)
}

var (
	// reGoInstall matches a `go install <module>@<version>` invocation, allowing
	// leading flags such as `-v` or `-tags 'postgres'` before the module path.
	reGoInstall = regexp.MustCompile(
		`\bgo\s+install\s+(?:-\S+(?:\s+(?:'[^']*'|"[^"]*"|[^-\s@][^\s@]*))?\s+)*(\S+@\S+)`)
	// reBrew matches a `brew install <target>` invocation.
	// reBrew captures the flags and the first target of a brew install. Flags
	// come first so `brew install --cask alacritty` names alacritty, not
	// --cask; reading a flag as the formula accused working docs of citing a
	// formula that does not exist.
	reBrew = regexp.MustCompile(`\bbrew\s+install\s+((?:-[\w-]+\s+)*)(\S+)`)
	// reGitClone matches a `git clone` invocation. It deliberately does not
	// capture the target, since flags sit between the subcommand and the URL
	// and the first token is often one of them. cloneTarget reads the target.
	reGitClone = regexp.MustCompile(`\bgit\s+clone\b`)
	// reBrewTap matches a `brew tap owner/name` invocation, so a formula
	// documented to come from a tapped repository is not called missing.
	reBrewTap = regexp.MustCompile(`\bbrew\s+tap\s+([\w.-]+/[\w.-]+)`)
	// reScriptInstall matches a piped shell installer such as `curl -sSf URL | sh`
	// or `sh -c "$(curl URL)"`. kibble records it but does not run it, since
	// executing an arbitrary remote script is a risk a docs check should not take.
	reScriptInstall = regexp.MustCompile(
		`(?:curl|wget)\b[^|]*\|\s*(?:sudo\s+)?(?:sh|bash|zsh)\b` +
			`|(?:sh|bash|zsh)\s+-c\s+["']?\$\((?:curl|wget)\b`)
	// reScriptURL captures the fetched URL in a piped installer, for the report.
	reScriptURL = regexp.MustCompile(`https?://[^\s"'|)]+`)
	// reOSPackage matches a system package-manager install such as
	// `sudo apt install foo` or `dnf install foo`. kibble records it but does
	// not run it, since it needs root and a distro whose repository holds it.
	reOSPackage = regexp.MustCompile(
		`\b(apt-get|apt|dnf|yum|pacman|apk|zypper|snap)\s+(?:install|add|-S[yu]*)\b(.*)`)
)

// cloneValueFlags are the git-clone flags that consume the following token as
// their value. Skipping the value matters because a value can share the shape
// of a repository: `git clone -b release/v2 https://host/repo.git` once
// reported release/v2 as the repository, since a branch name holds a slash the
// same way a remote path does. The glued `--branch=release/v2` form is a single
// flag token and needs no entry here.
var cloneValueFlags = map[string]bool{
	"-b": true, "--branch": true, "-o": true, "--origin": true,
	"-u": true, "--upload-pack": true, "--reference": true,
	"--reference-if-able": true, "--depth": true, "--template": true,
	"-c": true, "--config": true, "--separate-git-dir": true,
	"-j": true, "--jobs": true, "--shallow-since": true,
	"--shallow-exclude": true, "--filter": true, "--bundle-uri": true,
	"--server-option": true,
}

// cloneTarget returns the repository a documented clone names. Taking the first
// token after the subcommand is wrong, because flags and their values come
// first: `git clone --depth 1 https://host/repo.git` once reported --depth as
// the repository, and the recipe that followed cloned nothing and passed.
// Flags and the values they consume are skipped, and the target is then found
// by shape, since a remote looks like a remote and a leftover flag value does
// not.
func cloneTarget(line string) string {
	fields := strings.Fields(line)
	i := 0
	for ; i < len(fields); i++ {
		if fields[i] == "clone" {
			i++
			break
		}
	}
	// A bare local path is a legitimate but rare target, so it is remembered
	// and used only when nothing remote-shaped turns up.
	plain := ""
	for ; i < len(fields); i++ {
		f := strings.Trim(fields[i], "\"'`")
		if f == "" {
			continue
		}
		if strings.HasPrefix(f, "-") {
			// A separate-token flag value is skipped so a value holding a
			// slash is never mistaken for the repository.
			if cloneValueFlags[f] && i+1 < len(fields) {
				i++
			}
			continue
		}
		if strings.Contains(f, "://") || strings.Contains(f, "@") ||
			strings.HasSuffix(f, ".git") || strings.Contains(f, "/") {
			return f
		}
		if plain == "" {
			plain = f
		}
	}
	return plain
}

// ExtractorWith returns an Extractor that reads code from fenced blocks,
// indented blocks, and inline spans, and classifies the install commands
// inside them. Only text the author marked as code is considered, so plain
// prose does not count as a runnable step. The repository's own names are
// what make a local npm install a route to the tool: `npm install chalk` is
// chalk's documented install in chalk's README and a dependency install in
// every other one. Release assets are picked for the architecture kibble was
// built for, since no daemon is asked here.
func ExtractorWith(owned repoNames) Extractor {
	return extractorFor(owned, releaseContext{Arch: runtime.GOARCH})
}

// extractorFor is ExtractorWith with what the release-binary route needs to
// know about the checkout and the container stated outright.
func extractorFor(owned repoNames, rc releaseContext) Extractor {
	return ExtractorFunc(func(repo, markdown string) []InstallStep {
		seen := map[string]bool{}
		var steps []InstallStep
		var taps []string
		tapSeen := map[string]bool{}
		blocks := docblock.CodeBlocks(markdown)
		for bi, block := range blocks {
			for i, line := range block.Lines {
				if m := reBrewTap.FindStringSubmatch(line); m != nil && !tapSeen[m[1]] {
					tapSeen[m[1]] = true
					taps = append(taps, m[1])
				}
				s, key, ok := classifyLine(repo, line)
				if !ok {
					s, key, ok = localNpmStep(repo, line, owned)
				}
				if !ok || seen[key] {
					continue
				}
				seen[key] = true
				if block.Line > 0 {
					s.Line = block.Line + i
				}
				switch {
				case s.Kind == "git-clone" && block.Span && block.List != 0:
					s.Block = listRecipe(blocks, bi)
				case s.Kind == "git-clone":
					s.Block = installRecipe(block.Lines[i:])
				}
				steps = append(steps, s)
			}
			if s, ok := sourceBuildStep(repo, block); ok {
				// Two builds can end on the same install line, one from git
				// and one from a tarball, so the whole recipe is the key.
				key := repo + "|source|" + strings.Join(s.Block, "\n")
				if !seen[key] {
					seen[key] = true
					s.loaderNote = loaderNoteOutside(markdown, s.Block)
					steps = append(steps, s)
				}
			}
			if s, ok := releaseStep(repo, block, rc); ok {
				key := repo + "|release|" + s.Module + "|" + strings.Join(s.Block, "\n")
				if !seen[key] {
					seen[key] = true
					steps = append(steps, s)
				}
			}
		}
		// A tap documented anywhere in the document covers every brew step,
		// since a reader adds it once and installs many formulas from it.
		if len(taps) > 0 {
			for i := range steps {
				if steps[i].Kind == "brew" {
					steps[i].Taps = taps
				}
			}
		}
		return steps
	})
}

// recipeCommands are the first words of a span that reads as a step of an
// install recipe written across list items.
var recipeCommands = map[string]bool{
	"cd": true, "git": true, ".": true, "source": true, "make": true, "export": true,
	"sh": true, "bash": true, "cargo": true, "go": true, "npm": true, "pip": true,
	"python": true, "python3": true,
}

// listRecipe returns the install recipe for a clone written as an inline span
// in a list. nvm's README spreads its install over list items, `cd ~/` and the
// clone in the first, `cd ~/.nvm` and the checkout in the second, sourcing in
// the third, and the clone span alone is a recipe whose product never exists.
// The recipe joins the spans of the clone's outermost list from the first span
// of the clone's item to the end of the list. A span starting with a recipe
// command joins, a single other word such as `nvm` is a prose mention and is
// dropped, and anything else, a code block inside the list, or a second clone
// keeps the clone span alone, since a recipe the document did not write is
// worse than an unverified one.
func listRecipe(blocks []docblock.Block, at int) []string {
	clone := blocks[at]
	alone := installRecipe(clone.Lines)
	var joined []string
	for k, b := range blocks {
		if b.List != clone.List || b.Item < clone.Item {
			continue
		}
		if !b.Span {
			return alone
		}
		text := strings.TrimSpace(strings.Join(b.Lines, " "))
		fields := strings.Fields(text)
		switch {
		case len(fields) == 0:
			continue
		case k != at && reGitClone.MatchString(text):
			return alone
		case recipeCommands[fields[0]] || strings.HasPrefix(fields[0], "./"):
			joined = append(joined, text)
		case len(fields) == 1:
			continue
		default:
			return alone
		}
	}
	return installRecipe(joined)
}

// reTeardown matches a line that undoes an install, such as make uninstall or
// make clean. Install blocks often document teardown next to setup, and
// running it would remove the binary the recipe just produced.
var reTeardown = regexp.MustCompile(`\b(uninstall|clean)\b|^rm\s`)

// installRecipe trims an install block to its meaningful lines: in a prompted
// block only the prompted lines survive, so printed output is not mistaken for
// a command, trailing shell comments are stripped, and capture stops at the
// first teardown line. A blank or comment line after the recipe has more than
// its clone line ends it, since blocks often list alternative install options
// separated exactly that way, and alternatives are not a sequence to run.
func installRecipe(lines []string) []string {
	var out []string
	for _, l := range docblock.PrepareLines(lines) {
		l = strings.TrimSpace(stripComment(strings.TrimPrefix(strings.TrimSpace(l), "$ ")))
		if l == "" || strings.HasPrefix(l, "#") {
			if len(out) > 1 {
				break
			}
			continue
		}
		if reTeardown.MatchString(l) {
			break
		}
		out = append(out, l)
	}
	return out
}

var (
	// reConfigure matches the configure step of an autotools build.
	reConfigure = regexp.MustCompile(`^(?:sh\s+)?\./configure(\s|$)`)
	// reCMakeGenerate matches a cmake call that generates a build tree, which
	// is any cmake call that does not build, install, or run a script.
	reCMakeGenerate = regexp.MustCompile(`^cmake\s+(?:[^-]|-[^-EP])`)
	// reBuildInstall matches the step that installs what a configured build
	// produced. The space before install keeps make uninstall out.
	reBuildInstall = regexp.MustCompile(`^(?:make|ninja)\b.*\sinstall\b|^cmake\s+--install\b`)
	// reLeadingSudo matches a sudo opening a command, which the container,
	// already root and without sudo installed, drops.
	reLeadingSudo = regexp.MustCompile(`(^|&&\s*|;\s*)sudo\s+(?:-E\s+)?`)
)

// reLoaderStep matches the ways a document tells a reader to make an installed
// shared library findable.
var reLoaderStep = regexp.MustCompile(`\bldconfig\b|\bLD_LIBRARY_PATH\b`)

// loaderNoteOutside returns the loader step, such as ldconfig, that a document
// names somewhere other than in the recipe, or empty when the recipe runs one
// itself or the document names none. A recipe that ran the step and still left
// a binary unable to start has no excuse. One that left it for a later
// sentence was run without it, and that is kibble's gap, not the document's.
func loaderNoteOutside(markdown string, recipe []string) string {
	if reLoaderStep.MatchString(strings.Join(recipe, "\n")) {
		return ""
	}
	return reLoaderStep.FindString(markdown)
}

// sourceBuildStep returns the install route a block documents by building the
// checkout from source: an autotools `./configure` or a cmake generate step,
// followed in the same block by `make install`, `ninja install`, or `cmake
// --install`. jq documents only this route, so kibble tested nothing in jq and
// missed that the binary `make install` leaves cannot find its own library
// until ldconfig runs. The recipe is the block from its first command through
// the install line, and on past it to the first blank line, comment, or
// teardown, since a block that follows the install with `sudo ldconfig`
// documents a working install and must be run whole. Every line runs as
// written apart from a leading sudo. A block that clones is the clone route's
// recipe, and one that installs system packages or pipes a remote script needs
// the routes kibble refuses, so neither is a source build. A recipe holding a
// placeholder is recorded unrun.
func sourceBuildStep(repo string, block docblock.Block) (InstallStep, bool) {
	if block.Span || !docblock.ShellLangs[block.Lang] {
		return InstallStep{}, false
	}
	var recipe []string
	var step *InstallStep
	configured := false
	for _, raw := range docblock.PrepareLines(block.Lines) {
		raw = strings.TrimPrefix(strings.TrimSpace(raw), "$ ")
		written := strings.TrimSpace(stripComment(raw))
		refused := reGitClone.MatchString(written) || reOSPackage.MatchString(written) ||
			reScriptInstall.MatchString(written)
		if step != nil && (written == "" || strings.HasPrefix(written, "#") ||
			reTeardown.MatchString(written) || refused) {
			break
		}
		if written == "" || strings.HasPrefix(written, "#") {
			continue
		}
		if refused {
			return InstallStep{}, false
		}
		l := reLeadingSudo.ReplaceAllString(written, "$1")
		recipe = append(recipe, l)
		if step != nil {
			continue
		}
		for _, seg := range splitSegments(l) {
			seg = strings.TrimSpace(seg)
			if reConfigure.MatchString(seg) || reCMakeGenerate.MatchString(seg) {
				configured = true
				continue
			}
			if configured && reBuildInstall.MatchString(seg) {
				step = &InstallStep{Repo: repo, Kind: "source-build", Raw: written}
				if block.Line > 0 {
					step.Line = docblock.SourceLineIndex(block)(raw)
				}
				break
			}
		}
	}
	if step == nil {
		return InstallStep{}, false
	}
	step.Block, step.Run = recipe, true
	for _, r := range recipe {
		if docblock.HasPlaceholder(docblock.CommandHead(r)) {
			step.Run = false
		}
	}
	return *step, true
}

// classifyLine matches one code line against the known install patterns. A
// line holding a placeholder is recorded with Run false, since running it
// would test the stand-in rather than the document.
func classifyLine(repo, line string) (InstallStep, string, bool) {
	run := !docblock.HasPlaceholder(docblock.CommandHead(line))
	if m := reGoInstall.FindStringSubmatch(line); m != nil {
		mod := m[1]
		bin := binaryName(mod)
		return InstallStep{
			Repo: repo, Kind: "go-install", Raw: strings.TrimSpace(line),
			Module: mod, Binary: bin, Run: run,
		}, repo + "|go|" + mod, true
	}
	if m := reBrew.FindStringSubmatch(line); m != nil {
		target := m[2]
		if strings.Contains(m[1], "--cask") {
			target = "cask:" + target
		}
		return InstallStep{
			Repo: repo, Kind: "brew", Raw: strings.TrimSpace(line), Module: target, Run: run,
		}, repo + "|brew|" + target, true
	}
	if kind, pkg, ok := classifyPackage(repo, line); ok {
		// The package name is asked rather than the line: Python extras such
		// as httpie[socks] are shaped like a bracketed placeholder, and the
		// name has already had them removed.
		return InstallStep{
			Repo: repo, Kind: kind, Raw: strings.TrimSpace(line),
			Module: pkg, Binary: packageBinary(pkg), Run: !docblock.HasPlaceholder(pkg),
		}, repo + "|" + kind + "|" + pkg, true
	}
	if reGitClone.MatchString(line) {
		if target := cloneTarget(line); target != "" {
			return InstallStep{
				Repo: repo, Kind: "git-clone", Raw: strings.TrimSpace(line), Module: target, Run: run,
			}, repo + "|clone|" + target, true
		}
	}
	// A piped shell installer and a system package are recorded but not run, so
	// a repository whose only install is one of these is reported honestly
	// rather than passing as if nothing were documented.
	if reScriptInstall.MatchString(line) {
		url := reScriptURL.FindString(line)
		return InstallStep{
			Repo: repo, Kind: "script-install", Raw: strings.TrimSpace(line), Module: url,
		}, repo + "|script|" + url, true
	}
	if m := reOSPackage.FindStringSubmatch(line); m != nil {
		if pkg := osPackageName(m[2]); pkg != "" {
			return InstallStep{
				Repo: repo, Kind: "os-package", Raw: strings.TrimSpace(line), Module: m[1] + ":" + pkg,
			}, repo + "|ospkg|" + m[1] + "|" + pkg, true
		}
	}
	return InstallStep{}, "", false
}

// osPackageName returns the first package named after a system install verb,
// skipping the flags that can sit before it, so `-y foo` names foo.
func osPackageName(tail string) string {
	for _, f := range strings.Fields(tail) {
		if strings.HasPrefix(f, "-") {
			continue
		}
		if strings.ContainsAny(f, "|&;$`") {
			return ""
		}
		return f
	}
	return ""
}

// pkgInvocations are the documented package installs kibble runs, in the order
// they are tried. Each names the leading words that identify the command and
// the step kind the match produces.
var pkgInvocations = []struct {
	// Kind is the step kind a match produces.
	Kind string
	// Words are the leading command words that identify the invocation.
	Words []string
	// Global reports whether the line must also carry a global flag, as npm
	// installs project dependencies rather than a tool without one.
	Global bool
}{
	{Kind: "cargo-install", Words: []string{"cargo", "install"}},
	{Kind: "uv-install", Words: []string{"uv", "tool", "install"}},
	{Kind: "pipx-install", Words: []string{"pipx", "install"}},
	{Kind: "pip-install", Words: []string{"pip", "install"}},
	{Kind: "pip-install", Words: []string{"pip3", "install"}},
	{Kind: "pip-install", Words: []string{"python", "-m", "pip", "install"}},
	{Kind: "pip-install", Words: []string{"python3", "-m", "pip", "install"}},
	{Kind: "npm-install", Words: []string{"npm", "install"}, Global: true},
	{Kind: "npm-install", Words: []string{"npm", "i"}, Global: true},
	{Kind: "npm-install", Words: []string{"pnpm", "add"}, Global: true},
	{Kind: "npm-install", Words: []string{"yarn", "global", "add"}},
}

// bootstrapPackages are the package managers and build plumbing a README
// installs to prepare the machine rather than to document its own tool. A
// line such as `pip install --upgrade pip` is setup, not an install a reader
// is meant to end up with, so kibble does not verify it as one.
var bootstrapPackages = map[string]bool{
	"pip": true, "pip3": true, "npm": true, "cargo": true, "uv": true,
	"pipx": true, "yarn": true, "pnpm": true, "setuptools": true, "wheel": true,
	"virtualenv": true, "build": true,
}

// classifyPackage matches a line against the known package installs and
// returns the step kind and the package being installed. A bootstrap package
// that is the repository itself is kept, since `pip install uv` in uv's README
// is uv's own install, not machine setup.
func classifyPackage(repo, line string) (string, string, bool) {
	for _, inv := range pkgInvocations {
		if inv.Global && !hasGlobalFlag(line) {
			continue
		}
		pkg := normalizePackage(installTarget(line, inv.Words...))
		if pkg == "" || (bootstrapPackages[pkg] && normalizeName(pkg) != normalizeName(repo)) {
			continue
		}
		return inv.Kind, pkg, true
	}
	return "", "", false
}

// reGlobalFlag matches the flag that makes a node install a tool install
// rather than a project dependency install.
var reGlobalFlag = regexp.MustCompile(`(^|\s)(-g|--global)(\s|$)`)

// hasGlobalFlag reports whether a line installs globally. A trailing comment
// is not part of the command: zx's `npm install zx # add -g to install
// globally` is a local install that mentions the flag.
func hasGlobalFlag(line string) bool {
	return reGlobalFlag.MatchString(stripComment(line))
}

// reNpmPackage matches a plain registry package argument, scoped or not, with
// an optional version or tag. A URL, a path, a GitHub shorthand, and an alias
// of another package all fail it.
var reNpmPackage = regexp.MustCompile(`^((?:@[\w~][\w.~-]*/)?[\w~][\w.~-]*)(?:@[\w.^~*-]+)?$`)

// npmLocalFlags are the npm install flags a local route may carry. Each takes
// no value and leaves the install in the current project, so every other
// argument on the line is a package.
var npmLocalFlags = map[string]bool{
	"-S": true, "--save": true, "-P": true, "--save-prod": true, "-D": true, "--save-dev": true,
	"-O": true, "--save-optional": true, "--save-peer": true, "-E": true, "--save-exact": true,
	"--no-save": true, "-f": true, "--force": true, "--legacy-peer-deps": true,
	"--no-audit": true, "--no-fund": true, "--no-package-lock": true, "--ignore-scripts": true,
	"-s": true, "--silent": true, "-q": true, "--quiet": true, "-d": true, "--verbose": true,
}

// localNpmPackage returns the first package a local npm install names and
// that package's bare name, or empty strings when the line is not a local
// install of plain registry packages. Every argument must be a registry name
// or a flag from npmLocalFlags. A registry setting is refused, since the
// package it fetches need not be the one the public name means. So is a
// workspace or prefix flag, which installs somewhere a new project lacks, and
// a path or URL riding along, which a new project cannot resolve.
func localNpmPackage(line string) (string, string) {
	cmd := shellCommand(line)
	if !localNpmInstall(cmd) {
		return "", ""
	}
	var pkg, name string
	for _, f := range strings.Fields(cmd)[2:] {
		if strings.HasPrefix(f, "-") {
			if !npmLocalFlags[f] {
				return "", ""
			}
			continue
		}
		f = unquote(f)
		m := reNpmPackage.FindStringSubmatch(f)
		if m == nil {
			return "", ""
		}
		if pkg == "" {
			pkg, name = f, m[1]
		}
	}
	return pkg, name
}

// unquote removes one matching pair of quotes around a shell word. A quote
// with no partner stays, so a broken line does not read as a clean name.
func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

// npmPackageName returns a node package request without its version or tag,
// or the request unchanged when it is not a plain registry name.
func npmPackageName(module string) string {
	if m := reNpmPackage.FindStringSubmatch(module); m != nil {
		return m[1]
	}
	return module
}

// localNpmStep returns the install step for a local npm install of a package
// the repository itself declares. The name must equal a declared package name
// exactly: the checkout's folder name and a near spelling are not enough,
// because the line runs for real and the registry serves whatever holds the
// name. The expected binary comes from the manifest and is empty for a
// package that declares none. The key differs from a global install's, so a
// document that shows both has both verified.
func localNpmStep(repo, line string, owned repoNames) (InstallStep, string, bool) {
	pkg, name := localNpmPackage(line)
	if pkg == "" {
		return InstallStep{}, "", false
	}
	bin, ok := owned.npmBinary(name)
	if !ok {
		return InstallStep{}, "", false
	}
	return InstallStep{
		Repo: repo, Kind: "npm-install", Raw: strings.TrimSpace(line),
		Module: pkg, Binary: bin, Run: !docblock.HasPlaceholder(pkg),
	}, repo + "|npm-local|" + pkg, true
}

// installKey names an install step for the purpose of adding it once across
// documents. A local npm install and a global one of the same package are two
// routes and keep separate keys.
func installKey(s InstallStep) string {
	key := s.Kind + "|" + s.Module
	if localNpmRoute(s) {
		key += "|local"
	}
	return key
}

// valueFlags are flags that consume the argument after them, so the argument
// is not mistaken for the package being installed.
var valueFlags = map[string]bool{
	"--path": true, "--git": true, "--branch": true, "--tag": true, "--rev": true,
	"--registry": true, "--root": true, "--features": true, "--bin": true,
	"--index": true, "--profile": true, "--target": true, "--version": true,
	"--python": true, "--prefix": true, "-r": true, "--requirement": true,
	"-e": true, "--editable": true, "-c": true, "--constraint": true,
}

// reVersionSpec matches the start of a version constraint on a package name,
// such as the `==1.2` in black==1.2.
var reVersionSpec = regexp.MustCompile(`(==|>=|<=|~=|!=|>|<)`)

// normalizePackage reduces a documented package argument to its bare name, so
// `"black[jupyter]"` and `ruff>=0.5` both name the package they install. The
// documented line still runs verbatim; this is only what kibble calls it.
func normalizePackage(pkg string) string {
	pkg = strings.Trim(pkg, `"'`)
	if i := strings.Index(pkg, "["); i > 0 {
		pkg = pkg[:i]
	}
	if loc := reVersionSpec.FindStringIndex(pkg); loc != nil && loc[0] > 0 {
		pkg = pkg[:loc[0]]
	}
	return pkg
}

// installTarget returns the package a line installs, or empty when the line
// does not match the given command words or names no package. A local install
// such as `cargo install --path .` names none, since the clone recipe already
// covers building the repository itself.
func installTarget(line string, words ...string) string {
	fields := strings.Fields(stripComment(strings.TrimPrefix(strings.TrimSpace(line), "$ ")))
	for len(fields) > 0 && (fields[0] == "sudo" || strings.Contains(fields[0], "=")) {
		fields = fields[1:]
	}
	if len(fields) < len(words) {
		return ""
	}
	for i, w := range words {
		if fields[i] != w {
			return ""
		}
	}
	rest := fields[len(words):]
	for i := 0; i < len(rest); i++ {
		tok := rest[i]
		if valueFlags[tok] {
			i++
			continue
		}
		if strings.HasPrefix(tok, "-") {
			continue
		}
		if tok == "." || strings.HasPrefix(tok, "./") || strings.HasPrefix(tok, "/") {
			return ""
		}
		return tok
	}
	return ""
}

// packageBinary guesses the binary name a package provides, for the case where
// the install adds nothing detectable to the bin directory. A scoped node
// package and a versioned request both reduce to the bare name, and a URL
// names the repository in its owner/name path, so an archive or a ref does not
// become the name.
func packageBinary(pkg string) string {
	if strings.Contains(pkg, "://") {
		if els := packageCandidates(pkg); len(els) > 1 {
			return els[1]
		} else if len(els) == 1 {
			return els[0]
		}
	}
	if i := strings.LastIndex(pkg, "@"); i > 0 {
		pkg = pkg[:i]
	}
	if i := strings.LastIndex(pkg, "/"); i >= 0 {
		pkg = pkg[i+1:]
	}
	return strings.TrimPrefix(pkg, "@")
}

// binaryName returns the binary name `go install` produces for a module path.
// Go names the binary after the last path element, unless that element is a
// major-version suffix like v2, in which case it uses the preceding element.
func binaryName(modulePath string) string {
	p := strings.SplitN(modulePath, "@", 2)[0]
	base := path.Base(p)
	if isMajorVersion(base) {
		base = path.Base(path.Dir(p))
	}
	return base
}

// isMajorVersion reports whether s is a module major-version suffix like v2.
func isMajorVersion(s string) bool {
	if len(s) < 2 || s[0] != 'v' {
		return false
	}
	for _, r := range s[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// releaseContext is what the release-binary route needs to know beyond the
// document: which repository the checkout is, and what the container runs.
type releaseContext struct {
	// Origin is the checkout's own GitHub repository as owner/name, read from
	// its origin remote. Empty when the checkout has none, and then a release
	// URL counts as the checkout's own when its repository name is the
	// checkout's name, the same test a documented clone is held to.
	Origin string
	// Arch is the architecture the container runs, in Go's naming.
	Arch string
}

// originRepo returns the GitHub repository a checkout's origin remote names,
// as owner/name, or empty when it has no such remote. Only a directory that
// is itself a checkout is asked, so a folder inside some other repository is
// never taken to be that repository.
func originRepo(dir string) string {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return ""
	}
	out, err := exec.Command("git", "-C", dir, "config", "--get", "remote.origin.url").Output()
	if err != nil {
		return ""
	}
	return githubRepo(httpRemote(strings.TrimSpace(string(out))))
}

// reGitHubRepo matches a GitHub repository address and captures owner/name.
var reGitHubRepo = regexp.MustCompile(
	`^(?:https?://)?(?:www\.)?github\.com/([\w.-]+/[\w.-]+?)(?:\.git)?/?$`)

// githubRepo returns owner/name for a GitHub repository address, or empty
// for any other address.
func githubRepo(url string) string {
	if m := reGitHubRepo.FindStringSubmatch(url); m != nil {
		return m[1]
	}
	return ""
}

var (
	// reReleaseURL matches a GitHub release asset URL, by tag or for the latest
	// release, capturing the owner, the repository, the tag, and the asset.
	reReleaseURL = regexp.MustCompile(`https?://github\.com/([\w.-]+)/([\w.-]+)/releases/` +
		"(?:download/([^/\\s\"'`]+)|latest/download)/([^/\\s\"'`|;&<>()]+)")
	// reAssignment matches a line that sets a shell variable, with or without
	// export, and captures the variable's name.
	reAssignment = regexp.MustCompile(`^(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)=`)
	// reShellVar matches a shell variable reference, $NAME or ${NAME}.
	reShellVar = regexp.MustCompile(`\$\{?([A-Za-z_][A-Za-z0-9_]*)`)
	// reReleaseSudo matches a sudo opening a command anywhere in a line, a
	// pipe into `sudo tar` included, which the container, already root and
	// without sudo installed, drops.
	reReleaseSudo = regexp.MustCompile(`(^|&&\s*|;\s*|\|\s*)sudo\s+(?:-E\s+)?`)
	// reAssetWord matches one word of a block that could name a release
	// asset: a URL is split at its slashes, and a variable reference stays
	// inside the word it is part of.
	reAssetWord = regexp.MustCompile("[^\\s\"'`/|;&<>()=]+")
	// reX8664 matches the spellings of x86_64, which hold a separator that
	// would otherwise split the one token in two.
	reX8664 = regexp.MustCompile(`(?i)x86[_-]64`)
	// reAlnum matches one token of an asset name.
	reAlnum = regexp.MustCompile(`[a-z0-9]+`)
)

// releaseDownload is one documented fetch of a GitHub release asset.
type releaseDownload struct {
	// Owner is the GitHub account the release belongs to.
	Owner string
	// Name is the repository the release belongs to.
	Name string
	// Asset is the asset's file name, or the pattern gh is given.
	Asset string
	// Source is the URL fetched, or the gh invocation.
	Source string
}

// releaseFollowUps are the commands that may follow a release download in its
// recipe: extracting the archive, verifying it, and moving the binary into
// place. Anything else ends the recipe, so a block that goes on to start a
// server or edit a config file has only its install run.
var releaseFollowUps = map[string]bool{
	"tar": true, "unzip": true, "gunzip": true, "mv": true, "cp": true, "install": true,
	"chmod": true, "mkdir": true, "ln": true, "cd": true, "rm": true,
	"sha256sum": true, "shasum": true,
}

// releaseStep returns the install route a block documents by downloading one
// of the checkout's own GitHub release assets: a curl or wget of a
// releases/download or releases/latest/download URL, or a gh release download
// naming its repository, followed in the same block by a tar or unzip
// extract and optionally by moving the binary into place. ntfy documents this
// as its Linux install, and kibble had nothing to run for it. The recipe is
// the variable assignments before the download, the download, and the lines
// after it up to the first command that is none of releaseFollowUps, with
// sudo dropped. Another repository's release is a dependency, not the tool,
// and a piped installer stays a script install. An asset built for another
// platform than the container's is run in the container's variant only when
// the same block names that variant, and is otherwise recorded unrun with the
// asset's platform as the reason. So is a URL holding a variable the block
// never sets, since guessing its value would test kibble's guess. A code
// span is read too: a fence indented under a mkdocs tab, as ntfy writes one
// tab per architecture, parses as a span whose first line is the language,
// and that word sits before the download where nothing is taken from.
func releaseStep(repo string, block docblock.Block, rc releaseContext) (InstallStep, bool) {
	if !block.Span && !docblock.ShellLangs[block.Lang] {
		return InstallStep{}, false
	}
	var prelude, recipe []string
	assigned := map[string]bool{}
	var dl *releaseDownload
	raw, written := "", ""
	extracted := false
	for _, line := range docblock.PrepareLines(block.Lines) {
		line = strings.TrimPrefix(strings.TrimSpace(line), "$ ")
		w := strings.TrimSpace(stripComment(line))
		if w == "" || strings.HasPrefix(w, "#") {
			continue
		}
		l := reReleaseSudo.ReplaceAllString(w, "$1")
		if dl == nil {
			if m := reAssignment.FindStringSubmatch(l); m != nil {
				assigned[m[1]] = true
				prelude = append(prelude, l)
				continue
			}
			d, ok := lineDownload(l)
			if !ok || !ownRelease(d, repo, rc) {
				continue
			}
			dl, raw, written = &d, line, w
			recipe = append(append(recipe, prelude...), l)
			extracted = extracts(l)
			continue
		}
		if d, ok := lineDownload(l); ok {
			if !ownRelease(d, repo, rc) {
				break
			}
			// The same asset for another platform is an alternative to the
			// download already taken, not a second step to run.
			if d.Asset != dl.Asset && platformFree(d.Asset) == platformFree(dl.Asset) {
				continue
			}
			recipe = append(recipe, l)
			extracted = extracted || extracts(l)
			continue
		}
		if !releaseFollowUp(l) {
			break
		}
		recipe = append(recipe, l)
		extracted = extracted || extracts(l)
	}
	if dl == nil || !extracted {
		return InstallStep{}, false
	}
	step := InstallStep{
		Repo: repo, Kind: "release-binary", Raw: written, Module: dl.Source, Block: recipe, Run: true,
	}
	if block.Line > 0 {
		step.Line = docblock.SourceLineIndex(block)(raw)
	}
	for _, r := range recipe {
		if docblock.HasPlaceholder(docblock.CommandHead(r)) {
			step.Run = false
			return step, true
		}
	}
	for _, m := range reShellVar.FindAllStringSubmatch(dl.Source, -1) {
		if !assigned[m[1]] {
			step.Run, step.skipReason = false, ReasonPlaceholder
			step.skipNote = fmt.Sprintf("the release URL uses $%s, which this block never sets, "+
				"so kibble does not guess its value", m[1])
			return step, true
		}
	}
	goos, arch := assetPlatform(dl.Asset)
	if runsOn(goos, arch, rc.Arch) {
		return step, true
	}
	if v := platformVariant(block, dl.Asset, rc.Arch); v != "" {
		from, to := assetStem(dl.Asset), assetStem(v)
		for i := range step.Block {
			step.Block[i] = strings.ReplaceAll(step.Block[i], from, to)
		}
		step.Module = strings.ReplaceAll(step.Module, from, to)
		return step, true
	}
	want := "linux/" + rc.Arch
	step.Run, step.skipReason = false, ReasonOtherPlatform
	step.skipNote = fmt.Sprintf("release asset %s is built for %s and the container runs %s, "+
		"and this block lists no %s variant to run instead",
		dl.Asset, platformName(goos, arch), want, want)
	return step, true
}

// lineDownload returns the release download a line makes, from the first
// segment that is a curl or wget of a release asset URL or a gh release
// download naming its repository. A gh call that names none downloads from
// whatever repository the reader's working directory holds, which a container
// does not have, so it is not one. A piped installer is never one.
func lineDownload(line string) (releaseDownload, bool) {
	if reScriptInstall.MatchString(line) {
		return releaseDownload{}, false
	}
	for _, seg := range splitSegments(line) {
		seg = strings.TrimSpace(seg)
		switch commandWord(seg) {
		case "curl", "wget":
			if m := reReleaseURL.FindStringSubmatch(seg); m != nil {
				return releaseDownload{Owner: m[1], Name: m[2], Asset: m[4], Source: m[0]}, true
			}
		case "gh":
			if d, ok := ghDownload(seg); ok {
				return d, true
			}
		}
	}
	return releaseDownload{}, false
}

// ghValueFlags are the gh release download flags that consume the next word.
var ghValueFlags = map[string]bool{
	"-D": true, "--dir": true, "-O": true, "--output": true, "-A": true, "--archive": true,
}

// ghDownload reads a `gh release download` segment: the repository from -R or
// --repo and the asset from -p or --pattern, quotes removed.
func ghDownload(seg string) (releaseDownload, bool) {
	fields := strings.Fields(seg)
	i := slices.Index(fields, "gh")
	if i < 0 || i+2 >= len(fields) || fields[i+1] != "release" || fields[i+2] != "download" {
		return releaseDownload{}, false
	}
	var repo, pattern string
	for j := i + 3; j < len(fields); j++ {
		f := fields[j]
		next := ""
		if j+1 < len(fields) {
			next = unquote(fields[j+1])
		}
		switch {
		case f == "-R" || f == "--repo":
			repo, j = next, j+1
		case strings.HasPrefix(f, "--repo="):
			repo = unquote(strings.TrimPrefix(f, "--repo="))
		case f == "-p" || f == "--pattern":
			pattern, j = next, j+1
		case strings.HasPrefix(f, "--pattern="):
			pattern = unquote(strings.TrimPrefix(f, "--pattern="))
		case ghValueFlags[f]:
			j++
		}
	}
	parts := strings.Split(strings.TrimSuffix(strings.TrimSuffix(repo, "/"), ".git"), "/")
	if len(parts) < 2 || parts[len(parts)-2] == "" || parts[len(parts)-1] == "" {
		return releaseDownload{}, false
	}
	return releaseDownload{
		Owner: parts[len(parts)-2], Name: parts[len(parts)-1], Asset: pattern, Source: seg,
	}, true
}

// commandWord returns the program a shell segment runs, past any sudo and
// leading variable assignments, without its directory.
func commandWord(seg string) string {
	for _, f := range strings.Fields(seg) {
		if f == "sudo" || f == "-E" || reAssignment.MatchString(f) {
			continue
		}
		return path.Base(f)
	}
	return ""
}

// ownRelease reports whether a release download fetches the checkout's own
// release. With an origin remote the owner and repository must both match it.
// Without one the repository name must be the checkout's name.
func ownRelease(d releaseDownload, repo string, rc releaseContext) bool {
	if rc.Origin != "" {
		return strings.EqualFold(d.Owner+"/"+d.Name, rc.Origin)
	}
	return strings.EqualFold(strings.TrimSuffix(d.Name, ".git"), repo)
}

// extracts reports whether a line unpacks an archive with tar or unzip.
func extracts(line string) bool {
	for _, seg := range splitSegments(line) {
		if w := commandWord(seg); w == "tar" || w == "unzip" {
			return true
		}
	}
	return false
}

// releaseFollowUp reports whether every command in a line is one that may
// follow a release download in its recipe.
func releaseFollowUp(line string) bool {
	for _, seg := range splitSegments(line) {
		if w := commandWord(seg); w != "" && !releaseFollowUps[w] {
			return false
		}
	}
	return true
}

// osTokens maps the operating system names asset files carry to Go's names.
var osTokens = map[string]string{
	"linux": "linux", "darwin": "darwin", "macos": "darwin", "mac": "darwin", "osx": "darwin",
	"apple": "darwin", "windows": "windows", "win": "windows", "win32": "windows",
	"win64": "windows", "exe": "windows", "freebsd": "freebsd", "openbsd": "openbsd",
	"netbsd": "netbsd", "android": "android", "illumos": "illumos", "solaris": "solaris",
}

// archTokens maps the architecture names asset files carry to Go's names.
// "any" stands for a build that runs on every architecture, such as a macOS
// universal binary.
var archTokens = map[string]string{
	"amd64": "amd64", "x64": "amd64", "64bit": "amd64", "arm64": "arm64", "aarch64": "arm64",
	"arm": "arm", "armv5": "arm", "armv6": "arm", "armv6l": "arm", "armv7": "arm",
	"armv7l": "arm", "armhf": "arm", "armel": "arm", "386": "386", "i386": "386",
	"i686": "386", "x86": "386", "32bit": "386", "riscv64": "riscv64", "ppc64le": "ppc64le",
	"ppc64": "ppc64", "s390x": "s390x", "loong64": "loong64", "mips": "mips",
	"mips64": "mips64", "all": "any", "universal": "any",
}

// assetPlatform returns the operating system and architecture an asset name
// says it was built for, each empty when the name does not say.
func assetPlatform(asset string) (goos, arch string) {
	name := reX8664.ReplaceAllString(strings.ToLower(asset), "amd64")
	for _, tok := range reAlnum.FindAllString(name, -1) {
		if v, ok := osTokens[tok]; ok && goos == "" {
			goos = v
		}
		if v, ok := archTokens[tok]; ok && arch == "" {
			arch = v
		}
	}
	return goos, arch
}

// platformFree returns an asset name with its platform tokens blanked, so two
// builds of one release compare equal.
func platformFree(asset string) string {
	name := reX8664.ReplaceAllString(strings.ToLower(asset), "amd64")
	return reAlnum.ReplaceAllStringFunc(name, func(tok string) string {
		if _, ok := osTokens[tok]; ok {
			return "*"
		}
		if _, ok := archTokens[tok]; ok {
			return "*"
		}
		return tok
	})
}

// runsOn reports whether an asset built for goos and arch runs in a Linux
// container of the given architecture. A name that does not say is assumed
// to run, since the download is then whatever the document says it is.
func runsOn(goos, arch, container string) bool {
	return (goos == "" || goos == "linux") && (arch == "" || arch == "any" || arch == container)
}

// platformVariant returns the build of an asset for the container's platform
// when the block names one, in a URL, a gh pattern, a comment, or any other
// word, and empty otherwise.
func platformVariant(block docblock.Block, asset, arch string) string {
	want := platformFree(asset)
	for _, line := range block.Lines {
		for _, w := range reAssetWord.FindAllString(line, -1) {
			if w == asset || platformFree(w) != want {
				continue
			}
			if goos, a := assetPlatform(w); runsOn(goos, a, arch) {
				return w
			}
		}
	}
	return ""
}

// platformName names the platform an asset was built for, as os/arch or as
// whichever half the name gives.
func platformName(goos, arch string) string {
	switch {
	case goos != "" && arch != "" && arch != "any":
		return goos + "/" + arch
	case goos != "":
		return goos
	}
	return arch
}

// archiveSuffixes are the archive extensions an asset stem is found without,
// longest first so .tar.gz is not read as .gz.
var archiveSuffixes = []string{
	".tar.gz", ".tar.xz", ".tar.bz2", ".tar.zst", ".tgz", ".tbz", ".txz", ".zip", ".tar",
	".gz", ".xz", ".bz2", ".zst",
}

// assetStem returns an asset name without its archive extension. It is the
// name the archive's top directory usually carries, so swapping it swaps every
// line of a recipe that names the archive or what it unpacks to.
func assetStem(asset string) string {
	for _, suf := range archiveSuffixes {
		if s, ok := strings.CutSuffix(asset, suf); ok && s != "" {
			return s
		}
	}
	return asset
}
