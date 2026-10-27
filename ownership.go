package main

import (
	"bufio"
	"encoding/json"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// repoNames is what a repository calls its own tool: its directory name and
// the package, binary, and module names its manifests declare. kibble asks it
// whether a documented install puts that tool on PATH, because a README that
// shows `uv tool install ruff` is teaching uv, and treating ruff as the tool
// under test produced coverage claims about ruff's commands in uv's report.
type repoNames struct {
	// names holds the normalized names, the repository's own included.
	names map[string]bool
	// modules holds the Go module paths the repository declares, without
	// major-version suffixes, so a go install can be matched by prefix.
	modules []string
	// ecosystems holds the ecosystems whose manifests named anything, so an
	// install can be told apart from one no manifest could have produced.
	ecosystems map[string]bool
	// declared reports whether any manifest named anything. Without one,
	// ownership is unknown rather than absent, and every install is kept.
	declared bool
}

// manifestMaxDepth bounds how deep below the root ownedNames looks for
// manifests, deep enough for a workspace member such as crates/cli.
const manifestMaxDepth = 2

// manifestSkipDirs are directories whose manifests describe something other
// than the repository's tool: dependencies, build output, and the sample
// projects tests and examples carry.
var manifestSkipDirs = map[string]bool{
	"node_modules": true, "vendor": true, "testdata": true, "target": true,
	"dist": true, "build": true, "test": true, "tests": true, "example": true,
	"examples": true, "fixtures": true,
}

var (
	// reSetupPyName matches a name keyword in a setup.py call.
	reSetupPyName = regexp.MustCompile(`\bname\s*=\s*["']([\w.-]+)["']`)
	// reGoModule matches the module directive of a go.mod file.
	reGoModule = regexp.MustCompile(`(?m)^module\s+"?([^\s"]+)"?`)
	// reMajorSuffix matches a module major-version path element such as /v2.
	reMajorSuffix = regexp.MustCompile(`/v\d+(/|$)`)
)

// ownedNames reads the manifests under dir and returns the names the
// repository declares for itself.
func ownedNames(dir, repo string) repoNames {
	rn := repoNames{names: map[string]bool{}, ecosystems: map[string]bool{}}
	rn.add(repo)
	if dir == "" {
		return rn
	}
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil {
			return nil
		}
		if d.IsDir() {
			if p == dir {
				return nil
			}
			name := strings.ToLower(d.Name())
			if strings.HasPrefix(name, ".") || manifestSkipDirs[name] ||
				strings.Count(rel, string(filepath.Separator)) >= manifestMaxDepth {
				return filepath.SkipDir
			}
			return nil
		}
		body, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil
		}
		// A manifest declares its ecosystem even when every name it holds is
		// already known, as ripgrep's Cargo.toml is in a checkout named ripgrep.
		declared := rn.declared
		rn.declared = false
		switch d.Name() {
		case "package.json":
			rn.addPackageJSON(body)
		case "pyproject.toml":
			rn.addSections(body, map[string]string{
				"project": "name", "tool.poetry": "name",
				"project.scripts": "*", "project.gui-scripts": "*", "tool.poetry.scripts": "*",
			})
		case "Cargo.toml":
			rn.addSections(body, map[string]string{"package": "name", "[bin]": "name"})
		case "setup.cfg":
			rn.addSections(body, map[string]string{"metadata": "name"})
		case "setup.py":
			for _, m := range reSetupPyName.FindAllSubmatch(body, -1) {
				rn.declare(string(m[1]))
			}
		case "go.mod":
			if m := reGoModule.FindSubmatch(body); m != nil {
				mod := stripMajor(string(m[1]))
				rn.modules = append(rn.modules, mod)
				rn.declare(path.Base(mod))
			}
		}
		if eco := manifestEcosystem[d.Name()]; rn.declared && eco != "" {
			rn.ecosystems[eco] = true
		}
		rn.declared = rn.declared || declared
		return nil
	})
	return rn
}

// add records a name without counting it as declared by a manifest.
func (rn *repoNames) add(name string) {
	if n := normalizeName(name); n != "" {
		rn.names[n] = true
	}
}

// declare records a name a manifest declared.
func (rn *repoNames) declare(name string) {
	if normalizeName(name) == "" {
		return
	}
	rn.add(name)
	rn.declared = true
}

// addPackageJSON records a node package's name and the binaries it provides.
func (rn *repoNames) addPackageJSON(body []byte) {
	var pkg struct {
		// Name is the package name, scope included.
		Name string `json:"name"`
		// Bin is a single path or a map of binary name to path.
		Bin json.RawMessage `json:"bin"`
	}
	if json.Unmarshal(body, &pkg) != nil {
		return
	}
	rn.declare(pkg.Name)
	var bins map[string]string
	if json.Unmarshal(pkg.Bin, &bins) == nil {
		for b := range bins {
			rn.declare(b)
		}
	}
}

// addSections records names from an INI-shaped manifest. keys maps a section
// header to the key holding a name in it, or to "*" when every key in the
// section is a name, as in a scripts table. An array-of-tables header such as
// [[bin]] is looked up with its inner brackets, as "[bin]".
func (rn *repoNames) addSections(body []byte, keys map[string]string) {
	section := ""
	sc := bufio.NewScanner(strings.NewReader(string(body)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			continue
		}
		want, ok := keys[section]
		if !ok {
			continue
		}
		k, v, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		k = strings.Trim(strings.TrimSpace(k), `"'`)
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		switch want {
		case "*":
			rn.declare(k)
		case k:
			rn.declare(v)
		}
	}
}

// owns reports whether a documented install puts the repository's own tool on
// PATH. A step of a kind kibble does not run as a session install is always
// owned, since nothing here would put it on PATH as the tool.
func (rn repoNames) owns(s InstallStep) bool {
	if !rn.declared {
		return true
	}
	switch {
	case s.Kind == "go-install":
		mod := stripMajor(strings.SplitN(s.Module, "@", 2)[0])
		for _, m := range rn.modules {
			if mod == m || strings.HasPrefix(mod, m+"/") {
				return true
			}
		}
		return rn.names[normalizeName(s.Binary)]
	case pkgKinds[s.Kind].Ecosystem != "":
		for _, c := range packageCandidates(s.Module) {
			if rn.names[normalizeName(c)] {
				return true
			}
		}
		return false
	}
	return true
}

// packageCandidates returns the names a package argument could match: the
// whole name and its bare form for a registry package, and every path element
// for a URL, so git+https://github.com/psf/black.git@main offers black.
func packageCandidates(pkg string) []string {
	if !strings.Contains(pkg, "://") && !strings.HasPrefix(pkg, "git+") {
		return []string{pkg, packageBinary(pkg)}
	}
	u := strings.TrimPrefix(pkg, "git+")
	if _, after, ok := strings.Cut(u, "://"); ok {
		u = after
	}
	var out []string
	for _, el := range strings.Split(u, "/")[1:] {
		el, _, _ = strings.Cut(el, "@")
		out = append(out, strings.TrimSuffix(el, ".git"))
	}
	return out
}

// normalizeName folds a package name the way package indexes compare them:
// case-insensitive, with dashes, underscores, and dots equivalent.
func normalizeName(name string) string {
	return strings.NewReplacer("_", "-", ".", "-").Replace(strings.ToLower(strings.TrimSpace(name)))
}

// stripMajor removes major-version path elements from a module path, so
// example.com/tool/v2/cmd/tool and example.com/tool compare by their stem.
func stripMajor(mod string) string {
	for reMajorSuffix.MatchString(mod) {
		mod = reMajorSuffix.ReplaceAllString(mod, "$1")
	}
	return strings.TrimSuffix(mod, "/")
}

// keep drops an install of something other than the repository's tool. A
// name that matches nothing declared is a foreign tool when the repository's
// own install in that ecosystem is in hand, as ruff is beside `pip install
// uv`, or when no manifest of that ecosystem exists, as `pip install black` is
// in a Go repository. Otherwise it is only a name kibble did not recognize:
// `cargo install ripgreppp` is the one cargo route ripgrep's README offers,
// and whether that crate exists is the registry's to say when the line runs.
func (rn repoNames) keep(steps []InstallStep) []InstallStep {
	known := map[string]bool{}
	for _, s := range steps {
		if s.Run && rn.owns(s) {
			known[stepEcosystem(s)] = true
		}
	}
	return slices.DeleteFunc(steps, func(s InstallStep) bool {
		if rn.owns(s) {
			return false
		}
		eco := stepEcosystem(s)
		return known[eco] || !rn.ecosystems[eco]
	})
}

// stepEcosystem is the ecosystem an install step runs in, empty for a kind
// that is not a package install.
func stepEcosystem(s InstallStep) string {
	if s.Kind == "go-install" {
		return "go"
	}
	return pkgKinds[s.Kind].Ecosystem
}
