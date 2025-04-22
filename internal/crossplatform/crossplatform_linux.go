package crossplatform

import (
	"errors"
	"syscall"
)

func SetReuseAddr(conn syscall.RawConn, value bool) error {
	v := 0
	if value {
		v = 1
	}
	var opErr error
	err := conn.Control(func(fd uintptr) {
		opErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, v)
	})
	if err != nil {
		return err
	}
	if opErr != nil {
		return opErr
	}
	return nil
}

func IsConnectionRefusedError(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED)
}

func IsBindError(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE)
}

func IsLinuxPermissionError(err error) bool {
	return errors.Is(err, syscall.EPERM)
}
