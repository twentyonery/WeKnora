// Package service: session sandbox file browser.
//
// The可视化工作台 file manager needs CRUD over the sandbox bound to a
// session. It rides the exact same resolution as the interactive terminal:
// pin first (never "whatever the agent points at today"), lookup-only reads,
// and typed errors the UI can answer with an explicit resume button. Writes
// provision through the manager's own session path, so an upload behaves like
// a conversation-turn write and nothing else.
package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"path"

	"github.com/Tencent/WeKnora/internal/sandbox"
	"github.com/Tencent/WeKnora/internal/types"
)

// ErrSessionFilesUnsupported means the resolved backend cannot expose a file
// browser (e.g. the disabled manager). The handler maps it onto 409 so the UI
// can explain the backend choice instead of showing an empty list.
var ErrSessionFilesUnsupported = errors.New("sandbox: backend does not support a session file browser")

// SessionFileEntry is the API shape of one directory listing row.
type SessionFileEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Type    string `json:"type"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"mod_time_unix"`
}

// SessionFileDownload is one file's bytes plus the metadata needed to serve
// them (content type from the name, not from the provider).
type SessionFileDownload struct {
	Reader io.ReadCloser
	Name   string
	Size   int64
}

// browserFor resolves the session's pinned sandbox and narrows it to the
// file-browser capability. Every read operation goes through here; the
// lookup-only contract of resolveSessionManager is what keeps a file panel
// from waking a paused microVM.
func (s *SandboxTerminalService) browserFor(
	ctx context.Context, sessionID string,
) (sandbox.SessionFileBrowserStore, sandbox.SessionFileStore, error) {
	mgr, _, err := s.resolveSessionManager(ctx, sessionID)
	if err != nil {
		return nil, nil, err
	}
	provider, ok := mgr.(sandbox.SessionCapabilityProvider)
	if !ok || provider == nil {
		return nil, nil, ErrSessionFilesUnsupported
	}
	store := provider.SessionFileStore()
	if store == nil {
		return nil, nil, ErrSessionFilesUnsupported
	}
	browser, ok := mgr.(sandbox.SessionFileBrowserStore)
	if !ok {
		return nil, store, ErrSessionFilesUnsupported
	}
	return browser, store, nil
}

// ListSessionDir lists one directory of the session's workspace. An empty dir
// lists /workspace itself.
func (s *SandboxTerminalService) ListSessionDir(
	ctx context.Context, sessionID, dir string,
) ([]SessionFileEntry, error) {
	browser, _, err := s.browserFor(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	entries, err := browser.ListSessionDir(ctx, sessionID, dir)
	if err != nil {
		return nil, err
	}
	out := make([]SessionFileEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, SessionFileEntry{
			Name:    entry.Name,
			Path:    entry.Path,
			Type:    string(entry.Type),
			Size:    entry.Size,
			ModTime: entry.ModTime.Unix(),
		})
	}
	return out, nil
}

// ReadSessionFile downloads one file's bytes from the session workspace.
func (s *SandboxTerminalService) ReadSessionFile(
	ctx context.Context, sessionID, filePath string,
) (*SessionFileDownload, error) {
	_, store, err := s.browserFor(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	content, err := store.ReadSessionFile(ctx, sessionID, filePath)
	if err != nil {
		return nil, err
	}
	return &SessionFileDownload{
		Reader: io.NopCloser(bytesReader(content)),
		Name:   path.Base(filePath),
		Size:   int64(len(content)),
	}, nil
}

// WriteSessionWorkspaceFile uploads one file into the session workspace.
// /workspace/input stays read-only: the manager's write guard rejects it.
func (s *SandboxTerminalService) WriteSessionWorkspaceFile(
	ctx context.Context, sessionID, filePath string, content []byte,
) error {
	_, store, err := s.browserFor(ctx, sessionID)
	if err != nil {
		return err
	}
	if err := store.WriteSessionWorkspaceFile(ctx, sessionID, filePath, content); err != nil {
		return err
	}
	s.emitSandboxAudit(ctx, types.AuditActionSandboxFileUploaded, sessionID, map[string]any{
		"path": filePath, "size": len(content),
	})
	return nil
}

// EnsureSessionDir creates a directory in the session workspace.
func (s *SandboxTerminalService) EnsureSessionDir(
	ctx context.Context, sessionID, dir string,
) error {
	_, store, err := s.browserFor(ctx, sessionID)
	if err != nil {
		return err
	}
	if err := store.EnsureSessionDir(ctx, sessionID, dir); err != nil {
		return err
	}
	s.emitSandboxAudit(ctx, types.AuditActionSandboxDirCreated, sessionID, map[string]any{
		"path": dir,
	})
	return nil
}

// RemoveSessionWorkspacePath deletes a file or directory in the workspace.
func (s *SandboxTerminalService) RemoveSessionWorkspacePath(
	ctx context.Context, sessionID, targetPath string, recursive bool,
) error {
	browser, _, err := s.browserFor(ctx, sessionID)
	if err != nil {
		return err
	}
	if err := browser.RemoveSessionWorkspacePath(ctx, sessionID, targetPath, recursive); err != nil {
		return err
	}
	s.emitSandboxAudit(ctx, types.AuditActionSandboxFileDeleted, sessionID, map[string]any{
		"path": targetPath, "recursive": recursive,
	})
	return nil
}

// RenameSessionWorkspacePath moves a path within the session workspace.
func (s *SandboxTerminalService) RenameSessionWorkspacePath(
	ctx context.Context, sessionID, fromPath, toPath string,
) error {
	browser, _, err := s.browserFor(ctx, sessionID)
	if err != nil {
		return err
	}
	if err := browser.RenameSessionWorkspacePath(ctx, sessionID, fromPath, toPath); err != nil {
		return err
	}
	s.emitSandboxAudit(ctx, types.AuditActionSandboxFileRenamed, sessionID, map[string]any{
		"from": fromPath, "to": toPath,
	})
	return nil
}

// FileContentType picks a serve content type from the file name. download
// responses default to octet-stream for anything the mime tables do not know,
// which is what a download link wants.
func FileContentType(name string) string {
	if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
		return ct
	}
	return "application/octet-stream"
}

// bytesReader keeps the stdlib import local to this file.
func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }
