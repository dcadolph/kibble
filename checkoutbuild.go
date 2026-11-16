package main

import (
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"strings"

	kplan "github.com/dcadolph/kibble/internal/plan"
)

// checkoutBuild returns an install that builds the repository's own tool from
// the checkout, for a repository whose documented installs give an example
// session nothing to run against. Without it one missing install route cost
// every example in the repository, and on the 11-11 sweeps that silence was
// the largest single reason a repository came back with nothing tested.
//
// It builds only what it can name without guessing: a Go main package at the
// root or under cmd/, a Rust package with one binary or one named for the
// repository, or a Python project with one console script or one named for it.
// When two ecosystems both offer a tool, the one named for the repository wins,
// and with no such name nothing is built, since running the examples against
// the wrong binary would convict a document of someone else's failure.
func checkoutBuild(dir, repo string) (kplan.PlanInstall, bool) {
	var found []kplan.PlanInstall
	for _, try := range []func(dir, repo string) (kplan.PlanInstall, bool){
		goCheckoutBuild, rustCheckoutBuild, pythonCheckoutBuild,
	} {
		if in, ok := try(dir, repo); ok {
			found = append(found, in)
		}
	}
	if len(found) == 1 {
		return found[0], true
	}
	for _, in := range found {
		if normalizeName(in.Binary) == normalizeName(repo) {
			return in, true
		}
	}
	return kplan.PlanInstall{}, false
}

// goCheckoutBuild builds a Go module's main package with go install, from the
// root when the root is one, else from the cmd/ directory pickBinary selects.
func goCheckoutBuild(dir, repo string) (kplan.PlanInstall, bool) {
	body, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return kplan.PlanInstall{}, false
	}
	m := reGoModule.FindSubmatch(body)
	if m == nil {
		return kplan.PlanInstall{}, false
	}
	base := path.Base(stripMajor(string(m[1])))
	if isMainPackage(dir) {
		if !reBinName.MatchString(base) {
			return kplan.PlanInstall{}, false
		}
		return kplan.PlanInstall{Cmd: "go install .", Ecosystem: "go", Binary: base, FromCheckout: true}, true
	}
	ents, err := os.ReadDir(filepath.Join(dir, "cmd"))
	if err != nil {
		return kplan.PlanInstall{}, false
	}
	var mains []string
	for _, e := range ents {
		if e.IsDir() && isMainPackage(filepath.Join(dir, "cmd", e.Name())) {
			mains = append(mains, e.Name())
		}
	}
	name, ok := pickBinary(mains, repo, base)
	if !ok {
		return kplan.PlanInstall{}, false
	}
	return kplan.PlanInstall{Cmd: "go install ./cmd/" + name, Ecosystem: "go", Binary: name, FromCheckout: true}, true
}

// isMainPackage reports whether dir holds a non-test Go file of package main.
func isMainPackage(dir string) bool {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	fset := token.NewFileSet()
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.PackageClauseOnly)
		if err == nil && f.Name.Name == "main" {
			return true
		}
	}
	return false
}

// rustCheckoutBuild builds a Rust package's binary with cargo install --path,
// locked when the checkout carries a lock file, as a release build would be. A
// workspace root with no package of its own is left alone.
func rustCheckoutBuild(dir, repo string) (kplan.PlanInstall, bool) {
	body, err := os.ReadFile(filepath.Join(dir, "Cargo.toml"))
	if err != nil {
		return kplan.PlanInstall{}, false
	}
	pkgs := sectionValues(body, map[string]string{"package": "name"})
	if len(pkgs) == 0 {
		return kplan.PlanInstall{}, false
	}
	bins := sectionValues(body, map[string]string{"[bin]": "name"})
	if len(bins) == 0 {
		if _, err := os.Stat(filepath.Join(dir, "src", "main.rs")); err != nil {
			return kplan.PlanInstall{}, false
		}
		bins = pkgs[:1]
	}
	name, ok := pickBinary(bins, repo, pkgs[0])
	if !ok {
		return kplan.PlanInstall{}, false
	}
	cmd := "cargo install --path . --bin " + name
	if _, err := os.Stat(filepath.Join(dir, "Cargo.lock")); err == nil {
		cmd += " --locked"
	}
	return kplan.PlanInstall{Cmd: cmd, Ecosystem: "rust", Binary: name, FromCheckout: true}, true
}

// checkoutVenv is the virtual environment a Python tool built from the
// checkout is installed into.
const checkoutVenv = "/opt/kibble-tool"

// pythonCheckoutBuild installs a Python project from the checkout with pip,
// when its pyproject.toml declares the console script to run. The install goes
// into an environment of its own with the script linked onto PATH, the way pipx
// does it. Installed into the image's Python, the tool shared an environment
// with whatever its examples manage: poetry's `poetry sync --only dev`, run in
// poetry's checkout, removed poetry's own dependencies, and the 39 lines after
// it crashed on a missing module.
func pythonCheckoutBuild(dir, repo string) (kplan.PlanInstall, bool) {
	body, err := os.ReadFile(filepath.Join(dir, "pyproject.toml"))
	if err != nil {
		return kplan.PlanInstall{}, false
	}
	scripts := sectionValues(body, map[string]string{"project.scripts": "*", "tool.poetry.scripts": "*"})
	names := sectionValues(body, map[string]string{"project": "name", "tool.poetry": "name"})
	names = append(names, "")
	name, ok := pickBinary(scripts, repo, names[0])
	if !ok {
		return kplan.PlanInstall{}, false
	}
	cmd := "python3 -m venv " + checkoutVenv + " && " + checkoutVenv + "/bin/pip install --quiet . && " +
		"ln -sf " + checkoutVenv + "/bin/" + name + " /usr/local/bin/" + name
	return kplan.PlanInstall{Cmd: cmd, Ecosystem: "python", Binary: name, FromCheckout: true}, true
}

// pickBinary returns the candidate named for the repository or its package,
// else the only candidate, else nothing. A name that cannot sit safely in a
// shell command is never returned.
func pickBinary(cands []string, names ...string) (string, bool) {
	pick := ""
	for _, c := range cands {
		for _, n := range names {
			if n != "" && normalizeName(c) == normalizeName(n) {
				pick = c
				break
			}
		}
		if pick != "" {
			break
		}
	}
	if pick == "" && len(cands) == 1 {
		pick = cands[0]
	}
	if pick == "" || !reBinName.MatchString(pick) {
		return "", false
	}
	return pick, true
}

// checkoutNote labels every example result run against a tool kibble built
// from the checkout, since a reader installs a release and the two can differ.
const checkoutNote = "built from the checkout, the documented install was not tested"

// fromCheckout reports whether a plan's tool is one kibble built from the
// checkout rather than one a documented install provided.
func fromCheckout(plan *kplan.Plan) bool {
	for _, in := range plan.Installs {
		if in.FromCheckout {
			return true
		}
	}
	return false
}
