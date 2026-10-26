//go:build linux

package config

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestInputMaskerSpecialFiles(t *testing.T) {
	// A subprocess deadline also catches regressions that leave open/read blocked.
	if os.Getenv("ORPHEUS_SPECIAL_FILE_CHILD") != "1" {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestInputMaskerSpecialFiles$")
		cmd.Env = append(os.Environ(), "ORPHEUS_SPECIAL_FILE_CHILD=1", "ORPHEUS_SPECIAL_FILE_DIR="+t.TempDir())
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("special-file checks failed (deadline=%v): %v\n%s", ctx.Err(), err, output)
		}
		return
	}
	dir := os.Getenv("ORPHEUS_SPECIAL_FILE_DIR")
	fifo := filepath.Join(dir, "connected-fifo")
	idleFIFO := filepath.Join(dir, "idle-fifo")
	for _, path := range []string{fifo, idleFIFO} {
		if err := syscall.Mkfifo(path, 0600); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(dir, "fifo-link")
	if err := os.Symlink(idleFIFO, link); err != nil {
		t.Fatal(err)
	}
	fd, err := syscall.Open(fifo, syscall.O_RDWR|syscall.O_NONBLOCK, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Close(fd) }()
	socket := filepath.Join(dir, "socket")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	before, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{fifo, idleFIFO, link, dir, socket, "/dev/null", "/dev/zero"} {
		for range 100 {
			token, err := (InputMasker{TokenFile: path}).Token()
			if token != "" || err == nil || err.Error() != "input masker token unavailable" {
				t.Fatal("special file must fail with a sanitized error")
			}
		}
	}
	after, err := os.ReadDir("/proc/self/fd")
	if err != nil || len(after) != len(before) {
		t.Fatal("special-file rejection leaked file descriptors", err)
	}
}

func TestInputMaskerTokenFileRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	m := InputMasker{TokenFile: path}
	for _, value := range []string{"synthetic-first\n", "synthetic-rotated", "", strings.Repeat("x", 4097), "bad\nheader", "bad\x00token", "не-токен", strings.Repeat("x", 4096)} {
		// Rename models atomic secret delivery: each Token call opens the new inode.
		next := filepath.Join(dir, "next")
		if err := os.WriteFile(next, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(next, path); err != nil {
			t.Fatal(err)
		}
		token, err := m.Token()
		valid := value == "synthetic-first\n" || value == "synthetic-rotated" || len(value) == 4096
		if valid {
			if err != nil || token != strings.TrimSpace(value) {
				t.Fatal("valid rotated token rejected", err)
			}
		} else if err == nil || token != "" || err.Error() != "invalid input masker token" {
			t.Fatal("invalid token accepted or unsafe diagnostic")
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if token, err := m.Token(); token != "" || err == nil || err.Error() != "input masker token unavailable" {
		t.Fatal("removed token accepted or unsafe diagnostic")
	}
	if err := os.WriteFile(path, []byte("synthetic-restored"), 0600); err != nil {
		t.Fatal(err)
	}
	// Secret-volume symlinks to regular files remain supported.
	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if token, err := (InputMasker{TokenFile: link}).Token(); err != nil || token != "synthetic-restored" {
		t.Fatal("restored secret or symlink delivery failed", err)
	}
}

func TestInputMaskerRegularFileBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("synthetic-token"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := openInputMaskerTokenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	conn, err := file.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var flags uintptr
	var errno syscall.Errno
	if err := conn.Control(func(fd uintptr) {
		flags, _, errno = syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_GETFL, 0)
	}); err != nil || errno != 0 {
		t.Fatal("F_GETFL failed", err, errno)
	}
	if flags&syscall.O_NONBLOCK != 0 {
		t.Fatal("regular token file retained O_NONBLOCK")
	}
}
