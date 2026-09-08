package usermgmt

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt/store"
)

func (r *Runtime) requestActivitySince() time.Time {
	_, cfg := r.Snapshot()
	return time.Now().UTC().Add(-time.Duration(cfg.WithDefaults().RequestActivity.RetentionDays) * 24 * time.Hour)
}

func (r *Runtime) listRequestActivity(c *gin.Context) {
	userID, ok := pathID(c, "id")
	if !ok {
		return
	}
	limit, offset, ok := parsePagination(c)
	if !ok {
		return
	}
	if offset != 0 {
		badRequest(c, "Request activity pagination uses before")
		return
	}
	before := int64(0)
	if values, present := c.Request.URL.Query()["before"]; present {
		var errBefore error
		if len(values) != 1 {
			badRequest(c, "Invalid request activity cursor")
			return
		}
		before, errBefore = strconv.ParseInt(values[0], 10, 64)
		if errBefore != nil || before <= 0 {
			badRequest(c, "before must be a positive cursor")
			return
		}
	}
	since := r.requestActivitySince()
	if raw, exists := c.GetQuery("since"); exists {
		parsed, errSince := time.Parse(time.RFC3339, raw)
		if errSince != nil {
			badRequest(c, "since must be RFC3339")
			return
		}
		if parsed.After(since) {
			since = parsed
		}
	}
	model, status := c.Query("model"), c.Query("status")
	if !validText(model, 256) || (status != "" && status != "success" && status != "error") {
		badRequest(c, "Invalid request activity filter")
		return
	}
	var items []store.RequestActivitySummary
	errList := r.withStore(false, func(db *store.Store) error {
		if _, errUser := db.GetUser(c.Request.Context(), userID); errUser != nil {
			return errUser
		}
		var errQuery error
		items, errQuery = db.ListRequestActivityFiltered(c.Request.Context(), userID, limit, before, since, model, status)
		return errQuery
	})
	if errList != nil {
		respondError(c, errList)
		return
	}
	next := int64(0)
	if len(items) == limit {
		next = items[len(items)-1].Sequence
	}
	c.JSON(http.StatusOK, gin.H{"requests": items, "next_before": next})
}

func (r *Runtime) getRequestActivity(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	var item store.RequestActivity
	since := r.requestActivitySince()
	errGet := r.withStore(false, func(db *store.Store) error {
		var errQuery error
		item, errQuery = db.GetRequestActivity(c.Request.Context(), id, since)
		return errQuery
	})
	if errGet != nil {
		respondError(c, errGet)
		return
	}
	c.JSON(http.StatusOK, gin.H{"request": item})
}
