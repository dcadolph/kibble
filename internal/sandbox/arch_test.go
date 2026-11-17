package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// errNotLocal stands in for an image the client has not pulled.
var errNotLocal = errors.New("no such image")

// testArchCheck returns a check whose daemon runs server and whose local
// images have the architectures in images. An image missing from images is
// not local.
func testArchCheck(server string, images map[string]string) *ArchCheck {
	return &ArchCheck{
		ImageArch: func(_ context.Context, image string) (string, error) {
			if arch, ok := images[image]; ok {
				return arch, nil
			}
			return "", errNotLocal
		},
		ServerArch: func(context.Context) (string, error) { return server, nil },
	}
}

// TestArchWarning checks when a run is told its image will be emulated. The
// local jq source build of 11-17 ran in an amd64 buildpack-deps on an arm64
// Mac, took 295 seconds against a 240 second budget, and read as TIMEOUT with
// nothing saying the image was the cause.
func TestArchWarning(t *testing.T) {
	tests := []struct {
		server   string
		images   map[string]string
		image    string
		platform string
		want     bool
	}{{ // Test 0: amd64 image on an arm64 daemon warns.
		server: "arm64", images: map[string]string{"buildpack-deps:stable": "amd64"},
		image: "buildpack-deps:stable", want: true,
	}, { // Test 1: a native image says nothing.
		server: "arm64", images: map[string]string{"buildpack-deps:stable": "arm64"},
		image: "buildpack-deps:stable",
	}, { // Test 2: an image not yet local is pulled native, so nothing to say.
		server: "arm64", image: "buildpack-deps:stable",
	}, { // Test 3: a platform the user asked for is not a surprise.
		server: "arm64", images: map[string]string{"golang:1.26": "amd64"},
		image: "golang:1.26", platform: "linux/amd64",
	}, { // Test 4: kernel names compare equal to Go names.
		server: "aarch64", images: map[string]string{"rust:1": "arm64"},
		image: "rust:1",
	}, { // Test 5: an arm64 image on an amd64 daemon warns too.
		server: "x86_64", images: map[string]string{"node:22": "arm64"},
		image: "node:22", want: true,
	}, { // Test 6: no image, nothing to check.
		server: "arm64",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Setenv("DOCKER_DEFAULT_PLATFORM", test.platform)
			t.Setenv("KIBBLE_DOCKER", "")
			got := testArchCheck(test.server, test.images).Warning(context.Background(), test.image)
			if (got != "") != test.want {
				t.Fatalf("Warning(%q) = %q, want warning %v", test.image, got, test.want)
			}
			if !test.want {
				return
			}
			for _, part := range []string{test.image, "emulated", "docker pull " + test.image,
				"DOCKER_DEFAULT_PLATFORM"} {
				if !strings.Contains(got, part) {
					t.Errorf("warning lacks %q:\n%s", part, got)
				}
			}
		})
	}
}

// TestArchWarningOnce checks that an image many steps share warns once, and
// that the daemon is asked its architecture once.
func TestArchWarningOnce(t *testing.T) {
	t.Setenv("DOCKER_DEFAULT_PLATFORM", "")
	c := testArchCheck("arm64", map[string]string{"a": "amd64", "b": "amd64"})
	serverCalls := 0
	server := c.ServerArch
	c.ServerArch = func(ctx context.Context) (string, error) {
		serverCalls++
		return server(ctx)
	}
	ctx := context.Background()
	if c.Warning(ctx, "a") == "" {
		t.Fatal("first check of a did not warn")
	}
	if got := c.Warning(ctx, "a"); got != "" {
		t.Errorf("second check of a warned again: %q", got)
	}
	if c.Warning(ctx, "b") == "" {
		t.Error("first check of b did not warn")
	}
	if serverCalls != 1 {
		t.Errorf("daemon asked %d times, want 1", serverCalls)
	}
}

// TestArchWarningServerUnknown checks that a client which cannot report the
// daemon's architecture falls back to the host's, as podman's version output
// lacks the field docker's has.
func TestArchWarningServerUnknown(t *testing.T) {
	t.Setenv("DOCKER_DEFAULT_PLATFORM", "")
	other := "amd64"
	if runtime.GOARCH == "amd64" {
		other = "arm64"
	}
	c := testArchCheck("", map[string]string{"native": runtime.GOARCH, "foreign": other})
	c.ServerArch = func(context.Context) (string, error) { return "", errors.New("no field") }
	ctx := context.Background()
	if got := c.Warning(ctx, "native"); got != "" {
		t.Errorf("native image warned: %q", got)
	}
	if c.Warning(ctx, "foreign") == "" {
		t.Error("foreign image did not warn")
	}
}

// TestArchLookups runs the two client queries against a stand-in client, so
// the inspect and version arguments and the trimming are checked without a
// daemon.
func TestArchLookups(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "fakedocker")
	script := `#!/bin/sh
case "$1 $2" in
"image inspect") [ "$5" = local ] && echo amd64 && exit 0; echo "no such image" >&2; exit 1 ;;
"version --format") echo arm64 ;;
*) exit 2 ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil { //nolint:gosec // Test client must be executable.
		t.Fatal(err)
	}
	t.Setenv("KIBBLE_DOCKER", bin)
	ctx := context.Background()
	if got, err := imageArch(ctx, "local"); err != nil || got != "amd64" {
		t.Errorf("imageArch(local) = %q, %v, want amd64", got, err)
	}
	if _, err := imageArch(ctx, "absent"); err == nil {
		t.Error("imageArch(absent) returned no error")
	}
	if got, err := serverArch(ctx); err != nil || got != "arm64" {
		t.Errorf("serverArch = %q, %v, want arm64", got, err)
	}
}

// TestPlatform checks which platform a run is said to have reached its verdict
// on. The report used to name the architecture kibble was compiled for, so an
// amd64 image emulated on an arm64 Mac was reported as linux/arm64.
func TestPlatform(t *testing.T) {
	tests := []struct {
		WantPlatform Platform
		WantString   string
		Images       map[string]string
		Image        string
		Requested    string
		WantEmulated bool
	}{{ // Test 0: an amd64 image on an arm64 daemon ran emulated.
		Images: map[string]string{"buildpack-deps:stable": "amd64"}, Image: "buildpack-deps:stable",
		WantPlatform: Platform{Arch: "amd64", Host: "arm64"}, WantEmulated: true,
		WantString: "linux/amd64 (emulated on arm64)",
	}, { // Test 1: a native image ran native.
		Images: map[string]string{"golang:1.26": "arm64"}, Image: "golang:1.26",
		WantPlatform: Platform{Arch: "arm64", Host: "arm64"}, WantString: "linux/arm64",
	}, { // Test 2: an image that cannot be read falls back to the daemon.
		Image: "gone", WantPlatform: Platform{Arch: "arm64", Host: "arm64"}, WantString: "linux/arm64",
	}, { // Test 3: a requested platform is the one the run used.
		Images: map[string]string{"golang:1.26": "arm64"}, Image: "golang:1.26", Requested: "linux/amd64",
		WantPlatform: Platform{Arch: "amd64", Host: "arm64"}, WantEmulated: true,
		WantString: "linux/amd64 (emulated on arm64)",
	}, { // Test 4: a requested platform with a variant still names its architecture.
		Image: "golang:1.26", Requested: "linux/arm64/v8",
		WantPlatform: Platform{Arch: "arm64", Host: "arm64"}, WantString: "linux/arm64",
	}, { // Test 5: no image is the daemon's own platform.
		WantPlatform: Platform{Arch: "arm64", Host: "arm64"}, WantString: "linux/arm64",
	}, { // Test 6: kernel names from the image compare equal to Go names.
		Images: map[string]string{"rust:1": "aarch64"}, Image: "rust:1",
		WantPlatform: Platform{Arch: "arm64", Host: "arm64"}, WantString: "linux/arm64",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Setenv("DOCKER_DEFAULT_PLATFORM", test.Requested)
			got := testArchCheck("aarch64", test.Images).Platform(context.Background(), test.Image)
			if diff := cmp.Diff(test.WantPlatform, got); diff != "" {
				t.Errorf("platform mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(test.WantEmulated, got.Emulated()); diff != "" {
				t.Errorf("emulated mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(test.WantString, got.String()); diff != "" {
				t.Errorf("string mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestPlatformAfterPull checks that an image not local when the warning
// looked it up is read once the run has pulled it, rather than the failed
// lookup being kept and the daemon's architecture reported.
func TestPlatformAfterPull(t *testing.T) {
	t.Setenv("DOCKER_DEFAULT_PLATFORM", "")
	images := map[string]string{}
	c := testArchCheck("arm64", images)
	ctx := context.Background()
	if w := c.Warning(ctx, "img"); w != "" {
		t.Fatalf("image not local warned: %q", w)
	}
	images["img"] = "amd64"
	if got := c.Platform(ctx, "img"); !got.Emulated() {
		t.Errorf("Platform after pull = %+v, want amd64 emulated on arm64", got)
	}
}

// TestPlatformAfterDeadline checks that a run whose context expired, as a
// TIMEOUT does, still has its platform read. The emulated jq build that timed
// out is the run that most needed it named.
func TestPlatformAfterDeadline(t *testing.T) {
	t.Setenv("DOCKER_DEFAULT_PLATFORM", "")
	c := &ArchCheck{
		ImageArch: func(ctx context.Context, _ string) (string, error) {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			return "amd64", nil
		},
		ServerArch: func(ctx context.Context) (string, error) {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			return "arm64", nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	want := Platform{Arch: "amd64", Host: "arm64"}
	if diff := cmp.Diff(want, c.Platform(ctx, "img")); diff != "" {
		t.Errorf("platform mismatch (-want +got):\n%s", diff)
	}
}

// TestPlatformName checks the unknown architecture renders as the OS alone
// rather than as a claim about an architecture.
func TestPlatformName(t *testing.T) {
	t.Parallel()
	var p Platform
	if diff := cmp.Diff("linux", p.Name()); diff != "" {
		t.Errorf("name mismatch (-want +got):\n%s", diff)
	}
	if p.Emulated() {
		t.Error("zero platform reads as emulated")
	}
}
