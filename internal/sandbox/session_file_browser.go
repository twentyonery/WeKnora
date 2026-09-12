package sandbox

// The UI file browser needs three operations SessionFileStore deliberately
// does not carry: single-directory listing, workspace-wide delete and rename.
// They are opt-in here rather than added to SessionFileStore so the existing
// implementors keep compiling.
//
// All paths reuse the same lexical rules the rest of the session surface
// enforces: reads stay inside /workspace (cleanSessionWorkDir), writes and
// deletes never touch /workspace itself or the read-only attachment tree
// (cleanSessionWorkspaceWritePath). Every operation is lookup-only — none of
// them provision a sandbox — so opening a file panel cannot wake a paused
// microVM.
//
// Rename is implemented as copy+delete on top of the neutral client rather
// than as a provider call: that keeps it available on every backend
// (including the local subprocess client, whose sandbox is a plain
// directory). The extra data movement is bounded by what one session's
// workspace already holds.

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
)

// ErrSessionFileExists is returned when a rename would overwrite an existing
// target. The UI maps it onto a confirmation-free 400: the browser never
// silently replaces files.
var ErrSessionFileExists = errors.New("sandbox: target path already exists")

// SessionFileBrowserStore is the file-manager capability of a session-scoped
// backend. SessionBoundManager implements it for every remote provider plus
// the local subprocess backend.
type SessionFileBrowserStore interface {
	// ListSessionDir lists one directory without recursing. An empty dir
	// means "the workspace root"; the result is a flat listing.
	ListSessionDir(ctx context.Context, sessionID, dir string) ([]RemoteDirEntry, error)

	// RemoveSessionWorkspacePath deletes a file or directory under
	// /workspace. /workspace/input stays read-only; the workspace root and
	// the output root cannot be deleted themselves.
	RemoveSessionWorkspacePath(ctx context.Context, sessionID, targetPath string, recursive bool) error

	// RenameSessionWorkspacePath moves fromPath to toPath within the
	// workspace. Refuses to overwrite an existing target
	// (ErrSessionFileExists).
	RenameSessionWorkspacePath(ctx context.Context, sessionID, fromPath, toPath string) error
}

var _ SessionFileBrowserStore = (*SessionBoundManager)(nil)

// ListSessionDir lists one directory of the session's live sandbox. Errors
// when no sandbox is bound: a browser that reached this method did so through
// the pin-first resolution, which already reported "no sandbox" as its own
// state.
func (m *SessionBoundManager) ListSessionDir(
	ctx context.Context, sessionID, dir string,
) ([]RemoteDirEntry, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, errors.New("sandbox: session ID required for ListSessionDir")
	}
	clean, err := cleanSessionWorkDir(dir, false)
	if err != nil {
		return nil, err
	}
	handle, ok, err := m.lookupSessionHandle(ctx, sessionID)
	if err != nil || !ok {
		return nil, err
	}
	entries, err := m.client.ListDir(ctx, handle, clean)
	if err != nil {
		return nil, fmt.Errorf("sandbox: list session dir %s: %w", clean, err)
	}
	return entries, nil
}

// RemoveSessionWorkspacePath deletes one path under /workspace in the
// session's live sandbox. It never provisions a sandbox and never touches the
// attachment tree: cleanSessionWorkspaceWritePath already refuses
// /workspace itself, both roots, and anything under /workspace/input.
func (m *SessionBoundManager) RemoveSessionWorkspacePath(
	ctx context.Context, sessionID, targetPath string, recursive bool,
) error {
	if err := m.requireRemoteBackend(); err != nil {
		return err
	}
	if strings.TrimSpace(sessionID) == "" {
		return errors.New("sandbox: session ID required for workspace remove")
	}
	clean, err := cleanSessionWorkspaceWritePath(targetPath)
	if err != nil {
		return err
	}
	handle, ok, err := m.lookupSessionHandle(ctx, sessionID)
	if err != nil || !ok {
		return err
	}
	if err := m.client.Remove(ctx, handle, clean); err != nil {
		return fmt.Errorf("sandbox: remove session path %s: %w", clean, err)
	}
	return nil
}

// RenameSessionWorkspacePath moves fromPath to toPath inside the session
// workspace. Directories are copied entry by entry and then deleted; files
// are read once and rewritten. The target must not exist.
func (m *SessionBoundManager) RenameSessionWorkspacePath(
	ctx context.Context, sessionID, fromPath, toPath string,
) error {
	if err := m.requireRemoteBackend(); err != nil {
		return err
	}
	if strings.TrimSpace(sessionID) == "" {
		return errors.New("sandbox: session ID required for workspace rename")
	}
	from, err := cleanSessionWorkspaceWritePath(fromPath)
	if err != nil {
		return fmt.Errorf("sandbox: rename source %w", err)
	}
	to, err := cleanSessionWorkspaceWritePath(toPath)
	if err != nil {
		return fmt.Errorf("sandbox: rename target %w", err)
	}
	if from == to {
		return nil
	}
	// A rename into its own subtree would walk forever on the copy path.
	if strings.HasPrefix(to, from+"/") {
		return fmt.Errorf("sandbox: cannot move %s into its own subtree %s", from, to)
	}

	handle, ok, err := m.lookupSessionHandle(ctx, sessionID)
	if err != nil || !ok {
		return err
	}

	if _, err := m.client.Stat(ctx, handle, to); err == nil {
		return fmt.Errorf("%w: %s", ErrSessionFileExists, to)
	}

	stat, err := m.client.Stat(ctx, handle, from)
	if err != nil {
		return fmt.Errorf("sandbox: stat rename source %s: %w", from, err)
	}
	if stat != nil && stat.Type == RemoteEntryDir {
		if err := m.copySessionDir(ctx, handle, from, to); err != nil {
			return err
		}
	} else {
		content, err := m.client.ReadFile(ctx, handle, from)
		if err != nil {
			return fmt.Errorf("sandbox: read rename source %s: %w", from, err)
		}
		if err := ignoreExistingDir(m.client.MakeDir(ctx, handle, path.Dir(to))); err != nil {
			return fmt.Errorf("sandbox: create rename parent: %w", err)
		}
		if err := m.client.WriteFile(ctx, handle, to, content); err != nil {
			return fmt.Errorf("sandbox: write rename target %s: %w", to, err)
		}
	}
	if err := m.client.Remove(ctx, handle, from); err != nil {
		return fmt.Errorf("sandbox: delete rename source %s after copy: %w", from, err)
	}
	return nil
}

// copySessionDir recreates src as dst entry by entry, then the caller deletes
// src. RemoteEntryOther nodes (symlinks, devices) are skipped: the neutral
// client cannot express them and a browser rename must not turn a symlink
// into a materialised copy.
func (m *SessionBoundManager) copySessionDir(
	ctx context.Context, handle RemoteSandboxHandle, src, dst string,
) error {
	if err := ignoreExistingDir(m.client.MakeDir(ctx, handle, dst)); err != nil {
		return fmt.Errorf("sandbox: create dir %s: %w", dst, err)
	}
	entries, err := m.client.ListDir(ctx, handle, src)
	if err != nil {
		return fmt.Errorf("sandbox: list dir %s: %w", src, err)
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		nextSrc := path.Join(src, entry.Name)
		nextDst := path.Join(dst, entry.Name)
		switch entry.Type {
		case RemoteEntryDir:
			if err := m.copySessionDir(ctx, handle, nextSrc, nextDst); err != nil {
				return err
			}
		case RemoteEntryFile:
			content, err := m.client.ReadFile(ctx, handle, nextSrc)
			if err != nil {
				return fmt.Errorf("sandbox: read %s: %w", nextSrc, err)
			}
			if err := m.client.WriteFile(ctx, handle, nextDst, content); err != nil {
				return fmt.Errorf("sandbox: write %s: %w", nextDst, err)
			}
		}
	}
	return nil
}
