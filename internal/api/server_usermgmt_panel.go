package api

import (
	"bytes"
	"regexp"
)

var managementPanelHead = regexp.MustCompile(`(?i)<head(?:\s[^>]*)?>`)
var nativeManagementPanel = regexp.MustCompile(`(?i)<meta\b[^>]*\bname\s*=\s*["']?cpa-native-management(?:["'\s>])`)

// injectManagementSessionBridge adapts the downloaded panel at response time.
// authenticated is supplied by the server's management/session access checks;
// no cookie value or management credential is ever passed into this script.
func injectManagementSessionBridge(html []byte, authenticated bool) []byte {
	if bytes.Contains(html, []byte(`id="cpa-session-bridge"`)) {
		return html
	}
	flag := []byte("false")
	if authenticated {
		flag = []byte("true")
	}
	script := managementSessionBridge
	if nativeManagementPanel.Match(html) {
		script = nativeManagementSessionBootstrap
	}
	bridge := bytes.Replace([]byte(script), []byte("__CPA_AUTHENTICATED__"), flag, 1)
	position := 0
	if head := managementPanelHead.FindIndex(html); head != nil {
		position = head[1]
	}
	result := make([]byte, 0, len(html)+len(bridge))
	result = append(result, html[:position]...)
	result = append(result, bridge...)
	result = append(result, html[position:]...)
	return result
}

const managementSessionBridge = `<script id="cpa-session-bridge">
(() => {
  "use strict";
  const authenticated = __CPA_AUTHENTICATED__;
  const modeKey = "cpa-session-mode";
  const storage = window.localStorage;
  const removeItem = Storage.prototype.removeItem;
  const authKeys = ["cli-proxy-auth", "apiBase", "managementKey", "isLoggedIn"];
  const removeAuthState = () => {
    removeItem.call(storage, modeKey);
    for (const key of authKeys) removeItem.call(storage, key);
  };
  const namedMode = storage.getItem(modeKey) === "true";
  if (new URLSearchParams(location.search).get("legacy") === "1") {
    if (namedMode) removeAuthState();
    return;
  }
  if (!authenticated) {
    if (namedMode) location.replace("/login");
    return;
  }

  storage.setItem(modeKey, "true");
  storage.setItem("cli-proxy-auth", JSON.stringify({state: {
    apiBase: location.origin, managementKey: "cpa-session", rememberPassword: true
  }, version: 0}));
  storage.setItem("apiBase", JSON.stringify(location.origin));
  storage.setItem("managementKey", JSON.stringify("cpa-session"));
  storage.setItem("isLoggedIn", "true");

  let pending = false;
  let finished = false;
  let errorMessage = "";
  let button;
  let error;
  const update = () => {
    if (!button) return;
    button.disabled = pending;
    button.textContent = pending ? "Signing out…" : "Sign out";
    error.textContent = errorMessage;
    error.hidden = errorMessage === "";
  };
  const logout = async () => {
    if (pending || finished) return;
    pending = true;
    errorMessage = "";
    update();
    try {
      const response = await fetch("/v0/management/logout", {
        method: "POST", credentials: "same-origin",
        headers: {"Content-Type": "application/json"}, body: "{}"
      });
      if (!response.ok) throw new Error("logout_failed");
      finished = true;
      removeAuthState();
      location.replace("/login");
    } catch {
      errorMessage = "Sign out failed. Try again.";
    } finally {
      pending = false;
      update();
    }
  };

  // The upstream panel's own Logout action removes isLoggedIn. Complete that
  // action on the server before redirecting or clearing named-session mode.
  Storage.prototype.removeItem = function(key) {
    const result = removeItem.call(this, key);
    if (this === storage && String(key) === "isLoggedIn" && !finished && storage.getItem(modeKey) === "true") {
      void logout();
    }
    return result;
  };

  const mount = () => {
    if (document.getElementById("cpa-session-tools")) return;
    const tools = document.createElement("div");
    tools.id = "cpa-session-tools";
    tools.setAttribute("aria-label", "Account session");
    tools.style.cssText = "position:fixed;right:16px;bottom:16px;z-index:2147483647;padding:10px;background:#fff;color:#222;border:1px solid #aaa;border-radius:6px;font:14px system-ui,sans-serif;box-shadow:0 2px 8px #0002";
    button = document.createElement("button");
    button.type = "button";
    button.id = "cpa-session-logout";
    button.style.cssText = "cursor:pointer;padding:6px 12px;font:inherit";
    button.addEventListener("click", () => { void logout(); });
    error = document.createElement("p");
    error.id = "cpa-session-logout-error";
    error.setAttribute("role", "alert");
    error.style.cssText = "margin:8px 0 0;max-width:260px";
    tools.append(button, error);
    document.body.append(tools);
    update();
  };
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", mount, {once:true});
  } else {
    mount();
  }
})();
</script>`

// The integrated React panel owns navigation, notifications and logout. It only
// needs the cookie-backed auth bootstrap before its normal auth store starts.
const nativeManagementSessionBootstrap = `<script id="cpa-session-bridge">
(() => {
  "use strict";
  const authenticated = __CPA_AUTHENTICATED__;
  const storage = window.localStorage;
  const modeKey = "cpa-session-mode";
  const namedMode = storage.getItem(modeKey) === "true";
  if (new URLSearchParams(location.search).get("legacy") === "1") {
    if (namedMode) {
      for (const key of [modeKey, "cli-proxy-auth", "apiBase", "managementKey", "isLoggedIn"]) storage.removeItem(key);
    }
    return;
  }
  if (!authenticated) {
    if (namedMode) location.replace("/login");
    return;
  }
  storage.setItem(modeKey, "true");
  storage.setItem("cli-proxy-auth", JSON.stringify({state: {
    apiBase: location.origin, managementKey: "cpa-session", rememberPassword: true
  }, version: 0}));
  storage.setItem("apiBase", JSON.stringify(location.origin));
  storage.setItem("managementKey", JSON.stringify("cpa-session"));
  storage.setItem("isLoggedIn", "true");
})();
</script>`
