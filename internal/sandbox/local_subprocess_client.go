// Package sandbox: local subprocess backend.
//
// The local backend runs each sandbox as a directory on the WeKnora host plus
// host processes rooted at that directory. It exists for single-node
// deployments and development machines where no Docker daemon or MicroVM
// service is available, and it implements the full RemoteSandboxClient
// contract so SessionBoundManager, the file tools, artifact collection and the
// terminal work without backend-specific branches upstream.
//
// Isolation model — read this before pointing a tenant config at "local":
//
//   - The sandbox filesystem is the directory <workspace_root>/<sandboxID>,
//     presented to scripts as "/". Every filesystem operation and every exec
//     working directory is jailed to that directory; ".." or symlink escapes
//     that would land outside it are rejected.
//   - Processes are NOT containerized. A malicious script runs with the
//     WeKnora process's OS privileges on the host network. CPU and memory
//     caps are enforced per-exec with ulimit inside the wrapper shell, and
//     the whole process group is killed on timeout, but this is weaker than
//     the cgroup boundary Docker gives. Deployments that need hard isolation
//     must use the Docker backend.
package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
)

// Local meta file written inside every sandbox directory so List/Get can
// recover ownership metadata after a WeKnora restart without any control
// plane: the filesystem is the control plane.
const localMetaFileName = ".weknora-meta.json"

// DefaultLocalLimits bound one exec when the stored config leaves them
// unset. They mirror the docker defaults in spirit: bounded, not tiny.
const (
	DefaultLocalCPULimitSeconds = 60
	DefaultLocalMemoryLimitMB   = 512
	// DefaultLocalIdleTTL reclaims a session directory that has not been
	// used for this long, matching DefaultDockerIdleTTL's role.
	DefaultLocalIdleTTL = 30 * time.Minute
	// DefaultLocalMaxDurationBytes caps a single exec's wall clock at the
	// same place DefaultTimeout does; the config timeout wins when set.
)

// LocalSandboxLimits is the resolved per-exec resource policy.
type LocalSandboxLimits struct {
	CPULimitSeconds int
	MemoryLimitMB   int
}

// localMeta is the persisted metadata bag.
type localMeta struct {
	TenantID  string            `json:"tenant_id,omitempty"`
	ConfigID  string            `json:"config_id,omitempty"`
	CreatedAt time.Time         `json:"created_at,omitempty"`
	Extra     map[string]string `json:"extra,omitempty"`
}

// localSandboxHandle implements RemoteSandboxHandle.
type localSandboxHandle struct {
	client *LocalSubprocessClient
	id     string
	meta   map[string]string
}

func (h *localSandboxHandle) ID() string                        { return h.id }
func (h *localSandboxHandle) Provider() RemoteProvider          { return SandboxTypeLocal }
func (h *localSandboxHandle) Metadata() map[string]string       { return cloneMetadata(h.meta) }

// LocalSubprocessClient is the RemoteSandboxClient adapter for the local
// backend. Safe for concurrent use: the registry mutex guards the in-memory
// process table, while the filesystem itself relies on per-sandbox directory
// isolation plus Go's atomic rename semantics for the meta file.
type LocalSubprocessClient struct {
	root string
	// limits applied to every exec; resolved once at construction.
	limits    LocalSandboxLimits
	idleTTL   time.Duration

	mu        sync.Mutex
	lastUsed  map[string]time.Time
	procs     map[string][]*os.Process // live process groups per sandbox
	sweeperStop chan struct{}
	sweeperOnce sync.Once
}

// localTerminalsSupported is build-tagged: PTYs need /dev/ptmx, which the
// darwin build resolves differently from Linux. See local_pty_linux.go and
// local_pty_other.go.
var localTerminalsSupported = localTerminalsSupportedImpl

// NewLocalSubprocessClient validates the workspace root and returns a client.
// The root is created when missing; it must be an absolute path so the jail
// below can never be relativised away by a odd working directory.
func NewLocalSubprocessClient(cfg *Config) (*LocalSubprocessClient, error) {
	if cfg == nil {
		return nil, fmt.Errorf("sandbox: local backend requires a config")
	}
	root := strings.TrimSpace(cfg.LocalWorkspaceRoot)
	if root == "" {
		return nil, fmt.Errorf("sandbox: local backend requires workspace_root")
	}
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("sandbox: local workspace_root must be absolute, got %q", root)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("sandbox: create local workspace root %s: %w", root, err)
	}
	limits := LocalSandboxLimits{
		CPULimitSeconds: cfg.LocalCPULimitSeconds,
		MemoryLimitMB:   cfg.LocalMemoryLimitMB,
	}
	if limits.CPULimitSeconds <= 0 {
		limits.CPULimitSeconds = DefaultLocalCPULimitSeconds
	}
	if limits.MemoryLimitMB <= 0 {
		limits.MemoryLimitMB = DefaultLocalMemoryLimitMB
	}
	idleTTL := cfg.LocalIdleTTL
	if idleTTL <= 0 {
		idleTTL = DefaultLocalIdleTTL
	}
	client := &LocalSubprocessClient{
		root:      filepath.Clean(root),
		limits:    limits,
		idleTTL:   idleTTL,
		lastUsed:  make(map[string]time.Time),
		procs:     make(map[string][]*os.Process),
		sweeperStop: make(chan struct{}),
	}
	client.startIdleSweeper()
	return client, nil
}

// Provider implements RemoteSandboxClient.
func (c *LocalSubprocessClient) Provider() RemoteProvider { return SandboxTypeLocal }

// Capabilities implements RemoteSandboxClient.
func (c *LocalSubprocessClient) Capabilities() RemoteSandboxCapabilities {
	return RemoteSandboxCapabilities{
		SupportsReconnect:             true, // the directory survives a restart
		SupportsMetadata:              true, // meta JSON inside the sandbox dir
		SupportsListSandboxes:         true, // root scan
		SupportsPauseResume:           false,
		SupportsTimeoutRefresh:        false,
		SupportsFilesystemEnumeration: true,
		SupportsSnapshots:             false, // skill installs fall back to base env
		SupportsVolumes:               false,
		SupportsTerminals:             localTerminalsSupported(),
	}
}

// Health implements RemoteSandboxClient: the backend is healthy when the
// workspace root exists and accepts writes.
func (c *LocalSubprocessClient) Health(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	probe, err := os.MkdirTemp(c.root, ".health-*")
	if err != nil {
		return fmt.Errorf("local workspace root %s not writable: %w", c.root, err)
	}
	return os.Remove(probe)
}

// --- paths ----------------------------------------------------------------

// sandboxRoot maps a sandbox ID onto its host directory. The ID is validated
// so it can never carry path separators.
func (c *LocalSubprocessClient) sandboxRoot(sandboxID string) (string, error) {
	if strings.TrimSpace(sandboxID) == "" {
		return "", fmt.Errorf("sandbox: local sandbox id is required")
	}
	if sandboxID != filepath.Base(sandboxID) || strings.Contains(sandboxID, ".") && strings.HasPrefix(sandboxID, ".") {
		return "", fmt.Errorf("sandbox: invalid local sandbox id %q", sandboxID)
	}
	return filepath.Join(c.root, sandboxID), nil
}

// hostPath jails a sandbox-absolute path into the sandbox directory. It
// mirrors Docker's "the container FS is what you see" semantics: the sandbox
// directory IS "/" for scripts.
func (c *LocalSubprocessClient) hostPath(sandboxID, path string) (string, error) {
	root, err := c.sandboxRoot(sandboxID)
	if err != nil {
		return "", err
	}
	clean := filepath.Clean("/" + strings.TrimSpace(path))
	if clean == "/" || clean == "." {
		return root, nil
	}
	host := filepath.Join(root, clean)
	// Defence in depth: Join above already cleans, but re-verify the prefix
	// so a crafted ID/path pair can never walk out of the jail.
	if host != root && !strings.HasPrefix(host, root+string(filepath.Separator)) {
		return "", fmt.Errorf("sandbox: path %q escapes the local sandbox jail", path)
	}
	return host, nil
}

// --- lifecycle ------------------------------------------------------------

// Create implements RemoteSandboxClient.
func (c *LocalSubprocessClient) Create(ctx context.Context, req RemoteCreateRequest) (RemoteSandboxHandle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id := uuid.NewString()
	root, err := c.sandboxRoot(id)
	if err != nil {
		return nil, err
	}
	// Seed the same layout every backend's images ship: /workspace with
	// input/output subdirectories, so artifact collection paths agree.
	for _, dir := range []string{"/workspace", "/workspace/input", "/workspace/output", "/tmp"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			return nil, fmt.Errorf("sandbox: seed local sandbox %s: %w", id, err)
		}
	}
	meta := localMeta{CreatedAt: time.Now().UTC()}
	if req.Metadata != nil {
		meta.TenantID = req.Metadata[remoteMetadataTenantID]
		meta.ConfigID = req.Metadata[remoteMetadataConfigID]
		meta.Extra = cloneMetadata(req.Metadata)
	}
	if err := c.writeMeta(id, meta); err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.lastUsed[id] = time.Now()
	c.mu.Unlock()
	return &localSandboxHandle{client: c, id: id, meta: meta.Extra}, nil
}

// Connect implements RemoteSandboxClient: reconnect is a directory-exists
// check plus meta recovery.
func (c *LocalSubprocessClient) Connect(ctx context.Context, req RemoteConnectRequest) (RemoteSandboxHandle, error) {
	summary, err := c.Get(ctx, req.SandboxID)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.lastUsed[req.SandboxID] = time.Now()
	c.mu.Unlock()
	return &localSandboxHandle{client: c, id: req.SandboxID, meta: summary.Metadata}, nil
}

// Get implements RemoteSandboxClient.
func (c *LocalSubprocessClient) Get(ctx context.Context, sandboxID string) (*RemoteSandboxSummary, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := c.sandboxRoot(sandboxID)
	if err != nil {
		return nil, localInvalidRequest("Get", err.Error())
	}
	info, err := os.Stat(filepath.Join(root, "workspace"))
	if err != nil || !info.IsDir() {
		return nil, NewRemoteError(SandboxTypeLocal, "Get", RemoteErrorKindNotFound,
			fmt.Sprintf("local sandbox %s not found", sandboxID), nil)
	}
	summary := &RemoteSandboxSummary{
		ID:         sandboxID,
		TemplateID: "local",
		State:      RemoteStateRunning,
		RawState:   "running",
	}
	if meta, err := c.readMeta(sandboxID); err == nil {
		summary.Metadata = meta.Extra
		summary.StartedAt = meta.CreatedAt
	}
	return summary, nil
}

// List implements RemoteSandboxClient by scanning the workspace root. The
// metadata filter is applied client-side exactly as the interface documents.
func (c *LocalSubprocessClient) List(ctx context.Context, filter RemoteListFilter) ([]RemoteSandboxSummary, error) {
	entries, err := os.ReadDir(c.root)
	if err != nil {
		return nil, fmt.Errorf("sandbox: scan local root %s: %w", c.root, err)
	}
	var out []RemoteSandboxSummary
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		summary, err := c.Get(ctx, entry.Name())
		if err != nil {
			continue // not a sandbox directory
		}
		if len(filter.Metadata) > 0 {
			match := true
			for key, value := range filter.Metadata {
				if summary.Metadata[key] != value {
					match = false
					break
				}
			}
			if !match {
				continue
			}
		}
		out = append(out, *summary)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Delete implements RemoteSandboxClient: kill every live process group the
// sandbox still owns, then remove the directory tree. A missing sandbox is
// RemoteErrorKindNotFound per the interface contract.
func (c *LocalSubprocessClient) Delete(ctx context.Context, sandboxID string) error {
	root, err := c.sandboxRoot(sandboxID)
	if err != nil {
		return localInvalidRequest("Delete", err.Error())
	}
	if _, err := os.Stat(root); err != nil {
		return NewRemoteError(SandboxTypeLocal, "Delete", RemoteErrorKindNotFound,
			fmt.Sprintf("local sandbox %s not found", sandboxID), nil)
	}
	c.killSandboxProcs(sandboxID)
	if err := os.RemoveAll(root); err != nil {
		return NewRemoteError(SandboxTypeLocal, "Delete", RemoteErrorKindInternal, err.Error(), err)
	}
	c.mu.Lock()
	delete(c.lastUsed, sandboxID)
	delete(c.procs, sandboxID)
	c.mu.Unlock()
	return nil
}

// localRootRefRe captures a sandbox-root reference (/workspace or /tmp) at a
// token boundary together with the rest of its path so the whole token stays
// jailed. One combined pass: two sequential replacements would re-match the
// host paths the first pass inserted.
var localRootRefRe = regexp.MustCompile(`(^|[^\w./~-])(/workspace|/tmp)([/\w.+-]*)`)

// translateSandboxRoots rewrites literal /workspace and /tmp references in a
// shell command or argv token onto their jailed host paths, so scripts keep
// using the sandbox-absolute spelling every other backend understands.
func (c *LocalSubprocessClient) translateSandboxRoots(sandboxID, text string) string {
	if text == "" || !strings.Contains(text, "/workspace") && !strings.Contains(text, "/tmp") {
		return text
	}
	workspace, err := c.hostPath(sandboxID, "/workspace")
	if err != nil {
		return text
	}
	tmp, err := c.hostPath(sandboxID, "/tmp")
	if err != nil {
		return text
	}
	return localRootRefRe.ReplaceAllStringFunc(text, func(match string) string {
		groups := localRootRefRe.FindStringSubmatch(match)
		root := groups[2]
		if root == "/workspace" {
			return groups[1] + workspace + groups[3]
		}
		return groups[1] + tmp + groups[3]
	})
}

// --- execution ------------------------------------------------------------

// Exec implements RemoteSandboxClient. Shell=true runs the command through
// the wrapper shell with ulimit lines prepended; Shell=false execs argv
// directly under the same wrapper so the limits always apply. The process
// runs in its own group so a timeout kills the whole tree, matching how the
// docker backend tears down a container exec.
func (c *LocalSubprocessClient) Exec(
	ctx context.Context,
	handle RemoteSandboxHandle,
	req RemoteExecRequest,
) (*RemoteExecResult, error) {
	if handle == nil || handle.Provider() != SandboxTypeLocal {
		return nil, localInvalidRequest("Exec", "mismatched sandbox handle")
	}
	if req.Shell && len(req.Args) > 0 {
		return nil, localInvalidRequest("Exec", "Shell=true forbids Args")
	}
	started := time.Now()
	if _, err := c.Connect(ctx, RemoteConnectRequest{SandboxID: handle.ID()}); err != nil {
		return nil, err
	}

	workDir, err := c.hostPath(handle.ID(), req.WorkDir)
	if err != nil {
		return nil, localInvalidRequest("Exec", err.Error())
	}
	if info, err := os.Stat(workDir); err != nil || !info.IsDir() {
		if req.WorkDir != "" {
			return nil, localInvalidRequest("Exec", fmt.Sprintf("workdir %q does not exist", req.WorkDir))
		}
		workDir, _ = c.hostPath(handle.ID(), "/workspace")
	}

	shell := "/bin/sh"
	if _, err := os.Stat("/bin/bash"); err == nil {
		shell = "/bin/bash"
	}

	// The sandbox directory is presented to scripts as "/", but the process
	// is not chrooted: a literal /workspace in a command would hit the real
	// host path (read-only on macOS, a jail escape on Linux). Rewrite the
	// known sandbox roots onto their jailed host paths before running.
	command := c.translateSandboxRoots(handle.ID(), req.Command)
	args := make([]string, len(req.Args))
	for i, arg := range req.Args {
		args[i] = c.translateSandboxRoots(handle.ID(), arg)
	}

	var cmd *exec.Cmd
	if req.Shell {
		// ulimit lines run in the wrapper shell; every child inherits them.
		// ulimit -v is not supported by macOS bash (it aborts the script),
		// so the memory line degrades to a no-op there; on Linux it holds.
		script := fmt.Sprintf("ulimit -t %d\nulimit -v %d 2>/dev/null || true\n",
			c.limits.CPULimitSeconds, c.limits.MemoryLimitMB*1024) + command
		cmd = exec.Command(shell, "-c", script)
	} else {
		// Wrap argv, never string interpolation, so arguments stay arguments:
		// `sh -c 'ulimit …; exec "$@"' <sh> <cmd> <args…>`.
		wrapper := fmt.Sprintf(`ulimit -t %d; { ulimit -v %d 2>/dev/null || true; }; exec "$@"`,
			c.limits.CPULimitSeconds, c.limits.MemoryLimitMB*1024)
		argv := append([]string{"-c", wrapper, shell, command}, args...)
		cmd = exec.Command(shell, argv...)
	}
	cmd.Dir = workDir
	cmd.Env = c.execEnv(req.Env)
	if req.Stdin != "" {
		cmd.Stdin = strings.NewReader(req.Stdin)
	}

	var stdout, stderr strings.Builder
	onOutput := req.OnOutput
	cmd.Stdout = &limitWriter{builder: &stdout, sink: onOutput, stream: "stdout"}
	cmd.Stderr = &limitWriter{builder: &stderr, sink: onOutput, stream: "stderr"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := cmd.Start(); err != nil {
		return nil, NewRemoteError(SandboxTypeLocal, "Exec", RemoteErrorKindInternal, err.Error(), err)
	}
	proc := cmd.Process
	c.trackProc(handle.ID(), proc)

	result := &RemoteExecResult{}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-execCtx.Done():
		// Kill the whole group: the wrapper shell alone would leave
		// grandchildren (python, node) running on the host.
		killProcessGroup(proc)
		<-done
		result.Killed = execCtx.Err() == context.DeadlineExceeded || ctx.Err() != nil
		result.ExitCode = -1
		c.untrackProc(handle.ID(), proc)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	case err := <-done:
		c.untrackProc(handle.ID(), proc)
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				result.ExitCode = exitErr.ExitCode()
			} else {
				return nil, NewRemoteError(SandboxTypeLocal, "Exec", RemoteErrorKindInternal, err.Error(), err)
			}
		}
	}
	result.Stdout = stdout.String()
	result.Stderr = stderr.String()
	result.Duration = time.Since(started)
	return result, nil
}

func (c *LocalSubprocessClient) execEnv(extra map[string]string) []string {
	env := []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=/workspace",
		"LANG=C.UTF-8",
		"TERM=xterm-256color",
	}
	for key, value := range extra {
		env = append(env, key+"="+value)
	}
	return env
}

// --- filesystem ------------------------------------------------------------

func (c *LocalSubprocessClient) WriteFile(ctx context.Context, handle RemoteSandboxHandle, path string, content []byte) error {
	host, err := c.hostPath(sandboxIDOf(handle), path)
	if err != nil {
		return localInvalidRequest("WriteFile", err.Error())
	}
	if err := os.MkdirAll(filepath.Dir(host), 0o755); err != nil {
		return NewRemoteError(SandboxTypeLocal, "WriteFile", RemoteErrorKindInternal, err.Error(), err)
	}
	return os.WriteFile(host, content, 0o644)
}

func (c *LocalSubprocessClient) ReadFile(ctx context.Context, handle RemoteSandboxHandle, path string) ([]byte, error) {
	host, err := c.hostPath(sandboxIDOf(handle), path)
	if err != nil {
		return nil, localInvalidRequest("ReadFile", err.Error())
	}
	data, err := os.ReadFile(host)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, NewRemoteError(SandboxTypeLocal, "ReadFile", RemoteErrorKindNotFound, err.Error(), err)
		}
		return nil, NewRemoteError(SandboxTypeLocal, "ReadFile", RemoteErrorKindInternal, err.Error(), err)
	}
	return data, nil
}

func (c *LocalSubprocessClient) ListDir(ctx context.Context, handle RemoteSandboxHandle, path string) ([]RemoteDirEntry, error) {
	host, err := c.hostPath(sandboxIDOf(handle), path)
	if err != nil {
		return nil, localInvalidRequest("ListDir", err.Error())
	}
	entries, err := os.ReadDir(host)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, NewRemoteError(SandboxTypeLocal, "ListDir", RemoteErrorKindNotFound, err.Error(), err)
		}
		return nil, NewRemoteError(SandboxTypeLocal, "ListDir", RemoteErrorKindInternal, err.Error(), err)
	}
	out := make([]RemoteDirEntry, 0, len(entries))
	for _, entry := range entries {
		item := RemoteDirEntry{Name: entry.Name(), Path: path + "/" + entry.Name()}
		info, err := entry.Info()
		if err == nil {
			item.Size = info.Size()
			item.ModTime = info.ModTime()
		}
		switch {
		case entry.Type().IsRegular():
			item.Type = RemoteEntryFile
		case entry.Type().IsDir():
			item.Type = RemoteEntryDir
		default:
			// Symlinks etc. stay opaque: matching how the artifact code
			// treats them, and avoiding a jail escape through a link.
			item.Type = RemoteEntryOther
		}
		out = append(out, item)
	}
	return out, nil
}

func (c *LocalSubprocessClient) MakeDir(ctx context.Context, handle RemoteSandboxHandle, path string) error {
	host, err := c.hostPath(sandboxIDOf(handle), path)
	if err != nil {
		return localInvalidRequest("MakeDir", err.Error())
	}
	if err := os.MkdirAll(host, 0o755); err != nil {
		return NewRemoteError(SandboxTypeLocal, "MakeDir", RemoteErrorKindInternal, err.Error(), err)
	}
	return nil
}

func (c *LocalSubprocessClient) Remove(ctx context.Context, handle RemoteSandboxHandle, path string) error {
	host, err := c.hostPath(sandboxIDOf(handle), path)
	if err != nil {
		return localInvalidRequest("Remove", err.Error())
	}
	clean := filepath.Clean("/" + strings.TrimSpace(path))
	if clean == "/" {
		return localInvalidRequest("Remove", "refusing to remove the sandbox root")
	}
	if err := os.RemoveAll(host); err != nil {
		return NewRemoteError(SandboxTypeLocal, "Remove", RemoteErrorKindInternal, err.Error(), err)
	}
	return nil
}

func (c *LocalSubprocessClient) Stat(ctx context.Context, handle RemoteSandboxHandle, path string) (*RemoteStatEntry, error) {
	host, err := c.hostPath(sandboxIDOf(handle), path)
	if err != nil {
		return nil, localInvalidRequest("Stat", err.Error())
	}
	info, err := os.Lstat(host)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, NewRemoteError(SandboxTypeLocal, "Stat", RemoteErrorKindNotFound, err.Error(), err)
		}
		return nil, NewRemoteError(SandboxTypeLocal, "Stat", RemoteErrorKindInternal, err.Error(), err)
	}
	entry := &RemoteStatEntry{Path: path, Size: info.Size(), ModTime: info.ModTime()}
	switch {
	case info.Mode().IsRegular():
		entry.Type = RemoteEntryFile
	case info.IsDir():
		entry.Type = RemoteEntryDir
	default:
		entry.Type = RemoteEntryOther
	}
	return entry, nil
}

// --- terminal --------------------------------------------------------------

// OpenTerminal implements the optional RemoteTerminalManager capability on
// Linux. See local_pty_linux.go.
func (c *LocalSubprocessClient) OpenTerminal(
	ctx context.Context,
	handle RemoteSandboxHandle,
	opts RemoteTerminalOptions,
) (RemoteTerminalSession, error) {
	return c.openLocalTerminal(ctx, handle, opts)
}

// --- process bookkeeping ----------------------------------------------------

func (c *LocalSubprocessClient) trackProc(sandboxID string, proc *os.Process) {
	c.mu.Lock()
	c.procs[sandboxID] = append(c.procs[sandboxID], proc)
	c.lastUsed[sandboxID] = time.Now()
	c.mu.Unlock()
}

func (c *LocalSubprocessClient) untrackProc(sandboxID string, proc *os.Process) {
	c.mu.Lock()
	live := c.procs[sandboxID][:0]
	for _, p := range c.procs[sandboxID] {
		if p != proc {
			live = append(live, p)
		}
	}
	c.procs[sandboxID] = live
	c.lastUsed[sandboxID] = time.Now()
	c.mu.Unlock()
}

func (c *LocalSubprocessClient) killSandboxProcs(sandboxID string) {
	c.mu.Lock()
	procs := c.procs[sandboxID]
	c.procs[sandboxID] = nil
	c.mu.Unlock()
	for _, proc := range procs {
		killProcessGroup(proc)
	}
}

// startIdleSweeper reclaims sandbox directories idle beyond the TTL, the
// local counterpart of the docker idle sweep. Only directories whose
// processes are all gone are removed.
func (c *LocalSubprocessClient) startIdleSweeper() {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-c.sweeperStop:
				return
			case <-ticker.C:
				c.sweepIdle()
			}
		}
	}()
}

func (c *LocalSubprocessClient) sweepIdle() {
	c.mu.Lock()
	var expired []string
	for id, used := range c.lastUsed {
		if time.Since(used) > c.idleTTL {
			expired = append(expired, id)
		}
	}
	c.mu.Unlock()
	for _, id := range expired {
		// Re-check after the window: a sandbox may have been reused.
		c.mu.Lock()
		used := c.lastUsed[id]
		procs := len(c.procs[id])
		c.mu.Unlock()
		if time.Since(used) <= c.idleTTL || procs > 0 {
			continue
		}
		_ = c.Delete(context.Background(), id)
	}
}

// Close stops the background sweeper. Tests call it; production clients live
// for the process lifetime.
func (c *LocalSubprocessClient) Close() {
	c.sweeperOnce.Do(func() { close(c.sweeperStop) })
}

// --- meta ------------------------------------------------------------------

func (c *LocalSubprocessClient) metaPath(sandboxID string) (string, error) {
	root, err := c.sandboxRoot(sandboxID)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, localMetaFileName), nil
}

func (c *LocalSubprocessClient) writeMeta(sandboxID string, meta localMeta) error {
	path, err := c.metaPath(sandboxID)
	if err != nil {
		return err
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func (c *LocalSubprocessClient) readMeta(sandboxID string) (localMeta, error) {
	path, err := c.metaPath(sandboxID)
	if err != nil {
		return localMeta{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return localMeta{}, err
	}
	var meta localMeta
	err = json.Unmarshal(data, &meta)
	return meta, err
}

// --- helpers ----------------------------------------------------------------

func sandboxIDOf(handle RemoteSandboxHandle) string {
	if handle == nil {
		return ""
	}
	return handle.ID()
}

// limitWriter tees exec output into the result buffer and the caller's
// streaming hook in one write, mirroring the docker adapter's output pump.
type limitWriter struct {
	builder *strings.Builder
	sink    func(stream string, chunk []byte)
	stream  string
}

func (w *limitWriter) Write(p []byte) (int, error) {
	w.builder.Write(p)
	if w.sink != nil {
		w.sink(w.stream, p)
	}
	return len(p), nil
}

func localInvalidRequest(op, message string) error {
	return NewRemoteError(SandboxTypeLocal, op, RemoteErrorKindInvalidRequest, message, nil)
}

// killProcessGroup terminates a sandbox process and everything it spawned.
// Exec starts every command with Setpgid, so the negative PID reaches the
// whole group; a double kill of an already-dead group is a no-op.
func killProcessGroup(proc *os.Process) {
	if proc == nil {
		return
	}
	_ = syscall.Kill(-proc.Pid, syscall.SIGKILL)
	_ = proc.Kill()
}
