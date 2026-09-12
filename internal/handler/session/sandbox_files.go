// Package session: session sandbox file browser endpoints.
//
// All five verbs ride the same ownership check as the rest of the session
// surface (GetSession for reads, GetOwnedSession for mutations) and delegate
// the actual sandbox work to SandboxTerminalService, which resolves the
// session's pinned backend lookup-only. Path safety lives entirely in the
// sandbox package; this handler never joins or cleans a path itself.
package session

import (
	"errors"
	"io"
	"net/http"
	"strings"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/filetransport"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/sandbox"
	secutils "github.com/Tencent/WeKnora/internal/utils"
	"github.com/gin-gonic/gin"
)

// respondSessionFileError maps service-layer errors onto status codes the
// file panel can distinguish: 404 for an unbound session, 409 for a backend
// without a browser capability, 400 for path-rule violations.
func respondSessionFileError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, sandbox.ErrNoLiveSessionSandbox):
		c.Error(apperrors.NewNotFoundError("No live sandbox bound to this session"))
	case errors.Is(err, service.ErrSessionFilesUnsupported):
		c.Error(apperrors.NewConflictError("This sandbox backend does not support the file browser"))
	case errors.Is(err, sandbox.ErrSessionFileExists):
		c.Error(apperrors.NewBadRequestError("Target path already exists"))
	case errors.Is(err, sandbox.ErrSandboxPaused):
		c.Error(apperrors.NewConflictError("Sandbox is paused"))
	default:
		// Path-rule violations and provider failures both arrive here; the
		// sandbox package's messages are safe to surface because they quote
		// only the cleaned path.
		c.Error(apperrors.NewBadRequestError(err.Error()))
	}
}

func (h *Handler) requireSessionForRead(c *gin.Context) (string, bool) {
	ctx := c.Request.Context()
	sessionID := secutils.SanitizeForLog(paramSessionID(c))
	if sessionID == "" {
		c.Error(apperrors.NewBadRequestError("Session ID is required"))
		return "", false
	}
	if _, err := h.sessionService.GetSession(ctx, sessionID); err != nil {
		if errors.Is(err, apperrors.ErrSessionNotFound) {
			c.Error(apperrors.NewNotFoundError("Session not found"))
			return "", false
		}
		logger.ErrorWithFields(ctx, err, nil)
		c.Error(apperrors.NewInternalServerError("Failed to load session"))
		return "", false
	}
	return sessionID, true
}

func (h *Handler) requireSessionForWrite(c *gin.Context) (string, bool) {
	ctx := c.Request.Context()
	sessionID := secutils.SanitizeForLog(paramSessionID(c))
	if sessionID == "" {
		c.Error(apperrors.NewBadRequestError("Session ID is required"))
		return "", false
	}
	// Mutations use the strict owner scope: a tenant admin may read an
	// API-key session but must not reshape its sandbox files.
	if _, err := h.sessionService.GetOwnedSession(ctx, sessionID); err != nil {
		if errors.Is(err, apperrors.ErrSessionNotFound) {
			c.Error(apperrors.NewNotFoundError("Session not found"))
			return "", false
		}
		logger.ErrorWithFields(ctx, err, nil)
		c.Error(apperrors.NewInternalServerError("Failed to load session"))
		return "", false
	}
	return sessionID, true
}

// ListSandboxFiles godoc
// @Summary      列出会话沙箱工作区的一个目录
// @Description  返回 /workspace 下指定目录的扁平列表（不递归）
// @Tags         会话
// @Produce      json
// @Param        session_id  path  string  true  "会话ID"
// @Param        path        query string  false "目录路径，默认 /workspace"
// @Success      200  {object}  map[string]interface{}
// @Failure      404  {object}  errors.AppError
// @Security     Bearer
// @Router       /sessions/{session_id}/sandbox/files [get]
func (h *Handler) ListSandboxFiles(c *gin.Context) {
	ctx := c.Request.Context()
	sessionID, ok := h.requireSessionForRead(c)
	if !ok {
		return
	}
	dir := c.Query("path")
	entries, err := h.terminalService.ListSessionDir(ctx, sessionID, dir)
	if err != nil {
		respondSessionFileError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": entries})
}

// DownloadSandboxFile godoc
// @Summary      下载会话沙箱中的一个文件
// @Description  按 path 读取 /workspace 下的文件并流式返回
// @Tags         会话
// @Produce      octet-stream
// @Param        session_id  path  string  true  "会话ID"
// @Param        path        query string  true  "文件路径"
// @Success      200  {string}  binary
// @Failure      404  {object}  errors.AppError
// @Security     Bearer
// @Router       /sessions/{session_id}/sandbox/files/download [get]
func (h *Handler) DownloadSandboxFile(c *gin.Context) {
	ctx := c.Request.Context()
	sessionID, ok := h.requireSessionForRead(c)
	if !ok {
		return
	}
	filePath := c.Query("path")
	if strings.TrimSpace(filePath) == "" {
		c.Error(apperrors.NewBadRequestError("path is required"))
		return
	}
	download, err := h.terminalService.ReadSessionFile(ctx, sessionID, filePath)
	if err != nil {
		respondSessionFileError(c, err)
		return
	}
	defer download.Reader.Close()
	if err := filetransport.Serve(c.Writer, c.Request, download.Reader, filetransport.Options{
		Filename:     download.Name,
		ContentType:  service.FileContentType(download.Name),
		CacheControl: "private, no-store",
	}); err != nil && !errors.Is(err, io.EOF) {
		logger.Errorf(ctx, "Failed to stream sandbox file: %v", err)
	}
}

// UploadSandboxFile godoc
// @Summary      上传文件到会话沙箱工作区
// @Description  multipart 上传单个文件，写入 /workspace 下的指定路径（input 区只读）
// @Tags         会话
// @Accept       multipart/form-data
// @Produce      json
// @Param        session_id  path  string  true  "会话ID"
// @Param        path        formData string  true "目标文件路径"
// @Param        file        formData file    true "文件内容"
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {object}  errors.AppError
// @Security     Bearer
// @Router       /sessions/{session_id}/sandbox/files [post]
func (h *Handler) UploadSandboxFile(c *gin.Context) {
	ctx := c.Request.Context()
	sessionID, ok := h.requireSessionForWrite(c)
	if !ok {
		return
	}
	filePath := c.PostForm("path")
	if strings.TrimSpace(filePath) == "" {
		c.Error(apperrors.NewBadRequestError("path is required"))
		return
	}
	header, err := c.FormFile("file")
	if err != nil {
		c.Error(apperrors.NewBadRequestError("file is required"))
		return
	}
	// Size cap matches the attachment endpoint's ceiling; a browser upload
	// must not become a way to fill a microVM disk in one request.
	if header.Size > int64(secutils.GetMaxFileSizeMB())*1024*1024 {
		c.Error(apperrors.NewBadRequestError("File exceeds the upload size limit"))
		return
	}
	f, err := header.Open()
	if err != nil {
		c.Error(apperrors.NewBadRequestError("Failed to read uploaded file"))
		return
	}
	defer f.Close()
	content, err := io.ReadAll(f)
	if err != nil {
		c.Error(apperrors.NewInternalServerError("Failed to read uploaded file"))
		return
	}
	if err := h.terminalService.WriteSessionWorkspaceFile(ctx, sessionID, filePath, content); err != nil {
		respondSessionFileError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

// MakeSandboxDir godoc
// @Summary      在会话沙箱工作区创建目录
// @Tags         会话
// @Produce      json
// @Param        session_id  path  string  true  "会话ID"
// @Param        body  body  object  true  "{path: string}"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Router       /sessions/{session_id}/sandbox/files/mkdir [post]
func (h *Handler) MakeSandboxDir(c *gin.Context) {
	ctx := c.Request.Context()
	sessionID, ok := h.requireSessionForWrite(c)
	if !ok {
		return
	}
	var body struct {
		Path string `json:"path"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.Error(apperrors.NewBadRequestError("Invalid request body"))
		return
	}
	if err := h.terminalService.EnsureSessionDir(ctx, sessionID, body.Path); err != nil {
		respondSessionFileError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

// RenameSandboxFile godoc
// @Summary      重命名（移动）会话沙箱中的文件或目录
// @Tags         会话
// @Produce      json
// @Param        session_id  path  string  true  "会话ID"
// @Param        body  body  object  true  "{from: string, to: string}"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Router       /sessions/{session_id}/sandbox/files/rename [post]
func (h *Handler) RenameSandboxFile(c *gin.Context) {
	ctx := c.Request.Context()
	sessionID, ok := h.requireSessionForWrite(c)
	if !ok {
		return
	}
	var body struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.Error(apperrors.NewBadRequestError("Invalid request body"))
		return
	}
	if strings.TrimSpace(body.From) == "" || strings.TrimSpace(body.To) == "" {
		c.Error(apperrors.NewBadRequestError("from and to are required"))
		return
	}
	if err := h.terminalService.RenameSessionWorkspacePath(ctx, sessionID, body.From, body.To); err != nil {
		respondSessionFileError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

// DeleteSandboxFile godoc
// @Summary      删除会话沙箱中的文件或目录
// @Tags         会话
// @Produce      json
// @Param        session_id  path  string  true  "会话ID"
// @Param        path        query string  true  "目标路径"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Router       /sessions/{session_id}/sandbox/files [delete]
func (h *Handler) DeleteSandboxFile(c *gin.Context) {
	ctx := c.Request.Context()
	sessionID, ok := h.requireSessionForWrite(c)
	if !ok {
		return
	}
	targetPath := c.Query("path")
	if strings.TrimSpace(targetPath) == "" {
		c.Error(apperrors.NewBadRequestError("path is required"))
		return
	}
	// Recursive delete matches directory semantics in the UI; the sandbox
	// path rules still refuse the roots and the attachment tree.
	if err := h.terminalService.RemoveSessionWorkspacePath(ctx, sessionID, targetPath, true); err != nil {
		respondSessionFileError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}
