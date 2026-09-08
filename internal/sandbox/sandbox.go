// Package sandbox holds the container boundary kibble executes inside: the
// hardening flags every run carries, the metadata hostnames it closes, and
// the plumbing that names, reaches, and tears down containers. It is separate
// because it is the security surface, and a security surface buried among a
// thousand lines of result classification is one nobody reviews.
//
// What it does not claim is as important as what it does. The session runs as
// root inside the container, because installing the packages a document
// depends on requires that, and the network stays open, because verifying an
// install is fetching it. The honest description is a reduced-capability root
// process on an open network behind the Docker boundary. docs/SECURITY.md
// owns the full statement.
package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"
)

// hardenedArgs are the docker flags every kibble container runs under. A
// documented command is text from a repository, which makes it untrusted
// input, so the container gets no capability the session does not need and
// bounded memory and process counts. The capabilities kept are the minimal
// set apt needs, since sessions install Debian packages the docs depend on.
// Network stays on because verifying an install is fetching it; that is a
// conscious tradeoff the README's security section owns.
func Args() []string {
	return append([]string{
		"--memory=4g",
		"--pids-limit=1024",
		"--security-opt", "no-new-privileges",
		"--cap-drop", "ALL",
		"--cap-add", "CHOWN",
		"--cap-add", "DAC_OVERRIDE",
		"--cap-add", "FOWNER",
		"--cap-add", "SETGID",
		"--cap-add", "SETUID",
	}, metadataBlackhole()...)
}

// cloudMetadataHosts are the names a cloud instance answers on to hand out
// credentials. A documented line kibble runs is untrusted, and kibble's own
// home is a CI runner inside exactly this kind of instance, so the convenient
// path to them is closed.
var cloudMetadataHosts = []string{
	"metadata.google.internal",
	"metadata.goog",
	"instance-data",
	"instance-data.ec2.internal",
	"metadata.packet.net",
}

// metadataBlackhole points the well-known metadata hostnames at an address
// that answers nothing.
//
// This closes the named path and not the numbered one. The addresses these
// names resolve to, 169.254.169.254 above all, stay reachable, because Docker
// has no portable flag that drops a route to a single address and the two
// alternatives are worse: an internal network breaks every install kibble
// exists to run, and host firewall rules are not kibble's to install. The
// honest description of this is a speed bump, and the security document says
// so rather than implying a wall.
func metadataBlackhole() []string {
	args := make([]string, 0, len(cloudMetadataHosts)*2)
	for _, host := range cloudMetadataHosts {
		args = append(args, "--add-host", host+":0.0.0.0")
	}
	return args
}

// containerSeq numbers containers within one run so every step gets a name
// kibble can tear down on its own.
var containerSeq atomic.Uint64

// containerName returns a container name unique to this process and step.
func Name() string {
	return fmt.Sprintf("kibble-%d-%d", os.Getpid(), containerSeq.Add(1))
}

// removeContainerFunc returns the cancel hook for a docker run. Killing the
// docker client leaves the container running, since the daemon owns it, so
// the hook removes the container by name before killing the client. Without
// it an interrupted run leaves containers building in the background.
func RemoveFunc(cmd *exec.Cmd, name string) func() error {
	return func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = exec.CommandContext(ctx, Bin(), "rm", "-f", name).Run()
		return cmd.Process.Kill()
	}
}

// dockerBin returns the container client binary to invoke. Podman and other
// drop-in replacements speak docker's command line, so KIBBLE_DOCKER names
// the binary and docker stays the default.
func Bin() string {
	if bin := os.Getenv("KIBBLE_DOCKER"); bin != "" {
		return bin
	}
	return "docker"
}

// DockerAvailable reports an error when the docker CLI cannot reach a running
// daemon, so kibble can fail fast with a clear message instead of reporting
// every install as a container error.
func Available(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, Bin(), "version", "--format", "{{.Server.Version}}").CombinedOutput()
	if err != nil {
		return fmt.Errorf("cannot reach the %s daemon: %s", Bin(), strings.TrimSpace(string(out)))
	}
	return nil
}
