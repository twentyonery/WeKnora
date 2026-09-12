//go:build linux

// Package sandbox: PTY support for the local subprocess backend on Linux.
//
// Go's stdlib has no PTY API, and pulling in a cgo-based dependency for one
// optional capability is not worth the cross-compile cost. /dev/ptmx plus
// four ioctls is the whole story on Linux, and golang.org/x/sys/unix (already
// a dependency) provides all of them.
package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

func localTerminalsSupportedImpl() bool {
	if _, err := os.Stat("/dev/ptmx"); err != nil {
		return false
	}
	return true
}

// openLocalTerminal allocates a PTY pair, spawns a shell with it as the
// controlling terminal, and returns a RemoteTerminalSession pumping output.
func (c *LocalSubprocessClient) openLocalTerminal(
	ctx context.Context,
	handle RemoteSandboxHandle,
	opts RemoteTerminalOptions,
) (RemoteTerminalSession, error) {
	if handle == nil || handle.Provider() != SandboxTypeLocal {
		return nil, localInvalidRequest("OpenTerminal", "mismatched sandbox handle")
	}
	// Fail before allocating anything when the sandbox is gone, so the
	// WebSocket handler reports not-found instead of a dead session.
	if _, err := c.Get(ctx, handle.ID()); err != nil {
		return nil, err
	}

	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("sandbox: open /dev/ptmx: %w", err)
	}
	ptsNum, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		master.Close()
		return nil, fmt.Errorf("sandbox: TIOCGPTN: %w", err)
	}
	// Unlock the slave: without this the first open blocks forever.
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		master.Close()
		return nil, fmt.Errorf("sandbox: TIOCSPTLCK: %w", err)
	}
	cols, rows := terminalCols(opts), terminalRows(opts)
	if err := unix.IoctlSetWinsize(int(master.Fd()), unix.TIOCSWINSZ,
		&unix.Winsize{Row: uint16(rows), Col: uint16(cols)}); err != nil {
		master.Close()
		return nil, fmt.Errorf("sandbox: TIOCSWINSZ: %w", err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", ptsNum),
		os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		master.Close()
		return nil, fmt.Errorf("sandbox: open /dev/pts/%d: %w", ptsNum, err)
	}

	cwd, err := c.hostPath(handle.ID(), terminalCwd(opts))
	if err != nil {
		master.Close()
		slave.Close()
		return nil, localInvalidRequest("OpenTerminal", err.Error())
	}
	if info, statErr := os.Stat(cwd); statErr != nil || !info.IsDir() {
		cwd, _ = c.hostPath(handle.ID(), "/workspace")
	}

	shell := "/bin/bash"
	if _, err := os.Stat(shell); err != nil {
		shell = "/bin/sh"
	}
	cmd := exec.Command(shell, "-i")
	cmd.Dir = cwd
	cmd.Env = localTerminalEnv(terminalEnvs(opts))
	// New session + controlling tty = job control (Ctrl-C reaches the
	// foreground process), which is the whole point of a PTY.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
		Ctty:    int(slave.Fd()),
	}
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	if err := cmd.Start(); err != nil {
		master.Close()
		slave.Close()
		return nil, fmt.Errorf("sandbox: start terminal shell: %w", err)
	}
	c.trackProc(handle.ID(), cmd.Process)
	slave.Close() // the child holds its own reference now

	session := &localTerminalSession{
		client: c,
		id:     handle.ID(),
		master: master,
		cmd:    cmd,
		events: make(chan RemoteTerminalEvent, terminalOutputBuffer),
		closed: make(chan struct{}),
	}
	session.pump()
	return session, nil
}

func localTerminalEnv(extra map[string]string) []string {
	env := []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=/workspace",
		"LANG=C.UTF-8",
	}
	for key, value := range extra {
		env = append(env, key+"="+value)
	}
	return env
}

// localTerminalSession is the Linux RemoteTerminalSession implementation.
type localTerminalSession struct {
	client *LocalSubprocessClient
	id     string
	master *os.File
	cmd    *exec.Cmd

	events chan RemoteTerminalEvent
	closed chan struct{}
	once   sync.Once

	writeMu sync.Mutex
}

func (s *localTerminalSession) Output() <-chan RemoteTerminalEvent { return s.events }

func (s *localTerminalSession) PID() uint32 {
	if s.cmd == nil || s.cmd.Process == nil {
		return 0
	}
	return uint32(s.cmd.Process.Pid)
}

func (s *localTerminalSession) Write(ctx context.Context, data []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.master.Write(data)
	return err
}

func (s *localTerminalSession) Resize(ctx context.Context, cols, rows uint32) error {
	return ioctlSetWinsize(int(s.master.Fd()), cols, rows)
}

// Close stops pumping and kills the shell. Unlike the remote providers there
// is no provider-native reconnect: the PTY dies with the WeKnora connection,
// which is also what the interface documents as acceptable.
func (s *localTerminalSession) Close() error {
	s.once.Do(func() {
		close(s.closed)
		if s.cmd != nil && s.cmd.Process != nil {
			killProcessGroup(s.cmd.Process)
			s.client.untrackProc(s.id, s.cmd.Process)
		}
		s.master.Close()
	})
	return nil
}

// pump forwards PTY output into the event channel until the master hits EOF
// (shell exit) or Close tears the session down.
func (s *localTerminalSession) pump() {
	go func() {
		defer close(s.events)
		buf := make([]byte, 4096)
		for {
			n, err := s.master.Read(buf)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				emitTerminalEvent(s.events, s.closed, RemoteTerminalEvent{Data: chunk})
			}
			if err != nil {
				exitCode := -1
				if s.cmd != nil && s.cmd.ProcessState != nil {
					exitCode = s.cmd.ProcessState.ExitCode()
				}
				select {
				case <-s.closed:
					return
				default:
					emitTerminalEvent(s.events, s.closed, RemoteTerminalEvent{
						Exited:   true,
						ExitCode: exitCode,
					})
				}
				return
			}
			select {
			case <-s.closed:
				return
			default:
			}
		}
	}()
}

// ioctlSetWinsize is split out so the TTY resize path can be unit-tested
// without a full session (a master fd from /dev/ptmx is enough).
func ioctlSetWinsize(fd int, cols, rows uint32) error {
	return unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ,
		&unix.Winsize{Row: uint16(rows), Col: uint16(cols)})
}
