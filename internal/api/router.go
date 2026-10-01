package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/luwa07832/config-center-api/internal/store"
)

// server bundles dependencies shared by every HTTP handler.
type server struct {
	store *store.Store
}

// NewRouter wires the public HTTP surface. The error contract in README.md applies to every entry.
func NewRouter(st *store.Store) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(gin.Recovery())

	srv := &server{store: st}

	router.GET("/healthz", func(c *gin.Context) {
		if err := st.Ping(); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"code": "storage_unavailable", "message": "database is not available"}})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok", "database": "ok"})
	})

	// Version diff is the read-only public comparison entry.
	router.GET("/config-version-diffs", srv.handleVersionDiff)
	router.GET("/namespaces/:namespace/environments/:environment/config-version-diffs/:base/:target", srv.handleVersionDiff)

	// Effective configuration and version history for a namespace and environment.
	router.GET("/effective-configs", srv.handleEffective)
	router.GET("/config-versions", srv.handleHistory)
	router.GET("/namespaces/:namespace/environments/:environment/config-versions/:version", srv.handleVersionSnapshot)

	// Publishing and rollback entry points that feed the stored version history.
	router.POST("/namespaces/:namespace/environments/:environment/config-versions", srv.handlePublish)
	router.POST("/namespaces/:namespace/environments/:environment/config-versions/:version/rollback", srv.handleRollback)

	router.NoRoute(func(c *gin.Context) {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "route_not_found", "message": "no route matches this path"}})
	})
	return router
}
