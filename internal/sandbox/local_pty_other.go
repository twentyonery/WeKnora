//go:build !linux

// Package sandbox: PTY support is Linux-only for the local subprocess
// backend. macOS development builds compile this stub instead, so the rest
// of the backend (exec, filesystem, lifecycle) stays fully testable on a
// laptop while the terminal capability degrades to "unsupported" — the same
// degradation path the docker backend already has.
package sandbox

import (
	"context"
)

func localTerminalsSupportedImpl() bool { return false }

func (c *LocalSubprocessClient) openLocalTerminal(
	ctx context.Context,
	handle RemoteSandboxHandle,
	opts RemoteTerminalOptions,
) (RemoteTerminalSession, error) {
	return nil, NewRemoteError(SandboxTypeLocal, "OpenTerminal",
		RemoteErrorKindUnsupported,
		"local backend terminals require Linux (/dev/ptmx)", nil)
}
