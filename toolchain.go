package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
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
	// OwnedBins is a shell assignment template that sets own to the binaries
	// the installed package itself provides, one basename per line. Only a
	// manager that lands dependency entry points in the same directory needs
	// one; for the rest, whatever the bin diff finds is already the package's.
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
		OwnedBins: `own=$(pip show -f '%s' 2>/dev/null | awk -F/ '/bin\// {print $NF}' | sort -u)`,
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

// commandEcosystem maps a build command to the ecosystem that provides it.
// Commands present in every image, such as make and cc, are deliberately
// absent: they say nothing about which toolchain a recipe needs.
var commandEcosystem = map[string]string{
	"cargo":  "rust",
	"rustc":  "rust",
	"rustup": "rust",
	"npm":    "node",
	"npx":    "node",
	"pnpm":   "node",
	"yarn":   "node",
	"node":   "node",
	"pip":    "python",
	"pip3":   "python",
	"poetry": "python",
	"uv":     "python",
	"python": "python",
	"go":     "go",
	"gofmt":  "go",
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
			name, ok := commandEcosystem[cmd]
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
