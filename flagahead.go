package main

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// ecosystemSources are the file types a binary of each ecosystem defines its
// flags in. Only the installed binary's own language is searched: code2prompt
// is a Rust binary, and its website's JavaScript naming a json option once
// made a removed flag look merely unreleased.
var ecosystemSources = map[string]map[string]bool{
	"go":     {".go": true},
	"rust":   {".rs": true},
	"python": {".py": true},
	"node":   {".js": true, ".mjs": true, ".cjs": true, ".ts": true, ".mts": true},
}

const (
	// sourceMaxBytes skips generated or vendored blobs no flag is defined in.
	sourceMaxBytes = 1 << 20
	// sourceMaxLine skips minified lines, which name every word in a bundle.
	sourceMaxLine = 1000
)

// installEcosystem returns the ecosystem an install step's binary is built in,
// empty when the step does not say.
func installEcosystem(kind string) string {
	if kind == "go-install" {
		return "go"
	}
	return pkgKinds[kind].Ecosystem
}

// reFlagDefinition matches the API words that surround a flag definition across
// the common parsers: cobra and pflag (Flags, StringVar), urfave (Name:),
// clap's builder (Arg::new, .long), argparse, click, commander, and yargs.
var reFlagDefinition = regexp.MustCompile(
	`(?i)flag|option|add_argument|arg::new|\.long\(|\bname:\s`)

// unreleasedFlags returns the flags among names that the checkout's source, in
// the language of the install kind, defines. A flag the installed release
// rejects while HEAD defines it is a document ahead of the release, not a
// stale one: yq documented --ini-key-value-delimiters before shipping it, and
// the maintainer who adds a flag and its docs in one change cannot release
// first. A definition is a quoted --name, or a quoted name on a line that
// reads like a flag definition, so the bare word in an unrelated string does
// not count.
func unreleasedFlags(dir, kind string, names []string) map[string]bool {
	defines := map[string]func(string) bool{}
	for _, n := range names {
		q := regexp.QuoteMeta(n)
		dashed := regexp.MustCompile(`["'` + "`" + `]--?` + q + `([^\w-]|$)`)
		quoted := regexp.MustCompile(`["'` + "`" + `]` + q + `["'` + "`" + `]`)
		defines[n] = func(line string) bool {
			return dashed.MatchString(line) || (quoted.MatchString(line) && reFlagDefinition.MatchString(line))
		}
	}
	return sourceDefines(dir, kind, defines)
}

// unreleasedSubs returns the subcommands among names that the checkout's
// source defines, the subcommand side of unreleasedFlags. sqlc documented
// analyze and fmt while its latest release had neither. A definition is the
// parser's own declaration of the name: cobra's Use: "name" with or without
// argument text, urfave's Name: "name", clap's Command::new("name") or
// #[command(name = "name")], and argparse's add_parser("name").
func unreleasedSubs(dir, kind string, names []string) map[string]bool {
	defines := map[string]func(string) bool{}
	for _, n := range names {
		q := regexp.QuoteMeta(n)
		pats := []*regexp.Regexp{
			regexp.MustCompile(`\bUse:\s*"` + q + `(\s[^"]*)?"`),
			regexp.MustCompile(`\bName:\s*"` + q + `"`),
			regexp.MustCompile(`Command::new\(\s*"` + q + `"\s*\)`),
			regexp.MustCompile(`#\[command\([^)]*\bname\s*=\s*"` + q + `"`),
			regexp.MustCompile(`add_parser\(\s*["']` + q + `["']`),
		}
		defines[n] = func(line string) bool {
			return slices.ContainsFunc(pats, func(re *regexp.Regexp) bool { return re.MatchString(line) })
		}
	}
	return sourceDefines(dir, kind, defines)
}

// sourceDefines walks the checkout's source in the language of the install
// kind and returns the names with a line their define function accepts.
// Hidden, vendored, and generated directories, oversized files, and minified
// lines are skipped, since none of them is where a project declares its CLI.
func sourceDefines(dir, kind string, defines map[string]func(string) bool) map[string]bool {
	found := map[string]bool{}
	exts := ecosystemSources[installEcosystem(kind)]
	if dir == "" || len(defines) == 0 || exts == nil {
		return found
	}
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if len(found) == len(defines) {
			return filepath.SkipAll
		}
		if d.IsDir() {
			name := strings.ToLower(d.Name())
			if p != dir && (strings.HasPrefix(name, ".") || manifestSkipDirs[name]) {
				return filepath.SkipDir
			}
			return nil
		}
		if !exts[strings.ToLower(filepath.Ext(p))] {
			return nil
		}
		if info, ierr := d.Info(); ierr != nil || info.Size() > sourceMaxBytes {
			return nil
		}
		f, oerr := os.Open(p)
		if oerr != nil {
			return nil
		}
		defer func() { _ = f.Close() }()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), sourceMaxBytes)
		for sc.Scan() {
			line := sc.Text()
			if len(line) > sourceMaxLine {
				continue
			}
			for n, def := range defines {
				if !found[n] && strings.Contains(line, n) && def(line) {
					found[n] = true
				}
			}
		}
		return nil
	})
	return found
}
