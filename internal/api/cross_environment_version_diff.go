package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// handleCrossEnvironmentConfigVersionDiff compares two historical versions of two environments
// within one namespace. It is strictly read-only: no version, audit or diff record is created and
// no publish, gray, promotion, rollback or history state changes. Version numbers are allocated
// independently per environment, so baseVersion may be greater than or equal to targetVersion.
func (s *server) handleCrossEnvironmentConfigVersionDiff(c *gin.Context) {
	namespace := c.Query("namespace")
	baseEnvironment := c.Query("baseEnvironment")
	targetEnvironment := c.Query("targetEnvironment")
	if namespace == "" || baseEnvironment == "" || targetEnvironment == "" {
		writeError(c, http.StatusBadRequest, "MISSING_SCOPE",
			"namespace, baseEnvironment and targetEnvironment are required")
		return
	}
	if baseEnvironment == targetEnvironment {
		writeError(c, http.StatusBadRequest, "SAME_ENVIRONMENT",
			"baseEnvironment and targetEnvironment must be different")
		return
	}
	base, baseOK := parseVersionParam(c, c.Query("baseVersion"))
	if !baseOK {
		return
	}
	target, targetOK := parseVersionParam(c, c.Query("targetVersion"))
	if !targetOK {
		return
	}

	// Lookups run base first, then target: a missing or out-of-scope base side fails before the
	// target side is inspected.
	if !s.versionAvailable(c, namespace, baseEnvironment, base) {
		return
	}
	if !s.versionAvailable(c, namespace, targetEnvironment, target) {
		return
	}

	baseVersion, err := s.store.GetVersion(c.Request.Context(), namespace, baseEnvironment, base)
	if err != nil {
		s.handleStorageError(c, err)
		return
	}
	targetVersion, err := s.store.GetVersion(c.Request.Context(), namespace, targetEnvironment, target)
	if err != nil {
		s.handleStorageError(c, err)
		return
	}
	baseItems, err := s.store.Items(c.Request.Context(), namespace, baseEnvironment, base)
	if err != nil {
		s.handleStorageError(c, err)
		return
	}
	targetItems, err := s.store.Items(c.Request.Context(), namespace, targetEnvironment, target)
	if err != nil {
		s.handleStorageError(c, err)
		return
	}
	baseEffective, err := s.store.EffectiveVersion(c.Request.Context(), namespace, baseEnvironment)
	if err != nil {
		s.handleStorageError(c, err)
		return
	}
	targetEffective, err := s.store.EffectiveVersion(c.Request.Context(), namespace, targetEnvironment)
	if err != nil {
		s.handleStorageError(c, err)
		return
	}
	c.JSON(http.StatusOK, BuildCrossEnvironmentVersionDiff(namespace,
		toVersionInfo(baseVersion, baseEffective),
		toVersionInfo(targetVersion, targetEffective),
		baseItems, targetItems))
}
