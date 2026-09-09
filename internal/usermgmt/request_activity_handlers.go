package usermgmt

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt/store"
)

func (r *Runtime) requestActivitySince() time.Time {
	return time.Time{}
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
	prompt, sessionID := c.Query("q"), c.Query("session_id")
	if !validText(prompt, 2048) || !validText(sessionID, 512) || !validText(model, 256) || (status != "" && status != "success" && status != "error") {
		badRequest(c, "Invalid request activity filter")
		return
	}
	var items []store.RequestActivitySummary
	errList := r.withStore(false, func(db *store.Store) error {
		if _, errUser := db.GetUser(c.Request.Context(), userID); errUser != nil {
			return errUser
		}
		var errQuery error
		items, errQuery = db.ListRequestActivitySearch(c.Request.Context(), userID, limit, before, since, model, status, prompt, sessionID)
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

func (r *Runtime) getRequestActivityContent(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	direction := c.DefaultQuery("direction", "request")
	if direction != "request" && direction != "response" {
		badRequest(c, "direction must be request or response")
		return
	}
	limit := 20
	if raw, exists := c.GetQuery("limit"); exists {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			badRequest(c, "limit must be 1..100")
			return
		}
		limit = parsed
	}
	after := int64(0)
	if raw, exists := c.GetQuery("after"); exists {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			badRequest(c, "after must be a nonnegative cursor")
			return
		}
		after = parsed
	}
	var chunks []store.RequestContentChunk
	var item store.RequestActivity
	errGet := r.withStore(false, func(db *store.Store) error {
		var err error
		item, err = db.GetRequestActivity(c.Request.Context(), id, time.Time{})
		if err != nil {
			return err
		}
		chunks, err = db.ListRequestContent(c.Request.Context(), id, direction, after, limit+1)
		return err
	})
	if errGet != nil {
		respondError(c, errGet)
		return
	}
	next := int64(0)
	if len(chunks) > limit {
		chunks = chunks[:limit]
		next = chunks[len(chunks)-1].Sequence
	}
	complete := (item.CaptureState == "complete" || item.CaptureState == "interrupted" || item.CaptureState == "legacy_preview") && next == 0
	c.JSON(http.StatusOK, gin.H{"chunks": chunks, "next_after": next, "complete": complete, "capture_state": item.CaptureState})
}

// Kept separate so the shared management route owner can register atomically.
func (r *Runtime) registerActivityContentRoutes(group *gin.RouterGroup) {
	group.GET("/requests/:id/content", r.getRequestActivityContent)
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
