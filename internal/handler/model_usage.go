package handler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

// ModelUsageHandler 处理模型成本可观测（M2）相关的 HTTP 请求。
type ModelUsageHandler struct {
	service interfaces.ModelUsageService
}

// NewModelUsageHandler 创建模型成本可观测处理器。
func NewModelUsageHandler(service interfaces.ModelUsageService) *ModelUsageHandler {
	return &ModelUsageHandler{service: service}
}

// parseTimeQuery 解析时间查询参数。支持 RFC3339 与 Unix 秒两种格式；
// 缺省或非法时返回零值（由调用方决定是否回退到默认区间）。
func parseTimeQuery(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	if secs, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return time.Unix(secs, 0), nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, err
	}
	return t, nil
}

// defaultUsageWindow 返回最近 7 天的默认统计区间。
func defaultUsageWindow() (time.Time, time.Time) {
	now := time.Now()
	return now.Add(-7 * 24 * time.Hour), now
}

// GetOverview godoc
// @Summary      获取模型用量与成本概览
// @Description  按模型聚合当前空间在时间区间内的调用量、缓存命中率与费用
// @Tags         模型管理
// @Accept       json
// @Produce      json
// @Param        start  query     string  false  "起始时间（RFC3339 或 Unix 秒，缺省为最近 7 天）"
// @Param        end    query     string  false  "结束时间（RFC3339 或 Unix 秒，缺省为当前时间）"
// @Success      200    {object}  map[string]interface{}  "按模型聚合的用量与成本"
// @Failure      400    {object}  errors.AppError          "时间参数格式错误"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /models/usage [get]
func (h *ModelUsageHandler) GetOverview(c *gin.Context) {
	ctx := c.Request.Context()

	tenantID := c.GetUint64(types.TenantIDContextKey.String())
	if tenantID == 0 {
		logger.Error(ctx, "Tenant ID is empty")
		c.Error(errors.NewBadRequestError("Workspace ID cannot be empty"))
		return
	}

	start, end := defaultUsageWindow()
	if raw := c.Query("start"); raw != "" {
		parsed, err := parseTimeQuery(raw)
		if err != nil {
			c.Error(errors.NewBadRequestError("start 参数格式错误，需为 RFC3339 或 Unix 秒"))
			return
		}
		start = parsed
	}
	if raw := c.Query("end"); raw != "" {
		parsed, err := parseTimeQuery(raw)
		if err != nil {
			c.Error(errors.NewBadRequestError("end 参数格式错误，需为 RFC3339 或 Unix 秒"))
			return
		}
		end = parsed
	}

	rows, err := h.service.GetOverview(ctx, start, end)
	if err != nil {
		logger.ErrorWithFields(ctx, err, nil)
		c.Error(errors.NewInternalServerError(err.Error()))
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    rows,
	})
}
