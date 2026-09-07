package usermgmt

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/oklog/ulid/v2"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt/store"
)

func (r *Runtime) listUsage(c *gin.Context) {
	period := c.DefaultQuery("period", time.Now().UTC().Format("2006-01"))
	parsed, errPeriod := time.Parse("2006-01", period)
	if errPeriod != nil || parsed.Format("2006-01") != period || parsed.Year() < 1 {
		badRequest(c, "period must be YYYY-MM")
		return
	}
	limit, offset, ok := parsePagination(c)
	if !ok {
		return
	}
	userID := c.Query("user_id")
	if userID != "" {
		id, errID := ulid.ParseStrict(userID)
		if errID != nil {
			badRequest(c, "Invalid resource identifier")
			return
		}
		userID = id.String()
	}
	var items []store.MonthlyUsage
	var total int64
	errList := r.withStore(false, func(db *store.Store) error {
		if userID != "" {
			item, errGet := db.GetMonthlyUsage(c.Request.Context(), userID, period)
			if errGet != nil {
				return errGet
			}
			items = make([]store.MonthlyUsage, 0, 1)
			if offset == 0 {
				items = append(items, item)
			}
			total = 1
			return nil
		}
		var errStore error
		items, total, errStore = db.ListMonthlyUsage(c.Request.Context(), period, limit, offset)
		return errStore
	})
	if errList != nil {
		respondError(c, errList)
		return
	}
	c.JSON(http.StatusOK, gin.H{"period": period, "usage": items, "total": total, "limit": limit, "offset": offset})
}
