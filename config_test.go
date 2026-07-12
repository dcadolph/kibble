package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestLoadExamplesConfig(t *testing.T) {
	t.Parallel()

	t.Run("missing file", func(t *testing.T) {
		t.Parallel()
		cfg, err := loadExamplesConfig(t.TempDir())
		if err != nil || cfg != nil {
			t.Errorf("got cfg=%v err=%v, want nil, nil", cfg, err)
		}
	})

	t.Run("malformed file", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".kibble.yml"), []byte(":\tnot yaml"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := loadExamplesConfig(dir); err == nil {
			t.Error("want error for malformed yaml, got nil")
		}
	})

	t.Run("valid file", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		body := `version: 1
examples:
  packages: [age]
  env:
    TZ: UTC
  fixtures:
    - path: notes.md
      contents: hello
  substitutions:
    "<key>": dummy
  steps:
    - match: tool serve
      background: true
      readyLog: listening
    - match: tool check
      nonzeroOk: true
`
		if err := os.WriteFile(filepath.Join(dir, ".kibble.yml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg, err := loadExamplesConfig(dir)
		if err != nil {
			t.Fatal(err)
		}
		want := &ExamplesConfig{
			Packages:      []string{"age"},
			Env:           map[string]string{"TZ": "UTC"},
			Fixtures:      []Fixture{{Path: "notes.md", Contents: "hello"}},
			Substitutions: map[string]string{"<key>": "dummy"},
			Steps: []StepRule{
				{Match: "tool serve", Background: true, ReadyLog: "listening"},
				{Match: "tool check", NonzeroOK: true},
			},
		}
		if diff := cmp.Diff(want, cfg); diff != "" {
			t.Errorf("mismatch (-want +got):\n%s", diff)
		}
	})
}
