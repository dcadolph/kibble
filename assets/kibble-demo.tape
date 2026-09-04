# kibble demo. Regenerate with: vhs assets/kibble-demo.tape
Output assets/kibble-demo.gif

Set Shell "bash"
Set FontSize 18
Set Width 1100
Set Height 620
Set Padding 26

# Set up a throwaway repo whose install docs name a formula that no longer
# exists, without recording the setup.
Hide
Type `export PATH="$(go env GOPATH)/bin:$PATH"; D=$(mktemp -d)/acme; mkdir -p "$D"; printf '# acme\n\n## Install\n\n    brew install ripgrep\n    brew install fd\n    brew install acme-cli\n' > "$D/README.md"; cd "$D"; clear`
Enter
Sleep 500ms
Show

Sleep 800ms
Type "kibble ."
Sleep 900ms
Enter
Sleep 4s
Sleep 1500ms
