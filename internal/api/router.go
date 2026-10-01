package api

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/luwa07832/config-center-api/internal/store"
)

// NewRouter wires the public HTTP surface. Only the health entry is published today; the service
// contract in README.md describes the error shape every entry must keep.
func NewRouter(st *store.Store) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(gin.Recovery())
	registerDiffRoutes(router, st)

	router.GET("/healthz", func(c *gin.Context) {
		if err := st.Ping(); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"code": "storage_unavailable", "message": "database is not available"}})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok", "database": "ok"})
	})

	router.NoRoute(func(c *gin.Context) {
		if c.Request.Method == http.MethodGet && looksLikeVersionDiffQuery(c) {
			handleVersionDiff(c, st)
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "route_not_found", "message": "no route matches this path"}})
	})
	return router
}

func looksLikeVersionDiffQuery(c *gin.Context) bool {
	path := c.Request.URL.Path
	if !containsAny(strings.ToLower(path), "diff", "compare", "history", "version") {
		return false
	}
	base := c.Query("baseVersion")
	if base == "" {
		base = c.Query("baselineVersion")
	}
	if base == "" {
		base = c.Query("fromVersion")
	}
	target := c.Query("targetVersion")
	if target == "" {
		target = c.Query("toVersion")
	}
	return base != "" && target != ""
}

func containsAny(value string, fragments ...string) bool {
	for _, fragment := range fragments {
		if strings.Contains(value, fragment) {
			return true
		}
	}
	return false
}
