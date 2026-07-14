package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const (
	passwordArtifactPermissions = os.FileMode(0o600)
	maxPasswordArtifactBytes    = 4096
)

// passwordArtifact identifies the exact file created or loaded before a
// password reset. Its identity is retained so post-commit cleanup cannot delete
// a different file that later appeared at the same path.
type passwordArtifact struct {
	path     string
	identity os.FileInfo
}

// createPasswordArtifact atomically creates a write-only password handoff file.
// The on-disk format is exactly the password bytes with no line terminator or
// metadata. O_EXCL prevents an existing file or symlink from being overwritten.
func createPasswordArtifact(path, password string) (*passwordArtifact, error) {
	if path == "" {
		return nil, errors.New("create password artifact: path is empty")
	}
	if password == "" {
		return nil, errors.New("create password artifact: password is empty")
	}
	if bytes.ContainsAny([]byte(password), "\r\n") {
		return nil, errors.New("create password artifact: line terminators are not allowed")
	}
	if len(password) > maxPasswordArtifactBytes {
		return nil, errors.New("create password artifact: password is too large")
	}

	file, err := os.OpenFile(
		path,
		os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW,
		passwordArtifactPermissions,
	)
	if err != nil {
		return nil, fmt.Errorf("create password artifact file %q: %w", path, err)
	}
	cleanup := func() {
		_ = file.Close()
		_ = os.Remove(path)
	}

	// Chmod after creation neutralizes a restrictive or permissive process umask
	// difference and makes the required artifact mode explicit.
	if err := file.Chmod(passwordArtifactPermissions); err != nil {
		cleanup()
		return nil, fmt.Errorf("set password artifact permissions %q: %w", path, err)
	}
	written, err := io.WriteString(file, password)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("write password artifact %q: %w", path, err)
	}
	if written != len(password) {
		cleanup()
		return nil, fmt.Errorf("write password artifact %q: %w", path, io.ErrShortWrite)
	}
	if err := file.Sync(); err != nil {
		cleanup()
		return nil, fmt.Errorf("sync password artifact %q: %w", path, err)
	}
	info, err := file.Stat()
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("stat password artifact %q: %w", path, err)
	}
	if err := validatePasswordArtifactInfo(info); err != nil {
		cleanup()
		return nil, fmt.Errorf("validate password artifact %q: %w", path, err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("close password artifact %q: %w", path, err)
	}
	if err := syncPasswordArtifactDirectory(path); err != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("sync password artifact directory %q: %w", filepath.Dir(path), err)
	}

	return &passwordArtifact{path: path, identity: info}, nil
}

// loadPasswordArtifact securely opens and validates an existing artifact. It
// returns the exact stored password; it never trims whitespace or a trailing
// newline. This implementation's format deliberately forbids line terminators.
func loadPasswordArtifact(path string) (*passwordArtifact, string, error) {
	if path == "" {
		return nil, "", errors.New("load password artifact: path is empty")
	}

	file, info, err := openValidatedPasswordArtifact(path)
	if err != nil {
		return nil, "", err
	}
	defer file.Close()

	content, err := io.ReadAll(io.LimitReader(file, maxPasswordArtifactBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("read password artifact %q: %w", path, err)
	}
	if len(content) == 0 {
		return nil, "", fmt.Errorf("read password artifact %q: artifact is empty", path)
	}
	if len(content) > maxPasswordArtifactBytes {
		return nil, "", fmt.Errorf("read password artifact %q: artifact is too large", path)
	}
	if bytes.ContainsAny(content, "\r\n") {
		return nil, "", fmt.Errorf("read password artifact %q: line terminators are not allowed", path)
	}

	return &passwordArtifact{path: path, identity: info}, string(content), nil
}

// Remove deletes the artifact after the caller has committed the new password
// to the database. It refuses to follow symlinks or remove a replacement file.
// A missing path is already clean and is therefore treated as success.
func (a *passwordArtifact) Remove() error {
	if a == nil || a.path == "" || a.identity == nil {
		return errors.New("remove password artifact: invalid artifact handle")
	}

	file, current, err := openValidatedPasswordArtifact(a.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !os.SameFile(a.identity, current) {
		_ = file.Close()
		return fmt.Errorf("remove password artifact %q: file identity changed", a.path)
	}

	// Keep the validated descriptor open until unlink completes. On Unix this
	// pins the inode while the pathname is removed.
	if err := os.Remove(a.path); err != nil {
		_ = file.Close()
		return fmt.Errorf("remove password artifact %q: %w", a.path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close removed password artifact %q: %w", a.path, err)
	}
	if err := syncPasswordArtifactDirectory(a.path); err != nil {
		return fmt.Errorf("sync removed password artifact directory %q: %w", filepath.Dir(a.path), err)
	}
	return nil
}

func openValidatedPasswordArtifact(path string) (*os.File, os.FileInfo, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open password artifact %q: %w", path, err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, fmt.Errorf("stat password artifact %q: %w", path, err)
	}
	if err := validatePasswordArtifactInfo(info); err != nil {
		_ = file.Close()
		return nil, nil, fmt.Errorf("validate password artifact %q: %w", path, err)
	}
	return file, info, nil
}

func validatePasswordArtifactInfo(info os.FileInfo) error {
	if info == nil || !info.Mode().IsRegular() {
		return errors.New("artifact is not a regular file")
	}
	if info.Mode().Perm()&^passwordArtifactPermissions != 0 {
		return errors.New("artifact permissions are broader than 0600")
	}
	return nil
}

func syncPasswordArtifactDirectory(path string) error {
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
