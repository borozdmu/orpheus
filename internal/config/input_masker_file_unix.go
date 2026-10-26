//go:build !windows

package config

import (
	"os"
	"syscall"
)

func setInputMaskerFileBlocking(file *os.File) error {
	conn, err := file.SyscallConn()
	if err != nil {
		return err
	}
	var flagErr error
	// Restore ordinary file reads only after fstat verified the opened inode.
	if err := conn.Control(func(fd uintptr) {
		flagErr = syscall.SetNonblock(int(fd), false)
	}); err != nil {
		return err
	}
	return flagErr
}
