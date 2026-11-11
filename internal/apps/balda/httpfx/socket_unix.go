//go:build unix

package httpfx

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// The private directory and lifetime lock serialize socket replacement with
// cleanup. Go's default unlink-on-close would remove a successor's pathname.
func listenUnix(path string) (net.Listener, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fmt.Errorf("unix socket requires a canonical absolute path")
	}
	parent := filepath.Dir(path)
	if err := os.Mkdir(parent, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	info, err := os.Lstat(parent)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || !ownedUnixFile(info) || info.Mode().Perm() != 0o700 {
		return nil, fmt.Errorf("unix socket parent must be an owned private directory with mode 0700")
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open unix socket ownership lock: %w", err)
	}
	info, err = lock.Stat()
	if err == nil && (!info.Mode().IsRegular() || !ownedUnixFile(info) || info.Mode().Perm() != 0o600) {
		err = fmt.Errorf("unix socket lock must be an owned private regular file")
	}
	if err == nil {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	}
	if err != nil {
		return nil, errors.Join(fmt.Errorf("claim unix socket ownership: %w", err), lock.Close())
	}
	if err := removeStaleUnixSocket(path); err != nil {
		return nil, errors.Join(err, lock.Close())
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, errors.Join(err, lock.Close())
	}
	listener.SetUnlinkOnClose(false)
	info, err = os.Lstat(path)
	if err != nil {
		return nil, errors.Join(err, listener.Close(), lock.Close())
	}
	owned := &unixSocketListener{UnixListener: listener, path: path, info: info, lock: lock}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, errors.Join(err, owned.Close())
	}
	return owned, nil
}

func ownedUnixFile(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int64(stat.Uid) == int64(os.Geteuid())
}

func removeStaleUnixSocket(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 || !ownedUnixFile(info) {
		return fmt.Errorf("existing unix socket path is not an owned socket")
	}
	connection, err := net.DialTimeout("unix", path, 100*time.Millisecond)
	if err == nil {
		return errors.Join(fmt.Errorf("unix socket is already active"), connection.Close())
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Errorf("check existing unix socket: %w", err)
	}
	return removeOwnedUnixSocket(path, info)
}

func removeOwnedUnixSocket(path string, owned os.FileInfo) error {
	current, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !os.SameFile(current, owned) {
		return nil
	}
	return os.Remove(path)
}

type unixSocketListener struct {
	*net.UnixListener
	path string
	info os.FileInfo
	lock *os.File
	once sync.Once
	err  error
}

func (l *unixSocketListener) Close() error {
	l.once.Do(func() {
		l.err = errors.Join(l.UnixListener.Close(), removeOwnedUnixSocket(l.path, l.info), l.lock.Close())
	})
	return l.err
}
