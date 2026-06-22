package auth

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// BundleAdminMiddleware 保护 bundle 管理 API。
// 目前使用固定 Bearer Token，便于最小化接入后台系统。
func BundleAdminMiddleware(expectedToken string) gin.HandlerFunc {
	return func(c *gin.Context) {
		expectedToken = strings.TrimSpace(expectedToken)
		if expectedToken == "" {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"error":   "bundle_admin_unconfigured",
				"message": "Bundle 管理 API 未配置管理员令牌",
			})
			return
		}

		rawAuthorization := strings.TrimSpace(c.GetHeader("Authorization"))
		const bearerPrefix = "Bearer "
		if !strings.HasPrefix(rawAuthorization, bearerPrefix) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error":   "unauthorized",
				"message": "缺少合法的 Bearer Token",
			})
			return
		}

		providedToken := strings.TrimSpace(strings.TrimPrefix(rawAuthorization, bearerPrefix))
		if subtle.ConstantTimeCompare([]byte(providedToken), []byte(expectedToken)) != 1 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error":   "unauthorized",
				"message": "Bundle 管理 API 令牌无效",
			})
			return
		}

		c.Next()
	}
}
