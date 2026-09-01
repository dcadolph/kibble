# Configuring kibble

Most repositories need no configuration at all. When the heuristics cannot settle a
call, a `.kibble.yml` at the repository root does. It writes fixtures with real
contents, exports environment, substitutes placeholder text, installs extra packages,
chooses which documents are replayed, and forces a specific line to run, skip, or run in
the background with a readiness probe. Every choice lives in the file, so the run stays
reproducible and the engine stays the thing that decides pass or fail. Set
`disable: true` to turn example checks off for a repository entirely.

The most common use is a scanner or linter whose whole job is to exit nonzero when it
finds something. Point it at real code and it does exactly that, which is the tool
working, not the docs breaking. One `nonzeroOk` line settles it.

```yaml
version: 1
examples:
  packages: [age]
  docs: [walkthrough.md]        # replay a guide the naming convention misses
  skipDocs: [docs/wip.md]       # leave one the convention picks up
  env:
    TOOL_PROFILE: ci            # exported for every document's session
  substitutions:
    "<api-key>": test-key-1234
  fixtures:
    - path: config.yaml
      contents: |
        setting: value
  steps:
    - match: mytool serve
      background: true
      readyLog: listening on
    - match: mytool scan          # a scanner exits nonzero on findings by design
      nonzeroOk: true
    - match: mytool *.go          # a documented form the clean session cannot run
      skip: file-glob loading is unreliable here
    - match: mytool demo          # force a line the planner would skip
      run: true
```

## Let a model write it

Writing that file by hand means reading every skip reason and deciding which ones you
disagree with. `-suggest` does the reading. It sends the lines the engine could not
settle, and only those, to a model you configure, then prints a `.kibble.yml` with one
entry per disagreement for you to review and commit.

```sh
export ANTHROPIC_API_KEY=...          # or OPENAI_API_KEY, or KIBBLE_ADVISOR=ollama
kibble -suggest ./myrepo > .kibble.yml
```

Claude, ChatGPT, and a local Ollama are supported, chosen in that order from the
environment, and `KIBBLE_ADVISOR` picks one explicitly. A local Ollama keeps the whole
thing on your machine. The model classifies lines once into a file you commit; it never
decides whether anything passed. The reasoning behind that boundary is in
[DESIGN.md](DESIGN.md).

## Preview the plan

Preview without running anything with `-plan`, which prints, per repository, the exact
lines kibble would run, the ones it would skip and why, and the fixtures and packages
the session needs. The preview is the floor, not the ceiling: at run time the session
may downgrade more lines to skips for reasons only execution can see, such as a command
that turns out to need a terminal. Turn the whole layer off with `-examples=false`.

## Drive it from an agent

`kibble -mcp` serves the Model Context Protocol over stdio, so an agent checks a
repository's documentation with the same engine and the same verdicts the command line
gives. Point a client at the binary:

```json
{
  "mcpServers": {
    "kibble": { "command": "kibble", "args": ["-mcp"] }
  }
}
```

Two tools. `plan_docs` answers what kibble would run and why it would leave the rest
alone; it takes milliseconds, needs no Docker, and is the right first call. `check_docs`
runs the documented steps for real, which pulls images and builds the project, so it
costs minutes and belongs in the same place a CI run does.
