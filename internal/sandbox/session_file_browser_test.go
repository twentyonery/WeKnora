package sandbox

// The file browser is tested against the real local subprocess client: its
// sandbox is a plain directory, so List/Read/Write/Remove/Rename are
// observable on disk and the path jail can be asserted without a stub.
// The remote-provider behaviour (jail refusals) is identical because every
// verb goes through the same clean* helpers.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
)

func browserCtx() context.Context {
	return context.WithValue(context.Background(), types.TenantIDContextKey, uint64(10000))
}

func newLocalBrowserManager(t *testing.T) (*SessionBoundManager, string) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Type = SandboxTypeLocal
	cfg.LocalWorkspaceRoot = t.TempDir()
	client, err := NewLocalSubprocessClient(cfg)
	require.NoError(t, err)
	mgr, err := NewSessionBoundManager(SessionBoundManagerConfig{
		Config:  cfg,
		Client:  client,
		Store:   NewMemorySessionSandboxBindingStore(),
		Checker: PermissiveSessionExistenceChecker{},
	})
	require.NoError(t, err)
	return mgr, cfg.LocalWorkspaceRoot
}

func TestSessionFileBrowserCRUDOnLocal(t *testing.T) {
	ctx := browserCtx()
	mgr, _ := newLocalBrowserManager(t)
	store := mgr.SessionFileStore()
	browser := mgr

	// Upload provisions the sandbox on first write, exactly like a
	// conversation turn does.
	require.NoError(t, store.WriteSessionWorkspaceFile(ctx, "s-1", "/workspace/hello.txt", []byte("hi")))

	entries, err := browser.ListSessionDir(ctx, "s-1", "/workspace")
	require.NoError(t, err)
	// The seeded input/output roots are part of every backend's layout.
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name)
	}
	require.ElementsMatch(t, []string{"hello.txt", "input", "output"}, names)

	content, err := store.ReadSessionFile(ctx, "s-1", "/workspace/hello.txt")
	require.NoError(t, err)
	require.Equal(t, "hi", string(content))

	// Mkdir then rename into it: rename is copy+delete on the neutral
	// client, and the file must end up only at the destination.
	require.NoError(t, store.EnsureSessionDir(ctx, "s-1", "/workspace/sub"))
	require.NoError(t, browser.RenameSessionWorkspacePath(ctx, "s-1",
		"/workspace/hello.txt", "/workspace/sub/renamed.txt"))

	entries, err = browser.ListSessionDir(ctx, "s-1", "/workspace")
	require.NoError(t, err)
	for _, entry := range entries {
		require.NotEqual(t, "hello.txt", entry.Name, "the source must be gone after a rename")
	}

	entries, err = browser.ListSessionDir(ctx, "s-1", "/workspace/sub")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "renamed.txt", entries[0].Name)

	// Renaming onto itself is a no-op, not an error.
	require.NoError(t, browser.RenameSessionWorkspacePath(ctx, "s-1",
		"/workspace/sub/renamed.txt", "/workspace/sub/renamed.txt"))
	// Renaming onto an existing different target is refused.
	require.NoError(t, store.WriteSessionWorkspaceFile(ctx, "s-1", "/workspace/other.txt", []byte("x")))
	require.ErrorIs(t, browser.RenameSessionWorkspacePath(ctx, "s-1",
		"/workspace/other.txt", "/workspace/sub/renamed.txt"), ErrSessionFileExists)

	// Delete then re-list.
	require.NoError(t, browser.RemoveSessionWorkspacePath(ctx, "s-1", "/workspace/sub", true))
	entries, err = browser.ListSessionDir(ctx, "s-1", "/workspace")
	require.NoError(t, err)
	names = names[:0]
	for _, entry := range entries {
		names = append(names, entry.Name)
	}
	require.ElementsMatch(t, []string{"other.txt", "input", "output"}, names)
}

func TestSessionFileBrowserRefusesOutsideWorkspace(t *testing.T) {
	ctx := browserCtx()
	mgr, _ := newLocalBrowserManager(t)
	store := mgr.SessionFileStore()
	browser := mgr

	// Listing never leaves /workspace.
	_, err := browser.ListSessionDir(ctx, "s-1", "/etc")
	require.Error(t, err)
	_, err = browser.ListSessionDir(ctx, "s-1", "../../etc")
	require.Error(t, err)

	// The attachment tree is read-only for writes and deletes alike.
	require.Error(t, store.WriteSessionWorkspaceFile(ctx, "s-1", "/workspace/input/evil.txt", nil))
	require.Error(t, browser.RemoveSessionWorkspacePath(ctx, "s-1", "/workspace/input/x", true))

	// The roots themselves are not deletable "files".
	require.Error(t, browser.RemoveSessionWorkspacePath(ctx, "s-1", "/workspace", true))
	require.Error(t, browser.RemoveSessionWorkspacePath(ctx, "s-1", "/workspace/output", true))

	// Rename cannot escape either.
	require.Error(t, browser.RenameSessionWorkspacePath(ctx, "s-1",
		"/workspace/a.txt", "/tmp/escaped.txt"))
	require.Error(t, browser.RenameSessionWorkspacePath(ctx, "s-1",
		"/etc/passwd", "/workspace/stolen.txt"))
}

func TestSessionFileBrowserRenameIntoOwnSubtreeRefused(t *testing.T) {
	ctx := browserCtx()
	mgr, _ := newLocalBrowserManager(t)
	browser := mgr
	store := mgr.SessionFileStore()
	require.NoError(t, store.EnsureSessionDir(ctx, "s-1", "/workspace/dir/nested"))
	require.Error(t, browser.RenameSessionWorkspacePath(ctx, "s-1",
		"/workspace/dir", "/workspace/dir/nested/moved"))
}

func TestSessionFileBrowserNoSandboxBound(t *testing.T) {
	ctx := browserCtx()
	mgr, _ := newLocalBrowserManager(t)
	// No session was ever provisioned in this manager's store: the
	// lookup-only contract must report emptiness, not create a sandbox.
	browser := mgr
	entries, err := browser.ListSessionDir(ctx, "never-bound", "/workspace")
	require.NoError(t, err)
	require.Nil(t, entries, "an unbound session lists nothing without provisioning")
}
