package usermgmt

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt/store"
)

func (r *Runtime) getPermissions(c *gin.Context) {
	userID, ok := pathID(c, "id")
	if !ok {
		return
	}
	var permissions []store.Permission
	errGet := r.withStore(false, func(db *store.Store) error {
		var errStore error
		permissions, errStore = db.ListPermissions(c.Request.Context(), userID)
		return errStore
	})
	if errGet != nil {
		respondError(c, errGet)
		return
	}
	c.JSON(http.StatusOK, gin.H{"permissions": permissions})
}

func (r *Runtime) replacePermissions(c *gin.Context) {
	userID, ok := pathID(c, "id")
	if !ok {
		return
	}
	var input struct {
		Permissions *[]store.Permission `json:"permissions"`
	}
	if !decodeRequest(c, &input) {
		return
	}
	if input.Permissions == nil {
		badRequest(c, "permissions must be an explicit array; use [] to clear it")
		return
	}
	permissions, errNormalize := normalizePermissions(*input.Permissions)
	if errNormalize != nil {
		badRequest(c, errNormalize.Error())
		return
	}
	errReplace := r.withStore(true, func(db *store.Store) error {
		return db.ReplacePermissions(c.Request.Context(), userID, permissions)
	})
	if errReplace != nil {
		respondError(c, errReplace)
		return
	}
	c.JSON(http.StatusOK, gin.H{"permissions": permissions})
}
