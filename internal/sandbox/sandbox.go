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
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
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

// MinFreeBytes is the free space kibble needs inside Docker before a run. A
// daemon out of space fails installs with errors that read like the
// document's fault, so the run refuses to start instead.
const MinFreeBytes int64 = 2 << 30

// ErrDF reports df output kibble cannot read a free-space figure from.
var ErrDF = errors.New("df output not understood")

// FreeBytes returns the space available on the root filesystem of a container
// started from image. On Docker Desktop that is the VM's disk, which images,
// volumes, and build cache from every project share. kibble only measures:
// what to remove is the owner's decision.
func FreeBytes(ctx context.Context, image string) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, Bin(), "run", "--rm", image, "df", "-Pk", "/").CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("df in %s failed: %s", image, strings.TrimSpace(string(out)))
	}
	return parseDF(string(out))
}

// parseDF reads the available column of POSIX df -Pk output, in kilobyte
// blocks, and returns it in bytes.
func parseDF(out string) (int64, error) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	fields := strings.Fields(lines[len(lines)-1])
	if len(lines) < 2 || len(fields) < 6 {
		return 0, fmt.Errorf("%w: %q", ErrDF, out)
	}
	kb, err := strconv.ParseInt(fields[3], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: available column %q: %w", ErrDF, fields[3], err)
	}
	return kb * 1024, nil
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
