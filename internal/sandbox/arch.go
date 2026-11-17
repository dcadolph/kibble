package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// ArchCheck finds images kibble is about to run that were built for another
// architecture than the daemon's. Docker runs such an image without asking
// when it is already local and no platform was requested, under emulation and
// several times slower. A source build that fits its budget natively then
// times out, and the timeout reads like the document's limit, not kibble's.
type ArchCheck struct {
	// ImageArch returns the architecture of a local image, or an error when
	// the image is not local, in which case a run pulls the native one.
	ImageArch func(ctx context.Context, image string) (string, error)
	// ServerArch returns the architecture the daemon runs containers on.
	ServerArch func(ctx context.Context) (string, error)
	// mu guards server and seen.
	mu sync.Mutex
	// server is the daemon architecture once looked up.
	server string
	// seen holds the images already checked, so each warns at most once.
	seen map[string]bool
}

// archCheck is the check every run shares, so an image used by many steps is
// looked up and reported once per process.
var archCheck = &ArchCheck{ImageArch: imageArch, ServerArch: serverArch}

// ArchWarning returns a warning when image is local, built for another
// architecture than the daemon's, and no platform was requested. It returns
// empty for an image already checked in this process.
func ArchWarning(ctx context.Context, image string) string {
	return archCheck.Warning(ctx, image)
}

// Warning returns the mismatch warning for image the first time image is
// checked, and empty after that or when there is nothing to warn about. A
// lookup that fails says nothing, since the run reports Docker's own error.
func (c *ArchCheck) Warning(ctx context.Context, image string) string {
	if image == "" || os.Getenv("DOCKER_DEFAULT_PLATFORM") != "" {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.seen[image] {
		return ""
	}
	if c.seen == nil {
		c.seen = map[string]bool{}
	}
	c.seen[image] = true
	if c.server == "" {
		c.server = runtime.GOARCH
		if arch, err := c.ServerArch(ctx); err == nil && arch != "" {
			c.server = normalizeArch(arch)
		}
	}
	arch, err := c.ImageArch(ctx, image)
	if err != nil || arch == "" {
		return ""
	}
	arch = normalizeArch(arch)
	if arch == c.server {
		return ""
	}
	return fmt.Sprintf("kibble: image %s is linux/%s and %s runs linux/%s, so steps in it run "+
		"emulated and slower, and a timeout there can be the emulation rather than the document. "+
		"Run `%s pull %s` for the native image, or set DOCKER_DEFAULT_PLATFORM to choose one.",
		image, arch, Bin(), c.server, Bin(), image)
}

// normalizeArch maps the kernel names some clients report to the Go names
// Docker uses for images, so arm64 and aarch64 compare equal.
func normalizeArch(arch string) string {
	switch arch = strings.TrimSpace(arch); arch {
	case "aarch64":
		return "arm64"
	case "x86_64":
		return "amd64"
	}
	return arch
}

// imageArch asks the client for a local image's architecture.
func imageArch(ctx context.Context, image string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, Bin(), "image", "inspect", "--format", "{{.Architecture}}", image).Output()
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", image, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// serverArch asks the client for the daemon's architecture. A client that
// does not answer, as some drop-in replacements do not, leaves the caller on
// the host's own architecture.
func serverArch(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, Bin(), "version", "--format", "{{.Server.Arch}}").Output()
	if err != nil {
		return "", fmt.Errorf("server arch: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
