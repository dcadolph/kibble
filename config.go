package main

import (
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
}

// StepRule overrides the planner's judgment for lines matching a substring.
type StepRule struct {
	// Match is the substring that selects documented lines.
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
	return cfg.Examples, nil
}
