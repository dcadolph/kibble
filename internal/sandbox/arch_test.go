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
