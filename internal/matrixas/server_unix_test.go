//go:build !windows

package matrixas

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"maunium.net/go/mautrix/appservice"
)

func TestAppServiceUnixStartupPreservesPreexistingPaths(t *testing.T) {
	tests := []struct {
		name   string
		create func(*testing.T, string) func()
	}{
		{
			name: "live socket",
			create: func(t *testing.T, path string) func() {
				listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
				if err != nil {
					t.Fatal(err)
				}
				listener.SetUnlinkOnClose(false)
				return func() { _ = listener.Close() }
			},
		},
		{
			name: "stale socket",
			create: func(t *testing.T, path string) func() {
				listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
				if err != nil {
					t.Fatal(err)
				}
				listener.SetUnlinkOnClose(false)
				if err = listener.Close(); err != nil {
					t.Fatal(err)
				}
				return func() {}
			},
		},
		{
			name: "file",
			create: func(t *testing.T, path string) func() {
				if err := os.WriteFile(path, []byte("operator-owned"), 0o600); err != nil {
					t.Fatal(err)
				}
				return func() {}
			},
		},
		{
			name: "symlink",
			create: func(t *testing.T, path string) func() {
				target := filepath.Join(filepath.Dir(path), "target")
				if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
				return func() {}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := shortUnixSocketPath(t)
			cleanup := tc.create(t, path)
			defer cleanup()
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}

			s := newUnixLifecycleTestServer(path)
			runtime, err := s.startAppServiceHTTP()
			if runtime != nil {
				s.stopAppServiceHTTP(runtime)
			}
			if err == nil {
				t.Fatal("startAppServiceHTTP succeeded with a pre-existing Unix path")
			}
			if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "remove it manually") {
				t.Fatalf("startup error %q is not actionable", err)
			}
			after, statErr := os.Lstat(path)
			if statErr != nil {
				t.Fatalf("pre-existing path was removed: %v", statErr)
			}
			if !os.SameFile(before, after) {
				t.Fatal("pre-existing path was replaced")
			}
		})
	}
}

func TestAppServiceUnixShutdownRemovesOwnedSocket(t *testing.T) {
	path := shortUnixSocketPath(t)
	s := newUnixLifecycleTestServer(path)
	runtime, err := s.startAppServiceHTTP()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Lstat(path); err != nil {
		t.Fatalf("owned socket was not created: %v", err)
	}

	s.stopAppServiceHTTP(runtime)
	if _, err = os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned socket remains after shutdown: %v", err)
	}
}

func TestAppServiceUnixShutdownPreservesReplacementCreatedAfterListenerClose(t *testing.T) {
	path := shortUnixSocketPath(t)
	s := newUnixLifecycleTestServer(path)
	runtime, err := s.startAppServiceHTTP()
	if err != nil {
		t.Fatal(err)
	}

	if err = runtime.listener.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runtime.done:
	case <-time.After(time.Second):
		t.Fatal("HTTP server did not report listener close")
	}
	if err = os.Remove(path); err != nil {
		t.Fatalf("owned socket path disappeared when listener closed: %v", err)
	}
	const replacement = "replacement"
	if err = os.WriteFile(path, []byte(replacement), 0o600); err != nil {
		t.Fatal(err)
	}

	s.stopAppServiceHTTP(runtime)
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("replacement path was removed: %v", err)
	}
	if string(got) != replacement {
		t.Fatalf("replacement contents = %q, want %q", got, replacement)
	}
}

func TestAppServiceUnixImmediateCancellationRemovesOwnedSocket(t *testing.T) {
	path := shortUnixSocketPath(t)
	accounts := newAccountStore(filepath.Join(t.TempDir(), "accounts.json"))
	s, _ := newSpacePersistenceTestServerWithRecorder(t, accounts)
	s.as.Host = appservice.HostConfig{Hostname: path}
	s.api = &builtinEmojiCommandTestAPI{loggedIn: false}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := s.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context cancellation", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned socket remains after immediate cancellation: %v", err)
	}
}

func newUnixLifecycleTestServer(path string) *Server {
	as := appservice.Create()
	as.Host = appservice.HostConfig{Hostname: path}
	return &Server{as: as}
}

func shortUnixSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "matrixas-unix-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "as.sock")
}
