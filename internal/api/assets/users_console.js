(() => {
  "use strict";
  const $ = id => document.getElementById(id);
  const state = {key: "", users: [], total: 0, user: null, settings: null, requests: [], before: 0, detail: null, raw: "", selection: 0, activity: 0};
  const number = value => value == null ? "—" : new Intl.NumberFormat().format(value);
  const label = user => user.display_name || user.email;
  const node = (tag, className, text) => {
    const element = document.createElement(tag);
    if (className) element.className = className;
    if (text != null) element.textContent = String(text);
    return element;
  };
  const message = (id, text) => { $(id).textContent = text; $(id).hidden = !text; };
  const report = error => message("page-error", error.message || "The request failed. Try again.");
  const run = action => async event => { try { await action(event); } catch (error) { report(error); } };
  let toastTimer;
  function toast(text) {
    message("toast", text);
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => { $("toast").hidden = true; }, 3500);
  }
  async function api(path, options = {}) {
    const headers = {Accept: "application/json"};
    if (state.key) headers.Authorization = "Bearer " + state.key;
    if (options.body !== undefined) headers["Content-Type"] = "application/json";
    const response = await fetch("/v0/management" + path, {credentials: "same-origin", cache: "no-store", ...options, headers, body: options.body === undefined ? undefined : JSON.stringify(options.body)});
    const result = await response.json().catch(() => ({}));
    if (!response.ok) {
      const error = new Error(typeof result.error === "string" ? result.error : result.error?.message || "Request failed (" + response.status + ").");
      error.status = response.status;
      if (response.status === 401) showAuth("Your management session has expired. Connect again.");
      throw error;
    }
    return result;
  }
  function storedKey() {
    try {
      // Compatibility with the upstream panel's versioned storage codec.
      const read = name => {
        let raw = localStorage.getItem(name);
        if (!raw) return null;
        if (raw.startsWith("enc::v1::")) {
          const key = new TextEncoder().encode("cli-proxy-api-webui::secure-storage|" + location.host + "|" + navigator.userAgent);
          const bytes = Uint8Array.from(atob(raw.slice(9)), value => value.charCodeAt(0));
          raw = new TextDecoder().decode(bytes.map((value, index) => value ^ key[index % key.length]));
        }
        try { return JSON.parse(raw); } catch { return raw; }
      };
      const saved = read("cli-proxy-auth");
      const auth = saved?.state;
      const apiBase = auth?.apiBase || read("apiBase");
      if (apiBase && new URL(apiBase, location.origin).origin !== location.origin) return "";
      const value = auth?.managementKey || read("managementKey");
      return typeof value === "string" && value !== "cpa-session" ? value : "";
    } catch { return ""; }
  }
  function showAuth(error = "") {
    $("auth-panel").hidden = false;
    $("workspace").hidden = true;
    $("connection-label").textContent = "Not connected";
    message("auth-error", error);
  }
  async function connect(key) {
    state.key = key;
    const result = await api("/users?limit=100");
    state.users = result.users || [];
    state.total = result.total;
    state.user = null;
    closeInspector();
    $("account").hidden = true;
    $("no-user").hidden = false;
    $("auth-panel").hidden = true;
    $("workspace").hidden = false;
    $("management-key").value = "";
    $("connection-label").textContent = key ? "Management key" : "Admin session";
    message("page-error", "");
    renderUsers();
    await loadSettings();
    if (state.users.length) await selectUser(state.users[0].id);
  }
  function renderUsers() {
    const query = $("user-search").value.trim().toLowerCase();
    const users = state.users.filter(user => (label(user) + " " + user.email).toLowerCase().includes(query));
    $("user-list").replaceChildren();
    for (const user of users) {
      const button = node("button", "user-item" + (user.id === state.user?.id ? " selected" : ""));
      button.type = "button";
      button.setAttribute("aria-pressed", String(user.id === state.user?.id));
      const heading = node("span", "user-item-heading");
      heading.append(node("span", "user-item-name", label(user)), node("span", "status-dot" + (user.status === "active" ? "" : " disabled")));
      const limit = user.monthly_token_limit;
      button.append(heading, node("div", "user-item-email", user.email), node("div", "user-item-usage", limit == null ? "Global quota default" : limit <= 0 ? "Unlimited tokens" : number(limit) + " tokens / month"));
      button.addEventListener("click", run(() => selectUser(user.id)));
      $("user-list").append(button);
    }
    if (!users.length) $("user-list").append(node("p", "empty hint", query ? "No matching loaded users." : "No users yet. Create your first account."));
    $("user-count").textContent = query ? users.length + " matches in " + state.users.length + " loaded" : state.users.length + " of " + state.total + " users";
    $("more-users").hidden = state.users.length >= state.total;
  }
  async function loadUsers(more = false) {
    const response = await api("/users?limit=100&offset=" + (more ? state.users.length : 0));
    state.users = more ? state.users.concat(response.users || []) : response.users || [];
    state.total = response.total;
    renderUsers();
  }
  async function loadSettings() {
    state.settings = await api("/user-management/settings");
    renderSettings();
  }
  function renderSettings() {
    if (!state.settings) return;
    const {quota, request_activity: capture} = state.settings;
    $("sidebar-enforcement").textContent = quota.enforce ? "Limits enforced" : "Tracking only · limits not enforced";
    $("enforcement-badge").textContent = quota.enforce ? "Limits enforced" : "Tracking only";
    $("enforcement-badge").className = "badge " + (quota.enforce ? "" : "neutral");
    $("retention-note").textContent = capture.enabled ? "Sanitized request content · " + capture.retention_days + " days retained" : "New request capture is off · retained activity remains available";
    $("capture-settings").textContent = (capture.enabled ? "Capture is on. " : "Capture is off. ") + "Activity is retained for " + capture.retention_days + " days. Credentials and file data are omitted; large content is truncated. Configure capture in the server config.";
    renderUsage();
  }
  async function selectUser(id) {
    const selection = ++state.selection;
    state.activity++;
    state.user = state.users.find(user => user.id === id) || {id};
    state.requests = [];
    closeInspector();
    renderUsers();
    $("account").hidden = true;
    $("no-user").hidden = true;
    message("page-error", "");
    const user = await api("/users/" + encodeURIComponent(id));
    if (selection !== state.selection) return;
    state.user = user;
    $("user-name").textContent = label(user);
    $("user-email").textContent = user.email + (user.role === "admin" ? " · Administrator" : "");
    $("user-status").textContent = user.status === "active" ? "Active" : "Disabled";
    $("user-status").className = "badge" + (user.status === "active" ? "" : " disabled");
    $("account").hidden = false;
    renderUsage();
    await loadRequests();
  }
  function renderUsage() {
    const user = state.user;
    if (!user) return;
    const usage = user.usage || {};
    const limit = user.monthly_token_limit ?? state.settings?.quota?.["default-monthly-tokens"] ?? 0;
    $("month-label").textContent = new Date().toLocaleDateString(undefined, {month: "long", year: "numeric", timeZone: "UTC"}) + " · UTC";
    $("used-tokens").textContent = number(usage.total_tokens ?? 0);
    $("input-tokens").textContent = number(usage.input_tokens ?? 0);
    $("output-tokens").textContent = number(usage.output_tokens ?? 0);
    $("request-count").textContent = number(usage.request_count ?? 0);
    const pct = limit > 0 ? Math.min(100, (usage.total_tokens || 0) / limit * 100) : 0;
    $("quota-progress").style.width = pct + "%";
    $("quota-progress").classList.toggle("exceeded", pct >= 100);
    $("quota-description").textContent = (limit > 0 ? "of " + number(limit) + " tokens / month" : "Unlimited monthly tokens") + (user.monthly_token_limit == null ? " · global default" : " · user override") + (limit > 0 && !state.settings?.quota.enforce ? " · not enforced" : "");
  }
  async function loadRequests(more = false) {
    if (!state.user) return;
    const selection = state.selection, activity = ++state.activity, userID = state.user.id;
    const query = new URLSearchParams({limit: "50"});
    if (more && state.before) query.set("before", String(state.before));
    if ($("model-filter").value.trim()) query.set("model", $("model-filter").value.trim());
    if ($("status-filter").value) query.set("status", $("status-filter").value);
    if ($("time-filter").value) query.set("since", new Date(Date.now() - Number($("time-filter").value) * 3600000).toISOString());
    if (!more) {
      state.requests = [];
      closeInspector();
      $("request-list").replaceChildren();
      $("requests-empty").hidden = true;
      $("more-requests").hidden = true;
      $("log-count").textContent = "Loading…";
    }
    const result = await api("/users/" + encodeURIComponent(userID) + "/requests?" + query);
    if (selection !== state.selection || activity !== state.activity) return;
    state.requests = more ? state.requests.concat(result.requests || []) : result.requests || [];
    state.before = result.next_before;
    renderRequests();
  }
  function renderRequests() {
    $("request-list").replaceChildren();
    for (const request of state.requests) {
      const row = node("tr", request.id === state.detail?.id ? "selected" : "");
      row.tabIndex = 0;
      row.setAttribute("aria-label", "Inspect " + (request.model || "request") + " at " + request.at);
      const at = new Date(request.at);
      const date = node("td", "request-date", at.toLocaleTimeString([], {hour: "2-digit", minute: "2-digit", second: "2-digit"}));
      date.append(node("small", "", at.toLocaleDateString([], {month: "short", day: "numeric"})));
      const model = node("td");
      model.append(node("div", "request-model", request.model || "Model not specified"), node("div", "request-path", request.method + " " + request.path));
      const status = node("td");
      status.append(node("span", "badge " + (request.status_code >= 400 ? "error" : request.status_code ? "" : "neutral"), request.status_code || "In progress"));
      row.append(date, model, status, node("td", "numeric", number(request.total_tokens)), node("td", "numeric duration", request.duration_ms == null ? "—" : request.duration_ms < 1000 ? request.duration_ms + " ms" : (request.duration_ms / 1000).toFixed(1) + " s"));
      const open = run(() => inspect(request.id));
      row.addEventListener("click", open);
      row.addEventListener("keydown", event => { if (event.key === "Enter" || event.key === " ") { event.preventDefault(); void open(event); } });
      $("request-list").append(row);
    }
    $("requests-empty").hidden = state.requests.length !== 0;
    $("log-count").textContent = state.requests.length + " requests loaded";
    $("more-requests").hidden = !state.before;
  }
  function closeInspector() {
    state.detail = null;
    state.raw = "";
    $("conversation").replaceChildren();
    $("raw-content").textContent = "";
    $("inspector").hidden = true;
    $("activity-grid").classList.remove("with-inspector");
  }
  function jsonBlock(container, title, value) {
    const details = node("details", "tool-call");
    details.append(node("summary", "", title), node("pre", "tool-json", typeof value === "string" ? value : JSON.stringify(value, null, 2)));
    container.append(details);
  }
  function textBlock(container, text) {
    // Treat all content as text. Fenced code gets a readable block without HTML.
    const chunks = String(text).split(/```[^\n]*\n([\s\S]*?)```/g);
    chunks.forEach((chunk, index) => { if (chunk) container.append(node(index % 2 ? "pre" : "div", index % 2 ? "code-block" : "message-content", chunk)); });
  }
  function content(container, value, depth = 0) {
    if (value == null) return;
    if (depth > 12) { jsonBlock(container, "Nested content", value); return; }
    if (typeof value === "string") { textBlock(container, value); return; }
    if (Array.isArray(value)) {
      if (value.length > 200) container.append(node("p", "hint", "Showing the latest 200 parts. The retained payload is available in Sanitized JSON."));
      value.slice(-200).forEach(part => content(container, part, depth + 1)); return;
    }
    if (typeof value !== "object") { textBlock(container, String(value)); return; }
    const type = value.type || "";
    if (value.text != null) textBlock(container, value.text);
    else if (value.refusal != null) textBlock(container, value.refusal);
    else if (type === "tool_use" || type === "function_call" || value.functionCall) {
      const call = value.functionCall || value;
      jsonBlock(container, "Tool call · " + (call.name || "function"), call.input ?? call.args ?? call.arguments ?? call);
    } else if (type === "tool_result" || type === "function_call_output" || value.functionResponse) {
      const output = value.functionResponse || value;
      jsonBlock(container, "Tool result · " + (output.name || output.tool_use_id || output.call_id || ""), output.response ?? output.output ?? output.content ?? output);
    } else if (/image|audio|video|file/.test(type) || value.inlineData || value.inline_data || value.fileData || value.file_data) {
      container.append(node("div", "attachment", "Attachment · " + (type || "file") + " (not loaded)"));
      jsonBlock(container, "Attachment metadata", value);
    } else if (value.content != null || value.parts != null) {
      content(container, value.content ?? value.parts, depth + 1);
    } else {
      jsonBlock(container, "Structured content", value);
    }
  }
  function addMessage(role, value, extra = {}) {
    role = typeof role === "string" ? role : "other";
    const knownRole = ["user", "assistant", "system", "developer", "tool", "function"].includes(role) ? role : "other";
    const section = node("section", "message " + knownRole);
    const heading = role.charAt(0).toUpperCase() + role.slice(1) + (extra.name ? " · " + extra.name : "");
    section.append(node("h4", "message-role", heading));
    content(section, value);
    if (Array.isArray(extra.tool_calls)) extra.tool_calls.slice(-200).forEach(call => {
      if (!call || typeof call !== "object") { jsonBlock(section, "Tool call", call); return; }
      jsonBlock(section, "Tool call · " + (call.function?.name || call.name || call.id || "function"), call.function?.arguments ?? call);
    });
    if (extra.function_call) jsonBlock(section, "Function call · " + (extra.function_call.name || "function"), extra.function_call.arguments ?? extra.function_call);
    if (extra.tool_call_id) section.append(node("p", "hint", "Tool call " + extra.tool_call_id));
    $("conversation").append(section);
  }
  function renderConversation(body) {
    $("conversation").replaceChildren();
    if (!body || typeof body !== "object") { addMessage("request", body || "No request content was retained."); return; }
	if (body._preview && body.content) {
	  $("conversation").append(node("p", "hint", "A shortened preview of the latest submitted content is shown."));
	  const latest = body.content.latest_user_content ?? body.content;
	  addMessage(typeof latest?.role === "string" ? latest.role : "user", latest?.content ?? latest?.parts ?? latest, latest && typeof latest === "object" ? latest : {});
	  return;
	}
    if (body.system) addMessage("system", body.system);
    if (body.instructions) addMessage("system", body.instructions);
    if (body.systemInstruction || body.system_instruction) addMessage("system", body.systemInstruction || body.system_instruction);
    let messages = body.messages ?? body.input ?? body.contents ?? body.prompt;
    if (typeof messages === "string") addMessage("user", messages);
    else if (Array.isArray(messages)) {
      if (messages.length > 200) $("conversation").append(node("p", "hint", "Showing the latest 200 messages. The retained payload is available in Sanitized JSON."));
      for (const entry of messages.slice(-200)) {
        if (typeof entry === "string") { addMessage("user", entry); continue; }
        if (!entry || typeof entry !== "object") continue;
        const role = entry.role === "model" ? "assistant" : entry.role || (/tool|function/.test(entry.type || "") ? "tool" : "user");
        addMessage(role, entry.content ?? entry.parts ?? entry, entry);
      }
    } else if (messages != null) addMessage("user", messages);
    if (body.tools?.length) jsonBlock($("conversation"), "Available tools · " + body.tools.length, body.tools);
    if (body.functions?.length) jsonBlock($("conversation"), "Available functions · " + body.functions.length, body.functions);
    if (!$("conversation").childElementCount) {
      $("conversation").append(node("p", "hint", "This request has no recognized conversation fields. Expand the sanitized payload to read its content."));
      jsonBlock($("conversation"), "Request payload", body);
    }
  }
  let detailSequence = 0;
  async function inspect(id) {
    const selection = state.selection, sequence = ++detailSequence;
    const result = await api("/requests/" + encodeURIComponent(id));
    if (selection !== state.selection || sequence !== detailSequence) return;
    state.detail = result.request;
    const detail = state.detail;
    let parsed = null;
    try { parsed = JSON.parse(detail.body_preview); } catch { /* Older omitted previews may be empty. */ }
    state.raw = parsed == null ? detail.body_preview || "No request body was retained." : JSON.stringify(parsed, null, 2);
    $("raw-content").textContent = state.raw;
    $("detail-meta").textContent = new Date(detail.at).toLocaleString();
    $("detail-summary").replaceChildren();
    for (const value of [detail.model, detail.provider, detail.method + " " + detail.path, detail.status_code ? "HTTP " + detail.status_code : "In progress", detail.total_tokens == null ? "Tokens pending / unavailable" : number(detail.total_tokens) + " tokens"]) {
      if (value) $("detail-summary").append(node("span", "", value));
    }
    const notes = [];
    if (detail.body_truncated) notes.push("Some submitted content was omitted from this preview.");
    if (detail.body_omitted_reason) notes.push(({empty_body:"This request had no body.",body_not_read:"The request was rejected before its content was read.",inspection_limit:"Content exceeded the 4 MiB capture limit.",invalid_json:"Invalid JSON content was not retained.",content_encoding:"Compressed request content was not retained.",preview_limit:"Content was too large to retain."})[detail.body_omitted_reason] || "Request content was unavailable.");
    message("truncation-note", notes.join(" "));
    renderConversation(parsed ?? detail.body_preview);
    $("inspector").hidden = false;
    $("activity-grid").classList.add("with-inspector");
    selectTab(false);
    renderRequests();
    $("close-inspector").focus({preventScroll: true});
  }
  function selectTab(raw) {
    $("conversation").hidden = raw;
    $("raw-content").hidden = !raw;
    $("conversation-tab").setAttribute("aria-selected", String(!raw));
    $("raw-tab").setAttribute("aria-selected", String(raw));
  }
  function openDialog(id) {
    const dialog = $(id), error = dialog.querySelector(".form-error");
    if (error) { error.hidden = true; error.textContent = ""; }
    dialog.showModal();
  }
  function openQuota() {
    if (!state.user) return;
    const limit = state.user.monthly_token_limit;
    $("quota-user").textContent = label(state.user);
    $("quota-mode").value = limit == null ? "inherit" : limit <= 0 ? "unlimited" : "custom";
    $("quota-value").value = limit > 0 ? limit : "";
    quotaMode();
    $("quota-form-note").textContent = state.settings?.quota.enforce ? "Limits are enforced for this account." : "Enforcement is off. Usage is tracked without blocking requests.";
    openDialog("quota-dialog");
  }
  function quotaMode() {
    const custom = $("quota-mode").value === "custom";
    $("custom-quota-field").hidden = !custom;
    $("quota-value").required = custom;
  }
  function openSettings() {
    if (!state.settings) return;
    $("default-limit").value = state.settings.quota["default-monthly-tokens"];
    $("enforce-quotas").checked = state.settings.quota.enforce;
    openDialog("settings-dialog");
  }
  function integer(id, minimum) {
    const raw = $(id).value.trim(), value = Number(raw);
    if (!raw || !Number.isSafeInteger(value) || value < minimum) throw new Error("Enter a whole number of tokens, at least " + minimum + ".");
    return value;
  }
  function onForm(id, save) {
    $(id).addEventListener("submit", async event => {
      event.preventDefault();
      const form = event.currentTarget, submit = form.querySelector('[type="submit"]');
      const error = form.querySelector(".form-error") || $("keys-error");
      error.hidden = true;
      submit.disabled = true;
      try { await save(); } catch (failure) { error.textContent = failure.message; error.hidden = false; }
      finally { submit.disabled = false; }
    });
  }
  let keysUserID = "";
  async function loadKeys() {
    const id = keysUserID;
    const result = await api("/users/" + encodeURIComponent(id) + "/keys?limit=200");
    if (id !== keysUserID) return;
    $("keys-list").replaceChildren();
    for (const key of result.keys || []) {
      const row = node("div", "key-row"), info = node("div");
      info.append(node("strong", "", key.label || "Untitled key"), node("p", "hint", key.key_prefix + "… · " + key.status));
      row.append(info);
      if (key.status === "active") {
        const revoke = node("button", "", "Revoke");
        revoke.type = "button";
        revoke.addEventListener("click", async () => {
          if (!window.confirm("Revoke this API key? Clients using it will lose access.")) return;
          revoke.disabled = true;
          try { await api("/keys/" + encodeURIComponent(key.id), {method: "DELETE"}); await loadKeys(); toast("API key revoked"); }
          catch (error) { message("keys-error", error.message); revoke.disabled = false; }
        });
        row.append(revoke);
      }
      $("keys-list").append(row);
    }
    if (!result.keys?.length) $("keys-list").append(node("p", "empty hint", "No API keys yet. Issue a key below."));
    if (result.total > 200) $("keys-list").append(node("p", "hint", "Showing the first 200 keys. Manage additional keys through the management API."));
  }
  async function openKeys() {
    if (!state.user) return;
    keysUserID = state.user.id;
    $("keys-user").textContent = label(state.user);
    $("new-key-panel").hidden = true;
    $("new-key-value").textContent = "";
    message("keys-error", "");
    $("keys-list").replaceChildren(node("p", "hint", "Loading keys…"));
    openDialog("keys-dialog");
    try { await loadKeys(); } catch (error) { message("keys-error", error.message); }
  }
  onForm("quota-form", async () => {
    const mode = $("quota-mode").value, id = state.user.id;
    const limit = mode === "inherit" ? null : mode === "unlimited" ? 0 : integer("quota-value", 1);
    const updated = await api("/users/" + encodeURIComponent(id), {method: "PATCH", body: {monthly_token_limit: limit}});
    state.users = state.users.map(user => user.id === id ? {...user, ...updated} : user);
    if (state.user?.id === id) { state.user = {...state.user, ...updated}; renderUsage(); }
    renderUsers();
    $("quota-dialog").close();
    toast("Monthly quota saved");
  });
  onForm("settings-form", async () => {
    const defaultLimit = integer("default-limit", 0), enforce = $("enforce-quotas").checked;
    await api("/user-management/settings", {method: "PUT", body: {default_monthly_tokens: defaultLimit, enforce}});
    state.settings.quota = {"default-monthly-tokens": defaultLimit, enforce};
    renderSettings();
    $("settings-dialog").close();
    toast("Quota defaults saved");
  });
  onForm("create-form", async () => {
    const user = await api("/users", {method: "POST", body: {display_name: $("create-name").value.trim(), email: $("create-email").value.trim(), role: "user"}});
    state.users.push(user);
    state.total++;
    $("create-form").reset();
    $("create-dialog").close();
    $("user-search").value = "";
    renderUsers();
    await selectUser(user.id);
    toast("User created");
  });
  onForm("issue-form", async () => {
    const result = await api("/users/" + encodeURIComponent(keysUserID) + "/keys", {method: "POST", body: {label: $("key-label").value.trim()}});
    $("new-key-value").textContent = result.key;
    $("new-key-panel").hidden = false;
    $("key-label").value = "";
    await loadKeys();
  });
  $("key-login-form").addEventListener("submit", async event => {
    event.preventDefault();
    const button = event.currentTarget.querySelector("button");
    button.disabled = true;
    try { await connect($("management-key").value.trim()); }
    catch (error) { showAuth(error.message); }
    finally { button.disabled = false; }
  });
  $("change-auth").addEventListener("click", () => {
    state.selection++;
    state.activity++;
    state.key = "";
    state.user = null;
    state.users = [];
    closeInspector();
    $("user-list").replaceChildren();
    $("request-list").replaceChildren();
    showAuth();
  });
  const clearKeySecret = () => {
    $("new-key-value").textContent = "";
    $("new-key-panel").hidden = true;
    keysUserID = "";
  };
  document.querySelectorAll("[data-close]").forEach(button => button.addEventListener("click", () => {
    if (button.dataset.close === "keys-dialog") clearKeySecret();
    $(button.dataset.close).close();
  }));
  $("keys-dialog").addEventListener("close", clearKeySecret);
  $("keys-dialog").addEventListener("cancel", clearKeySecret);
  $("new-user").addEventListener("click", () => openDialog("create-dialog"));
  $("empty-create-user").addEventListener("click", () => openDialog("create-dialog"));
  $("user-search").addEventListener("input", renderUsers);
  $("refresh-users").addEventListener("click", run(() => loadUsers()));
  $("more-users").addEventListener("click", run(() => loadUsers(true)));
  $("open-quota").addEventListener("click", openQuota);
  $("quota-mode").addEventListener("change", quotaMode);
  $("open-settings").addEventListener("click", openSettings);
  $("usage-settings").addEventListener("click", openSettings);
  $("open-keys").addEventListener("click", run(openKeys));
  $("refresh-requests").addEventListener("click", run(async () => {
    if (state.user) await selectUser(state.user.id);
  }));
  $("more-requests").addEventListener("click", run(() => loadRequests(true)));
  $("status-filter").addEventListener("change", run(() => loadRequests()));
  $("time-filter").addEventListener("change", run(() => loadRequests()));
  let filterTimer;
  $("model-filter").addEventListener("input", () => {
    clearTimeout(filterTimer);
    filterTimer = setTimeout(() => { void loadRequests().catch(report); }, 250);
  });
  $("close-inspector").addEventListener("click", () => { detailSequence++; closeInspector(); renderRequests(); });
  document.addEventListener("keydown", event => { if (event.key === "Escape" && !$("inspector").hidden) { detailSequence++; closeInspector(); renderRequests(); } });
  $("conversation-tab").addEventListener("click", () => selectTab(false));
  $("raw-tab").addEventListener("click", () => selectTab(true));
  const copy = async value => {
    try { await navigator.clipboard.writeText(value); toast("Copied to clipboard"); }
    catch { toast("Copy is unavailable. Select the text and copy it manually."); }
  };
  $("copy-content").addEventListener("click", () => { void copy($("raw-content").hidden ? $("conversation").innerText : state.raw); });
  $("copy-key").addEventListener("click", () => { void copy($("new-key-value").textContent); });
  // Try a same-origin HttpOnly admin session before the panel's legacy key.
  void (async () => {
    try { await connect(""); }
    catch (error) {
      const key = storedKey();
      if (key && (error.status === 401 || error.status === 403)) {
        try { await connect(key); return; } catch (failure) { error = failure; }
      }
      showAuth(error.status === 401 ? "" : error.message);
    }
  })();
})();
