package plan

import (
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/dcadolph/kibble/internal/shell"
)

// The session replays a document in a repository kibble made. It runs git
// init over a tree streamed without .git, so HEAD is unborn and nothing is
// tracked, branched, tagged, stashed or remote. A git command that reads any
// of that reports on kibble rather than on the document: gum's README pipes
// `git branch` into its picker, the empty list made gum exit before it ever
// reached a terminal, and kibble blamed the document.
//
// A git invocation therefore runs only in a form that writes repository
// state or asks git about itself. Every other form is skipped, including
// verbs nobody has listed yet, so the next one that reads invented state has
// to be allowed on purpose before it can produce a verdict. Listing the
// readers instead is the denylist this replaced, and it let `git branch`
// through because nobody had thought to name it.

// gitValueOpts are git's global options that take the next word as a value,
// from the synopsis git --help prints. The last four also accept an attached
// value after an equals sign.
var gitValueOpts = map[string]bool{
	"-C": true, "-c": true, "--git-dir": true, "--work-tree": true, "--namespace": true,
	"--config-env": true,
}

// gitInfoOpts are global options that ask git about itself and need no
// subcommand.
var gitInfoOpts = map[string]bool{
	"-v": true, "--version": true, "-h": true, "--help": true, "--html-path": true,
	"--man-path": true, "--info-path": true, "--exec-path": true, "--list-cmds": true,
}

// gitReadsRepo reports whether a line invokes git in a form that reads the
// session's repository. staged says an earlier line of the document already
// ran git add, which is what lets a later commit find something to commit.
func gitReadsRepo(flat string, staged bool) bool {
	for _, c := range gitInvocations(flat) {
		sub, args := gitSubcommand(c.Words[1:])
		if sub == "add" {
			staged = true
		}
		if !gitWrites(sub, args, staged) {
			return true
		}
	}
	return false
}

// reRemoteRef matches a word naming a remote-tracking ref such as
// origin/main~4, alone or as an option's attached value. Tools other than git
// read history through such a ref, and the session's repository has no
// remote to resolve it against. Only a whole word counts, so a URL whose path
// happens to contain /upstream/ is not one.
var reRemoteRef = regexp.MustCompile(`(^|=)(origin|upstream)/[A-Za-z0-9._/-]+`)

// namesRemoteRef reports whether any command in a line is handed a
// remote-tracking ref.
func namesRemoteRef(flat string) bool {
	for _, w := range shell.ArgWordsOrFields(flat) {
		if reRemoteRef.MatchString(w) {
			return true
		}
	}
	return false
}

// gitStages reports whether a line runs git add, so later lines in the same
// document can commit what it staged.
func gitStages(flat string) bool {
	for _, c := range gitInvocations(flat) {
		if sub, _ := gitSubcommand(c.Words[1:]); sub == "add" {
			return true
		}
	}
	return false
}

// gitInvocations returns every git command a line runs, in source order,
// including git reached through a wrapper such as xargs or env. A git named
// inside an argument, as in `echo "see git log"`, is a mention and is not
// returned.
func gitInvocations(flat string) []shell.Cmd {
	line, ok := shell.Parse(flat)
	if !ok {
		return nil
	}
	var out []shell.Cmd
	for _, c := range line.Cmds {
		c = shell.Invoked(c)
		if path.Base(c.Name()) == "git" {
			out = append(out, c)
		}
	}
	return out
}

// gitSubcommand returns the subcommand of a git invocation and the words
// after it, given the words after git itself. Global options before the
// subcommand are stepped over. An invocation with no subcommand that asks git
// about itself, such as git --version, returns the subcommand "version".
func gitSubcommand(words []string) (string, []string) {
	info := false
	for i := 0; i < len(words); i++ {
		w := words[i]
		if !strings.HasPrefix(w, "-") {
			return w, words[i+1:]
		}
		name, _, attached := strings.Cut(w, "=")
		switch {
		case gitValueOpts[name] && !attached:
			i++
		case gitInfoOpts[name]:
			info = true
		}
	}
	if info {
		return "version", nil
	}
	return "", nil
}

// gitWrites reports whether a git subcommand, in the form its arguments give
// it, writes repository state or asks git about itself rather than reading
// the repository. Each allowed form was measured in the session image
// against an unborn HEAD.
func gitWrites(sub string, args []string, staged bool) bool {
	switch sub {
	case "", "init", "clone", "add", "version":
		return true
	case "help":
		// The image has no man, so git help with a topic fails for a reason
		// that is the container's rather than the document's.
		return len(gitOperands(args, nil)) == 0
	case "commit":
		return commitWrites(args, staged)
	case "checkout":
		return createsBranch(args, "-b", "-B")
	case "switch":
		return createsBranch(args, "-c", "-C", "--create", "--force-create")
	case "branch":
		return renamesBranch(args)
	case "remote":
		return remoteWrites(args)
	case "config":
		return configWrites(args)
	}
	return false
}

// gitOperands returns the words of a subcommand's arguments that are not
// options or option values. valueOpts names the options that take the next
// word as their value.
func gitOperands(args, valueOpts []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			return append(out, args[i+1:]...)
		case strings.HasPrefix(a, "-") && len(a) > 1:
			if slices.Contains(valueOpts, a) {
				i++
			}
		default:
			out = append(out, a)
		}
	}
	return out
}

// commitWrites reports whether a commit can succeed in the session without
// reaching for history. A commit needs something staged, which in kibble's
// repository only a documented git add provides, since -a stages tracked
// files and nothing is tracked. --allow-empty needs nothing. Amending,
// fixing up, squashing or reusing a message all read a commit that does not
// exist.
func commitWrites(args []string, staged bool) bool {
	allowEmpty := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			return staged || allowEmpty
		case strings.HasPrefix(a, "--"):
			name, _, attached := strings.Cut(a, "=")
			switch name {
			case "--amend", "--fixup", "--squash", "--reuse-message", "--reedit-message":
				return false
			case "--allow-empty":
				allowEmpty = true
			case "--message", "--file", "--author", "--date", "--template", "--cleanup",
				"--trailer", "--pathspec-from-file":
				if !attached {
					i++
				}
			}
		case strings.HasPrefix(a, "-") && len(a) > 1:
			skip, history := commitCluster(a)
			if history {
				return false
			}
			if skip {
				i++
			}
		}
	}
	return staged || allowEmpty
}

// commitCluster reads one word of git commit's single-letter options and
// reports whether the next word is the value of its last option and whether
// it reuses an existing commit's message through -C or -c.
func commitCluster(a string) (bool, bool) {
	for j := 1; j < len(a); j++ {
		switch a[j] {
		case 'C', 'c':
			return false, true
		case 'm', 'F', 't':
			return j == len(a)-1, false
		case 'S', 'u':
			return false, false
		}
	}
	return false, false
}

// createsBranch reports whether a checkout or switch creates a branch at the
// current HEAD. On an unborn HEAD that works, while any start point names a
// commit that does not exist, and a checkout with no create option reads the
// index or a branch the repository lacks.
func createsBranch(args []string, createOpts ...string) bool {
	named := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, _, attached := strings.Cut(a, "=")
		switch {
		case slices.Contains(createOpts, name):
			if !attached {
				i++
			}
			named = true
		case a == "--":
			return false
		case strings.HasPrefix(a, "-"):
		default:
			return false
		}
	}
	return named
}

// renamesBranch reports whether a git branch call renames the current branch,
// as the new-repository snippet's `git branch -M main` does. That works on an
// unborn HEAD. Listing, creating or deleting a branch reads one that is not
// there.
func renamesBranch(args []string) bool {
	rename := false
	for _, a := range args {
		if a == "-m" || a == "-M" || a == "--move" {
			rename = true
		}
	}
	return rename && len(gitOperands(args, nil)) == 1
}

// remoteWrites reports whether a git remote call records a remote rather than
// reading one. Adding with -f fetches from the reader's remote at once, so it
// is not a write kibble can stand behind.
func remoteWrites(args []string) bool {
	ops := gitOperands(args, nil)
	if len(ops) == 0 {
		return false
	}
	switch ops[0] {
	case "add":
		return !slices.Contains(args, "-f") && !slices.Contains(args, "--fetch")
	case "set-url":
		return true
	}
	return false
}

// configValueOpts are git config options that take the next word as a value.
var configValueOpts = []string{"-f", "--file", "--blob", "--type", "--default", "--comment"}

// configWrites reports whether a git config call sets a value rather than
// reading one. A read reports the configuration kibble wrote, such as the
// user.name it set for the session, or the empty global configuration of a
// fresh container.
func configWrites(args []string) bool {
	for _, a := range args {
		name, _, _ := strings.Cut(a, "=")
		switch name {
		case "--get", "--get-all", "--get-regexp", "--get-urlmatch", "--get-color",
			"--get-colorbool", "--list", "-l", "--edit", "-e":
			return false
		case "--add", "--unset", "--unset-all", "--replace-all", "--rename-section",
			"--remove-section":
			return true
		}
	}
	ops := gitOperands(args, configValueOpts)
	if len(ops) == 0 {
		return false
	}
	switch ops[0] {
	case "set", "unset", "rename-section", "remove-section":
		return true
	case "get", "list", "edit":
		return false
	}
	return len(ops) >= 2
}
