package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/application/service/learning"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// LearningHandler exposes the caller's own learning profile.
//
// Like the memory handler, no endpoint takes a subject id: the profile is
// derived from the request context, so cross-user access is structurally
// impossible rather than checked.
type LearningHandler struct {
	learningService interfaces.LearningService
}

func NewLearningHandler(learningService interfaces.LearningService) *LearningHandler {
	return &LearningHandler{learningService: learningService}
}

// GetProfile godoc
// @Summary      获取我的学习画像
// @Description  返回指定知识库的知识网络掌握度画像与推荐
// @Tags         学习画像
// @Produce      json
// @Param        kb_id  query  string  true  "知识库 ID"
// @Success      200    {object}  map[string]interface{}  "学习画像"
// @Security     Bearer
// @Router       /learning/profile [get]
func (h *LearningHandler) GetProfile(c *gin.Context) {
	ctx := c.Request.Context()
	profile, err := h.learningService.Profile(ctx, c.Query("kb_id"))
	if err != nil {
		h.fail(c, err, "Failed to compute learning profile")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": profile})
}

// GetRecommendations godoc
// @Summary      获取下一步学习推荐
// @Description  返回当前知识库中最值得优先了解的盲区页面
// @Tags         学习画像
// @Produce      json
// @Param        kb_id  query  string  true  "知识库 ID"
// @Param        limit  query  int     false  "推荐条数"  default(5)
// @Success      200    {object}  map[string]interface{}  "推荐列表"
// @Security     Bearer
// @Router       /learning/recommendations [get]
func (h *LearningHandler) GetRecommendations(c *gin.Context) {
	ctx := c.Request.Context()
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "5"))
	recs, err := h.learningService.Recommend(ctx, c.Query("kb_id"), limit)
	if err != nil {
		h.fail(c, err, "Failed to build recommendations")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": recs})
}

type recordViewRequest struct {
	KBID string `json:"kb_id"`
	Slug string `json:"slug"`
}

// RecordView godoc
// @Summary      记录一次页面学习
// @Description  学习页打开某个 Wiki 页面时上报一次访问信号
// @Tags         学习画像
// @Accept       json
// @Produce      json
// @Param        request  body  object  true  "kb_id 与 slug"
// @Success      200      {object}  map[string]interface{}  "记录成功"
// @Security     Bearer
// @Router       /learning/events/view [post]
func (h *LearningHandler) RecordView(c *gin.Context) {
	ctx := c.Request.Context()
	var req recordViewRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(apperrors.NewValidationError("Invalid request data").WithDetails(err.Error()))
		return
	}
	if err := h.learningService.RecordPageView(ctx, req.KBID, req.Slug); err != nil {
		// A telemetry write must never fail the page open.
		logger.Warnf(ctx, "learning: record view failed: %v", err)
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

// ExportProfile godoc
// @Summary      导出我的学习画像
// @Description  以 JSON 导出指定知识库的完整学习画像；kb_id 为空时导出仅返回提示
// @Tags         学习画像
// @Produce      json
// @Param        kb_id  query  string  true  "知识库 ID"
// @Success      200    {object}  map[string]interface{}  "画像导出"
// @Security     Bearer
// @Router       /learning/profile/export [get]
func (h *LearningHandler) ExportProfile(c *gin.Context) {
	ctx := c.Request.Context()
	profile, err := h.learningService.Profile(ctx, c.Query("kb_id"))
	if err != nil {
		h.fail(c, err, "Failed to export learning profile")
		return
	}
	c.Header("Content-Disposition", `attachment; filename="weknora-learning-profile.json"`)
	c.JSON(http.StatusOK, gin.H{"success": true, "data": profile})
}

// DeleteProfile godoc
// @Summary      删除我的学习画像
// @Description  删除当前用户的掌握度与行为记录；带 kb_id 时只清该库，不带则清全部
// @Tags         学习画像
// @Produce      json
// @Param        kb_id  query  string  false  "知识库 ID（缺省清空全部）"
// @Success      200    {object}  map[string]interface{}  "删除成功"
// @Security     Bearer
// @Router       /learning/profile [delete]
func (h *LearningHandler) DeleteProfile(c *gin.Context) {
	ctx := c.Request.Context()
	if err := h.learningService.DeleteProfile(ctx, c.Query("kb_id")); err != nil {
		h.fail(c, err, "Failed to delete learning profile")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func (h *LearningHandler) fail(c *gin.Context, err error, message string) {
	switch {
	case errors.Is(err, learning.ErrNoLearningScope):
		c.Error(apperrors.NewUnauthorizedError("no principal in request"))
	default:
		logger.ErrorWithFields(c.Request.Context(), err, nil)
		c.Error(apperrors.NewInternalServerError(message).WithDetails(err.Error()))
	}
}
