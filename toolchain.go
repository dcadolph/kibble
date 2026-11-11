package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	kplan "github.com/dcadolph/kibble/internal/plan"
)

// toolchain is a language ecosystem and the container image that provides it.
type toolchain struct {
	// Name is the ecosystem identifier, such as rust or node.
	Name string
	// Image is the container image providing the ecosystem's tools.
	Image string
}

// toolchains are the ecosystems kibble can provide an image for. A recipe
// whose commands map to none of these runs in the configured default image,
// and a missing command is reported as a skip rather than a failed install.
var toolchains = map[string]toolchain{
	"rust":   {Name: "rust", Image: "rust:1"},
	"node":   {Name: "node", Image: "node:22"},
	"python": {Name: "python", Image: "python:3"},
	"go":     {Name: "go", Image: ""},
}

// pkgKind describes how one package manager installs a documented tool and
// where the binaries it produces land.
type pkgKind struct {
	// Ecosystem is the toolchain whose image provides the package manager.
	Ecosystem string
	// BinDir is the shell expression for the directory installs write to.
	BinDir string
	// Bootstrap is a command that must run before the install, for a manager
	// the base image does not ship.
	Bootstrap string
	// OwnedBins is a shell template that sets own to the absolute paths of the
	// binaries the installed package itself provides, one per line, and sets
	// ownknown to 1 when the manager answered. An answer is authoritative: it
	// covers binaries written outside BinDir, as a user install does, and an
	// empty one means the package owns no binary. Without one, whatever the
	// bin diff finds is taken as the package's.
	OwnedBins string
}

// pkgKinds are the package managers kibble installs from, keyed by step kind.
// Each runs the documented command verbatim in the image for its ecosystem.
var pkgKinds = map[string]pkgKind{
	"cargo-install": {
		Ecosystem: "rust",
		BinDir:    `"${CARGO_HOME:-$HOME/.cargo}/bin"`,
	},
	"npm-install": {
		Ecosystem: "node",
		BinDir:    `"$(npm prefix -g)/bin"`,
	},
	"pip-install": {
		Ecosystem: "python",
		BinDir:    `"/usr/local/bin"`,
		OwnedBins: `if info=$(pip show -f '%s' 2>/dev/null); then
  ownknown=1
  own=$(printf '%%s\n' "$info" |
    awk '/^Location: /{loc=substr($0,11)} /^  .*\/bin\/[^\/]+$/{sub(/^ +/,""); print loc "/" $0}' |
    while IFS= read -r p; do readlink -f "$p"; done)
fi`,
	},
	"pipx-install": {
		Ecosystem: "python",
		BinDir:    `"$HOME/.local/bin"`,
		Bootstrap: "pip install --quiet --root-user-action=ignore pipx",
	},
	"uv-install": {
		Ecosystem: "python",
		BinDir:    `"$HOME/.local/bin"`,
		Bootstrap: "pip install --quiet --root-user-action=ignore uv",
	},
}

// manifestEcosystem maps a repository manifest to the ecosystem it declares.
// It is the fallback signal when a recipe's own commands are inconclusive,
// such as a bare `make install`.
var manifestEcosystem = map[string]string{
	"Cargo.toml":       "rust",
	"package.json":     "node",
	"pyproject.toml":   "python",
	"setup.py":         "python",
	"requirements.txt": "python",
	"go.mod":           "go",
}

// detectToolchain returns the ecosystem a recipe needs, preferring the
// commands the recipe actually runs and falling back to the repository's
// manifests. It reports false when nothing identifies an ecosystem, leaving
// the caller on its configured default image.
func detectToolchain(lines []string, dir string) (toolchain, bool) {
	if name, ok := ecosystemFromCommands(lines); ok {
		return toolchains[name], true
	}
	if name, ok := ecosystemFromManifests(dir); ok {
		return toolchains[name], true
	}
	return toolchain{}, false
}

// ecosystemFromCommands returns the ecosystem most often named by the recipe's
// commands. Ties go to whichever appeared first, so the result does not depend
// on map iteration order.
func ecosystemFromCommands(lines []string) (string, bool) {
	counts := map[string]int{}
	order := map[string]int{}
	seen := 0
	for _, line := range lines {
		for _, cmd := range commandWords(line) {
			name, ok := kplan.CommandEcosystem[cmd]
			if !ok {
				continue
			}
			counts[name]++
			if _, dup := order[name]; !dup {
				order[name] = seen
				seen++
			}
		}
	}
	best, bestCount := "", 0
	for name, n := range counts {
		if n > bestCount || (n == bestCount && order[name] < order[best]) {
			best, bestCount = name, n
		}
	}
	return best, best != ""
}

// ecosystemFromManifests returns the ecosystem declared by the repository's
// manifests, and reports false when they disagree. A Go project carrying a
// package.json for its docs site names two ecosystems and settles neither, so
// the run keeps its default image and the missing tool, if any, is reported
// honestly rather than guessed at.
func ecosystemFromManifests(dir string) (string, bool) {
	if dir == "" {
		return "", false
	}
	found := ""
	for name, eco := range manifestEcosystem {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			continue
		}
		if found != "" && found != eco {
			return "", false
		}
		found = eco
	}
	return found, found != ""
}

// shellOperators separate one simple command from the next.
var shellOperators = []string{"&&", "||", "|", ";", "&"}

// commandWords returns the command name that starts each simple command on a
// line, so `sudo cargo build && npm ci` yields cargo and npm. Leading sudo and
// environment assignments are stripped, since neither names the tool.
func commandWords(line string) []string {
	line = stripComment(line)
	for _, op := range shellOperators {
		line = strings.ReplaceAll(line, op, "\n")
	}
	var out []string
	for _, seg := range strings.Split(line, "\n") {
		fields := strings.Fields(strings.TrimPrefix(strings.TrimSpace(seg), "$ "))
		for len(fields) > 0 {
			word := fields[0]
			if strings.HasPrefix(word, "#") {
				break
			}
			if word == "sudo" || strings.Contains(word, "=") {
				fields = fields[1:]
				continue
			}
			out = append(out, filepath.Base(word))
			break
		}
	}
	return out
}

// missingCommand returns the command name a container reported as missing,
// and whether the output names one. A recipe that fails because the image
// lacks its toolchain is kibble's limitation, not a broken document, so the
// caller reports a skip instead of a failed install.
func missingCommand(out string) (string, bool) {
	for _, line := range strings.Split(out, "\n") {
		// Go's toolchain reports a tool a `go generate` or build directive needs
		// as `exec: "easyjson": executable file not found in $PATH`. That is a
		// missing build tool, not a broken document, the same as a shell's
		// command-not-found.
		if m := reExecNotFound.FindStringSubmatch(line); m != nil {
			return m[1], true
		}
		// A package's own install script may name what it needs rather than
		// letting the shell say it, as fd-find does with "Missing required
		// commands: wget." The shell wording below never appears, so without
		// this the container's gap was reported as the document being broken.
		if m := reRequiredCommand.FindStringSubmatch(line); m != nil {
			// A dot belongs inside a name like python3.11 and not on the end
			// of one, where it is the sentence finishing.
			if name := strings.TrimRight(m[1], "."); name != "" {
				return name, true
			}
		}
		for _, marker := range []string{": command not found", ": not found"} {
			idx := strings.Index(line, marker)
			if idx < 0 {
				continue
			}
			head := line[:idx]
			if cut := strings.LastIndex(head, ": "); cut >= 0 {
				head = head[cut+2:]
			}
			if name := strings.TrimSpace(head); name != "" {
				return name, true
			}
		}
	}
	return "", false
}

// reExecNotFound matches the Go toolchain reporting a program it tried to run
// is not installed, such as `exec: "easyjson": executable file not found`.
var reExecNotFound = regexp.MustCompile(`exec: "?([^":]+)"?: executable file not found`)

// reRequiredCommand matches an install script naming a program it needs and
// could not find, such as `Missing required commands: wget.` The wording is an
// explicit statement rather than a resemblance, which is what separates it
// from the phrases a tool also uses about its own optional pieces.
var reRequiredCommand = regexp.MustCompile(
	`(?i)missing required commands?:\s*([A-Za-z0-9._+-]+)`)

// pythonImage returns the image a Python step in the checkout at dir runs in.
// A project that states an upper bound on Python, such as aider's
// requires-python ">=3.10,<3.15", gets the newest image the bound allows,
// since the default python:3 tag moves past a supported release and a reader
// on a supported Python would install fine. With no bound it returns the
// default image. When the stated range allows no Python 3 release at all,
// image is empty and blocked says why.
func pythonImage(dir string) (image, blocked string) {
	def := toolchains["python"].Image
	stated, maxMinor, bounded := statedPython(dir)
	switch {
	case !bounded:
		return def, ""
	case maxMinor < 0:
		return "", fmt.Sprintf("the project states Python %s, which no Python 3 image satisfies", stated)
	}
	return fmt.Sprintf("python:3.%d", maxMinor), ""
}

var (
	// rePyprojectPython matches the requires-python setting of a pyproject.toml.
	rePyprojectPython = regexp.MustCompile(`(?m)^\s*requires-python\s*=\s*["']([^"']+)["']`)
	// reSetupCfgPython matches the python_requires setting of a setup.cfg.
	reSetupCfgPython = regexp.MustCompile(`(?m)^\s*python_requires\s*=\s*(\S[^\n]*)$`)
	// reReadmePython matches a README sentence such as markitdown's "requires
	// Python 3.10 through 3.14".
	reReadmePython = regexp.MustCompile(`(?i)\bPython (3\.\d+) through 3\.(\d+)\b`)
	// rePythonSpec parses one version specifier into its operator and version.
	rePythonSpec = regexp.MustCompile(`^(<=|<|==|~=|>=|>|!=)\s*(\d+)(?:\.(\d+|\*))?(?:\.(\d+|\*))?$`)
)

// statedPython returns the Python range a checkout states, the highest
// Python 3 minor release it allows, and whether it states an upper bound at
// all. The project's own metadata wins over prose: the root pyproject.toml,
// then setup.cfg, then a README sentence naming a first and last release.
// maxMinor is -1 when the bound excludes every Python 3 release.
func statedPython(dir string) (stated string, maxMinor int, bounded bool) {
	if dir == "" {
		return "", 0, false
	}
	for _, src := range []struct {
		file string
		re   *regexp.Regexp
	}{{"pyproject.toml", rePyprojectPython}, {"setup.cfg", reSetupCfgPython}} {
		b, err := os.ReadFile(filepath.Join(dir, src.file))
		if err != nil {
			continue
		}
		if m := src.re.FindStringSubmatch(string(b)); m != nil {
			spec := strings.TrimSpace(m[1])
			maxMinor, bounded = pythonUpperBound(spec)
			return spec, maxMinor, bounded
		}
	}
	text, _, err := readREADME(dir)
	if err != nil {
		return "", 0, false
	}
	if m := reReadmePython.FindStringSubmatch(text); m != nil {
		last, _ := strconv.Atoi(m[2])
		return m[1] + " through 3." + m[2], last, true
	}
	return "", 0, false
}

// pythonUpperBound reads a PEP 440 specifier list and returns the highest
// Python 3 minor release it allows, and whether it bounds Python 3 at all.
// The compatible-release form ~=3.12 allows every later 3.x and so bounds
// nothing, while ~=3.12.0 stops at 3.12. Lower bounds and exclusions are
// ignored: the newest allowed image is the one picked, and python:3.N for an
// allowed N always exists.
func pythonUpperBound(spec string) (int, bool) {
	maxMinor, bounded := 0, false
	limit := func(n int) {
		if !bounded || n < maxMinor {
			maxMinor = n
		}
		bounded = true
	}
	for _, part := range strings.Split(spec, ",") {
		m := rePythonSpec.FindStringSubmatch(strings.TrimSpace(part))
		if m == nil {
			continue
		}
		op := m[1]
		major, _ := strconv.Atoi(m[2])
		minor, minorErr := strconv.Atoi(m[3])
		patch, patchErr := strconv.Atoi(m[4])
		hasMinor, hasPatch := minorErr == nil, patchErr == nil
		switch {
		case major < 3:
			if op == "<" || op == "<=" || op == "==" {
				limit(-1)
			}
		case major > 3:
		case op == "<" && !hasMinor:
			limit(-1)
		case op == "<" && hasPatch && patch > 0:
			limit(minor)
		case op == "<":
			limit(minor - 1)
		case (op == "<=" || op == "==") && hasMinor:
			limit(minor)
		case op == "~=" && hasMinor && m[4] != "":
			limit(minor)
		}
	}
	return maxMinor, bounded
}
