package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterLearningRoutes registers the personal learning-profile endpoints.
//
// Same isolation model as the memory routes: no subject parameter anywhere,
// the profile space comes from the caller's principal, so Viewer is the only
// role gate needed and API keys must be full-access.
func RegisterLearningRoutes(r *gin.RouterGroup, learningHandler *handler.LearningHandler, g *rbacGuards) {
	if learningHandler == nil {
		return
	}
	group := g.apiKeyGroup(r.Group("/learning", g.Viewer()), apiKeyFullAccess())
	{
		group.GET("/profile", learningHandler.GetProfile)
		group.DELETE("/profile", learningHandler.DeleteProfile)
		group.GET("/profile/export", learningHandler.ExportProfile)
		group.GET("/recommendations", learningHandler.GetRecommendations)
		group.POST("/events/view", learningHandler.RecordView)
	}
}
