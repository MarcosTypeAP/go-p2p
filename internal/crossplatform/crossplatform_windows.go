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
		opErr = syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, v)
	})
	if err != nil {
		return err
	}
	if opErr != nil {
		return opErr
	}
	return nil
}

const WSAECONNREFUSED syscall.Errno = 10061

func IsConnectionRefusedError(err error) bool {
	var errno syscall.Errno
	return errors.As(err, &errno) && (errno == WSAECONNREFUSED || errno == syscall.ECONNREFUSED)
}

const WSAEADDRINUSE syscall.Errno = 10048

func IsBindError(err error) bool {
	var errno syscall.Errno
	return errors.As(err, &errno) && (errno == WSAEADDRINUSE || errno == syscall.EADDRINUSE || errno == syscall.WSAEACCES || errno == syscall.EACCES)
}

func IsLinuxPermissionError(err error) bool {
	return false
}
