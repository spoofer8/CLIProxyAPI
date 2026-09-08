package usermgmt

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usermgmt/store"
	"golang.org/x/crypto/bcrypt"
)

const SessionCookieName = "cpa_session"
const SessionMarker = "cpa-session"

var ErrInvalidSession = errors.New("invalid or expired panel session")
var ErrInvalidCredentials = errors.New("invalid email or password")

type SessionIdentity = store.SessionIdentity

type ManagementLoginGuard interface {
	CheckManagementAccess(string, bool) (bool, int, string)
	RecordAuthenticationFailure(string)
	ResetAuthenticationFailures(string)
}

func (r *Runtime) SetManagementLoginGuard(guard ManagementLoginGuard) {
	if r == nil {
		return
	}
	r.loginGuardMu.Lock()
	r.loginGuard = guard
	r.loginGuardMu.Unlock()
}

func sessionHash(raw string) string {
	digest := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(digest[:])
}
func (r *Runtime) AuthenticateSession(ctx context.Context, rawCookie string) (*SessionIdentity, error) {
	decoded, errDecode := base64.RawURLEncoding.DecodeString(rawCookie)
	if errDecode != nil || len(decoded) != 32 {
		return nil, ErrInvalidSession
	}
	var identity SessionIdentity
	errSession := r.withStore(false, func(db *store.Store) error {
		var err error
		identity, err = db.GetSession(ctx, sessionHash(rawCookie))
		return err
	})
	if errors.Is(errSession, store.ErrNotFound) || errors.Is(errSession, ErrDisabled) {
		return nil, ErrInvalidSession
	}
	if errSession != nil {
		return nil, errSession
	}
	return &identity, nil
}

func setSessionCookie(c *gin.Context, value string, expires time.Time) {
	http.SetCookie(c.Writer, &http.Cookie{Name: SessionCookieName, Value: value, Path: "/", Expires: expires, HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: false})
}
func clearSessionCookie(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{Name: SessionCookieName, Value: "", Path: "/", MaxAge: -1, Expires: time.Unix(1, 0), HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: false})
}

func (r *Runtime) Login(c *gin.Context) {
	_, cfg := r.Snapshot()
	if !cfg.Enabled {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	r.loginGuardMu.RLock()
	guard := r.loginGuard
	r.loginGuardMu.RUnlock()
	ip := c.ClientIP()
	local := ip == "127.0.0.1" || ip == "::1"
	if guard == nil {
		respondError(c, errors.New("management login guard unavailable"))
		return
	}
	if allowed, status, message := guard.CheckManagementAccess(ip, local); !allowed {
		c.JSON(status, gin.H{"error": message})
		return
	}
	var input struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	form := strings.HasPrefix(c.GetHeader("Content-Type"), "application/x-www-form-urlencoded")
	if form {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)
		if errForm := c.Request.ParseForm(); errForm != nil {
			badRequest(c, "Invalid login form")
			return
		}
		input.Email = c.Request.PostForm.Get("email")
		input.Password = c.Request.PostForm.Get("password")
	} else if !decodeRequest(c, &input) {
		return
	}
	input.Email = strings.ToLower(strings.TrimSpace(input.Email))
	var user store.LoginUser
	errLookup := r.withStore(false, func(db *store.Store) error {
		var err error
		user, err = db.LoginUser(c.Request.Context(), input.Email)
		return err
	})
	if errLookup != nil && !errors.Is(errLookup, store.ErrNotFound) {
		respondError(c, errLookup)
		return
	}
	valid := false
	if errLookup == nil && user.PasswordHash != nil && len(input.Password) <= 72 {
		valid = bcrypt.CompareHashAndPassword([]byte(*user.PasswordHash), []byte(input.Password)) == nil && user.Role == "admin" && user.Status == "active"
	}
	if !valid {
		r.loginFailed(c, guard, user.ID, form)
		return
	}
	random := make([]byte, 32)
	if _, errRandom := rand.Read(random); errRandom != nil {
		respondError(c, errors.New("generate panel session failed"))
		return
	}
	raw := base64.RawURLEncoding.EncodeToString(random)
	ttl, _ := time.ParseDuration(cfg.Session.TTL)
	expires := time.Now().UTC().Add(ttl)
	actorCtx := WithAuditActor(c.Request.Context(), AuditActor{ID: user.ID, ClientIP: ip})
	errCreate := r.mutate(actorCtx, AuditEvent{Action: "session.login", Target: user.ID}, func(db *store.Store) error {
		return db.CreateSession(actorCtx, sessionHash(raw), user.ID, *user.PasswordHash, ip, expires)
	})
	if errors.Is(errCreate, store.ErrCredentialsChanged) || errors.Is(errCreate, store.ErrNotFound) {
		r.loginFailed(c, guard, user.ID, form)
		return
	}
	if errCreate != nil {
		respondError(c, errCreate)
		return
	}
	guard.ResetAuthenticationFailures(ip)
	setSessionCookie(c, raw, expires)
	c.Header("Cache-Control", "no-store")
	if form {
		c.Redirect(http.StatusSeeOther, "/management.html")
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "user": SessionIdentity{UserID: user.ID, Email: user.Email, DisplayName: user.DisplayName, Role: user.Role, ExpiresAt: expires}, "redirect": "/management.html"})
}

func (r *Runtime) loginFailed(c *gin.Context, guard ManagementLoginGuard, userID string, form bool) {
	guard.RecordAuthenticationFailure(c.ClientIP())
	ctx := WithAuditActor(c.Request.Context(), AuditActor{ClientIP: c.ClientIP()})
	if errAudit := r.RecordAudit(ctx, AuditEvent{Action: "session.login_failed", Target: userID, Detail: map[string]any{"reason": "invalid_credentials"}}); errAudit != nil {
		respondError(c, errAudit)
		return
	}
	if form {
		c.Redirect(http.StatusSeeOther, "/login?error=invalid")
		return
	}
	c.JSON(http.StatusUnauthorized, gin.H{"error": ErrInvalidCredentials.Error()})
}

func (r *Runtime) Logout(c *gin.Context) {
	if active, _ := r.Snapshot(); active == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	cookie, errCookie := c.Request.Cookie(SessionCookieName)
	if errCookie != nil {
		clearSessionCookie(c)
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
		return
	}
	identity, errSession := r.AuthenticateSession(c.Request.Context(), cookie.Value)
	if errors.Is(errSession, ErrInvalidSession) {
		clearSessionCookie(c)
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
		return
	}
	if errSession != nil {
		respondError(c, errSession)
		return
	}
	ctx := WithAuditActor(c.Request.Context(), AuditActor{ID: identity.UserID, ClientIP: c.ClientIP()})
	errDelete := r.mutate(ctx, AuditEvent{Action: "session.logout", Target: identity.UserID}, func(db *store.Store) error { _, err := db.DeleteSession(ctx, sessionHash(cookie.Value)); return err })
	if errDelete != nil && !errors.Is(errDelete, store.ErrNotFound) {
		respondError(c, errDelete)
		return
	}
	clearSessionCookie(c)
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (r *Runtime) setPassword(c *gin.Context) {
	userID, ok := pathID(c, "id")
	if !ok {
		return
	}
	var input struct {
		Password json.RawMessage `json:"password"`
	}
	if !decodeRequest(c, &input) {
		return
	}
	if input.Password == nil {
		badRequest(c, "password is required; null or an empty string clears it")
		return
	}
	var password *string
	if errJSON := json.Unmarshal(input.Password, &password); errJSON != nil {
		badRequest(c, "password must be a string or null")
		return
	}
	var hash *string
	if password != nil && *password != "" {
		if len(*password) < 8 || len(*password) > 72 {
			badRequest(c, "password must contain 8..72 bytes")
			return
		}
		encoded, errHash := bcrypt.GenerateFromPassword([]byte(*password), bcrypt.DefaultCost)
		if errHash != nil {
			respondError(c, errors.New("hash panel password failed"))
			return
		}
		value := string(encoded)
		hash = &value
	}
	errSet := r.mutate(c.Request.Context(), AuditEvent{Action: "password.update", Target: userID, Detail: map[string]any{"cleared": hash == nil}}, func(db *store.Store) error { return db.SetPassword(c.Request.Context(), userID, hash) })
	if errors.Is(errSet, store.ErrCredentialsChanged) {
		badRequest(c, "Only admin accounts can have a panel password")
		return
	}
	if errSet != nil {
		respondError(c, errSet)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

var loginPage = template.Must(template.New("login").Parse(`<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Sign in · CLIProxyAPI</title><style>body{font:16px system-ui,sans-serif;background:#f6f7fa;color:#1e2430;margin:0;min-height:100vh;display:grid;place-items:center}main{width:min(340px,calc(100vw - 64px));background:white;padding:36px;border:1px solid #e1e4ec;border-radius:16px}h1{font-size:26px;margin:0 0 8px}p{color:#5f6776;line-height:1.5}label{display:block;margin:20px 0 7px;font-size:14px}input{box-sizing:border-box;width:100%;padding:12px;border:1px solid #bbc2cf;border-radius:7px;font:inherit}button{width:100%;padding:13px;margin-top:24px;background:#273c70;color:white;border:0;border-radius:7px;font:inherit;cursor:pointer}.error{color:#ad2635}</style><main><h1>Sign in</h1><p>Manage CLIProxyAPI with your admin account.</p>{{if .Error}}<p class="error" role="alert">Invalid email or password.</p>{{end}}<form method="post" action="/v0/management/login"><label for="email">Email</label><input id="email" name="email" type="email" autocomplete="username" required autofocus><label for="password">Password</label><input id="password" name="password" type="password" autocomplete="current-password" required><button type="submit">Sign in</button></form></main></html>`))

func (r *Runtime) LoginPage(c *gin.Context) {
	if active, _ := r.Snapshot(); active == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Header("Content-Type", "text/html; charset=utf-8")
	if errRender := loginPage.Execute(c.Writer, struct{ Error bool }{Error: c.Query("error") == "invalid"}); errRender != nil {
		c.Abort()
	}
}
