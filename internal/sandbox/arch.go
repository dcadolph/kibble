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
	// mu guards server, seen, and arch.
	mu sync.Mutex
	// server is the daemon architecture once looked up.
	server string
	// seen holds the images already checked, so each warns at most once.
	seen map[string]bool
	// arch holds each image's architecture once a lookup succeeded.
	arch map[string]string
}

// Platform names the platform a container ran on. A verdict reached under
// emulation is a narrower claim than one reached natively, since the reader's
// own machine runs the native build.
type Platform struct {
	// Arch is the architecture the container ran, the image's own when it
	// could be read and the daemon's otherwise.
	Arch string
	// Host is the architecture the daemon runs containers on natively.
	Host string
}

// Emulated reports whether the container ran on another architecture than
// the daemon's own.
func (p Platform) Emulated() bool {
	return p.Arch != "" && p.Host != "" && p.Arch != p.Host
}

// Name returns the platform as an OS and architecture pair, or "linux" alone
// when the architecture is unknown.
func (p Platform) Name() string {
	if p.Arch == "" {
		return "linux"
	}
	return "linux/" + p.Arch
}

// String returns the platform's name, followed by the daemon's architecture
// when the container ran emulated.
func (p Platform) String() string {
	if p.Emulated() {
		return fmt.Sprintf("%s (emulated on %s)", p.Name(), p.Host)
	}
	return p.Name()
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
	server := c.serverLocked(ctx)
	arch := c.imageLocked(ctx, image)
	if arch == "" || arch == server {
		return ""
	}
	return fmt.Sprintf("kibble: image %s is linux/%s and %s runs linux/%s, so steps in it run "+
		"emulated and slower, and a timeout there can be the emulation rather than the document. "+
		"Run `%s pull %s` for the native image, or set DOCKER_DEFAULT_PLATFORM to choose one.",
		image, arch, Bin(), server, Bin(), image)
}

// ImagePlatform returns the platform a container of image ran on.
func ImagePlatform(ctx context.Context, image string) Platform {
	return archCheck.Platform(ctx, image)
}

// HostPlatform returns the daemon's own platform, for a verdict no container
// was run for.
func HostPlatform(ctx context.Context) Platform {
	return archCheck.Platform(ctx, "")
}

// Platform returns the platform a container of image ran on: the platform
// DOCKER_DEFAULT_PLATFORM requested when set, else the local image's
// architecture, else the daemon's. It is asked after the run, when the image
// is local, and ignores ctx's cancellation, since a run that hit its deadline
// still needs its platform named. An empty image returns the daemon's own.
func (c *ArchCheck) Platform(ctx context.Context, image string) Platform {
	ctx = context.WithoutCancel(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	p := Platform{Host: c.serverLocked(ctx)}
	p.Arch = requestedArch(os.Getenv("DOCKER_DEFAULT_PLATFORM"))
	if p.Arch == "" && image != "" {
		p.Arch = c.imageLocked(ctx, image)
	}
	if p.Arch == "" {
		p.Arch = p.Host
	}
	return p
}

// serverLocked returns the daemon's architecture, looked up once, or the
// host's own when the client cannot say. The caller holds mu.
func (c *ArchCheck) serverLocked(ctx context.Context) string {
	if c.server == "" {
		c.server = runtime.GOARCH
		if arch, err := c.ServerArch(ctx); err == nil && arch != "" {
			c.server = normalizeArch(arch)
		}
	}
	return c.server
}

// imageLocked returns image's architecture, or empty when the image is not
// local. Only an answer is kept, so an image pulled after a failed lookup is
// read on the next ask. The caller holds mu.
func (c *ArchCheck) imageLocked(ctx context.Context, image string) string {
	if arch, ok := c.arch[image]; ok {
		return arch
	}
	arch, err := c.ImageArch(ctx, image)
	if err != nil || arch == "" {
		return ""
	}
	if c.arch == nil {
		c.arch = map[string]string{}
	}
	c.arch[image] = normalizeArch(arch)
	return c.arch[image]
}

// requestedArch returns the architecture of a platform such as linux/amd64
// or linux/arm64/v8, or empty when platform names none.
func requestedArch(platform string) string {
	parts := strings.Split(strings.TrimSpace(platform), "/")
	if len(parts) < 2 {
		return ""
	}
	return normalizeArch(parts[1])
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
