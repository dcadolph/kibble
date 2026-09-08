package main

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// kibbleConfig is the root of a repo's .kibble.yml file.
type kibbleConfig struct {
	// Version is the config schema version.
	Version int `yaml:"version"`
	// Examples tunes example planning for the repo.
	Examples *ExamplesConfig `yaml:"examples"`
}

// ExamplesConfig is a repo owner's overrides for example planning. The
// planner's heuristics decide everything on their own; this file settles the
// calls they cannot make, such as fixtures with meaningful contents or a
// step that is expected to exit nonzero.
type ExamplesConfig struct {
	// Disable turns example checks off for the repo.
	Disable bool `yaml:"disable"`
	// Packages are extra Debian packages the session installs.
	Packages []string `yaml:"packages"`
	// Env is extra environment exported for the whole session.
	Env map[string]string `yaml:"env"`
	// Fixtures are files written into the workdir before any step runs.
	Fixtures []Fixture `yaml:"fixtures"`
	// Substitutions rewrite documented text before planning, keyed by the
	// exact text to replace.
	Substitutions map[string]string `yaml:"substitutions"`
	// Steps are per-line rules matched by substring.
	Steps []StepRule `yaml:"steps"`
	// Docs are extra documents to replay, as paths relative to the repository
	// root, for instructions the naming convention does not pick up.
	Docs []string `yaml:"docs"`
	// SkipDocs are documents the convention picks up that should not run.
	SkipDocs []string `yaml:"skipDocs"`
}

// StepRule overrides the planner's judgment for the lines it selects. A rule
// must carry at least one selector, and the structured ones are preferred:
// Binary and Subcommand say what a line invokes, which is what the author
// means, while Match compares text and can only approximate it.
type StepRule struct {
	// Binary selects lines that invoke this documented binary.
	Binary string `yaml:"binary"`
	// Subcommand selects lines whose first subcommand is exactly this. It
	// pairs with Binary and is ignored without one, since a subcommand name
	// on its own belongs to no particular tool.
	Subcommand string `yaml:"subcommand"`
	// Match selects lines containing this text as a run of whole words, so
	// `tool run` does not select `tool run-production`. It is the escape
	// hatch for what Binary and Subcommand cannot say; prefer those.
	Match string `yaml:"match"`
	// Run forces a line to run even when the planner would skip it.
	Run bool `yaml:"run"`
	// Skip skips the line with this reason.
	Skip string `yaml:"skip"`
	// NonzeroOK accepts a nonzero exit as documented behavior.
	NonzeroOK bool `yaml:"nonzeroOk"`
	// Background runs the containing step behind the session.
	Background bool `yaml:"background"`
	// ReadyLog is output that marks the background step ready.
	ReadyLog string `yaml:"readyLog"`
}

// loadExamplesConfig reads .kibble.yml from a repo directory. A missing file
// returns nil config and no error; a malformed file returns the error so the
// run can name it rather than silently ignoring the owner's intent.
func loadExamplesConfig(dir string) (*ExamplesConfig, error) {
	b, err := os.ReadFile(filepath.Join(dir, ".kibble.yml"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var cfg kibbleConfig
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return nil, err
	}
	if cfg.Examples != nil {
		for i, rule := range cfg.Examples.Steps {
			// A rule that selects nothing is reported rather than ignored. It
			// is always a mistake, and silently matching no line makes the
			// owner think their override applied when it never did.
			if rule.Binary == "" && rule.Match == "" {
				return nil, fmt.Errorf("steps[%d]: a rule needs a binary or a match", i)
			}
			if rule.Subcommand != "" && rule.Binary == "" {
				return nil, fmt.Errorf("steps[%d]: subcommand %q needs a binary",
					i, rule.Subcommand)
			}
		}
	}
	return cfg.Examples, nil
}
