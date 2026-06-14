/* Xray Manager — Alpine.js front-end logic.
 *
 * Latency convention (matches the Go API):
 *   0  → untested
 *  -1  → timeout / error
 *  >0  → round-trip in milliseconds
 */

function xrayApp() {
  return {
    // --- auth / nav ---
    booted: false,
    authenticated: false,
    pw: "",
    tab: "proxies",

    // --- data ---
    proxies: [],
    subs: [],
    status: {
      running: false, activeProxyId: "", activeName: "", activeEndpoint: "",
      pid: 0, uptime: 0, binaryOk: true, warning: "", socksPort: 10808, httpPort: 10809,
    },
    statusLoadedAt: 0,
    logs: [],
    now: Date.now(),

    // --- ui state ---
    query: "",
    expandedId: null,
    modal: null,           // 'addProxy' | 'addSub' | {t:'delSub', id}
    newProxyUrl: "",
    newSubName: "",
    newSubUrl: "",
    toasts: [],
    _tid: 0,
    _logSource: null,
    _testing: false,

    // ===================================================================
    // lifecycle
    // ===================================================================
    async init() {
      this.refreshIconsSoon();
      // Re-render Lucide icons whenever the DOM changes.
      this._obs = new MutationObserver(() => this.refreshIconsSoon());
      this._obs.observe(this.$root, { childList: true, subtree: true });

      // Tick once a second for live uptime.
      setInterval(() => { this.now = Date.now(); }, 1000);

      // React to tab changes: manage the log stream.
      this.$watch("tab", (t) => {
        if (t === "status") this.openLogStream();
        else this.closeLogStream();
      });

      await this.checkAuth();
      this.booted = true;
    },

    refreshIconsSoon() {
      if (this._iconRAF) return;
      this._iconRAF = requestAnimationFrame(() => {
        this._iconRAF = null;
        if (window.lucide && window.lucide.createIcons) window.lucide.createIcons();
      });
    },

    // ===================================================================
    // API helper
    // ===================================================================
    async api(method, path, body) {
      const opts = { method, headers: {} };
      if (body !== undefined) {
        opts.headers["Content-Type"] = "application/json";
        opts.body = JSON.stringify(body);
      }
      const resp = await fetch(path, opts);
      if (resp.status === 401) {
        this.authenticated = false;
        this.closeLogStream();
        throw new Error("Session expired — please sign in again.");
      }
      let data = null;
      const text = await resp.text();
      if (text) { try { data = JSON.parse(text); } catch (_) { data = null; } }
      if (!resp.ok) {
        const msg = (data && data.error) || ("HTTP " + resp.status);
        throw new Error(msg);
      }
      return data;
    },

    // ===================================================================
    // auth
    // ===================================================================
    async checkAuth() {
      try {
        const r = await this.api("GET", "/api/auth/status");
        this.authenticated = !!(r && r.authenticated);
      } catch (_) { this.authenticated = false; }
      if (this.authenticated) await this.loadAll();
    },
    async login() {
      if (!this.pw.trim()) { this.pushToast({ tone: "warning", title: "Enter your password" }); return; }
      try {
        await this.api("POST", "/api/auth/login", { password: this.pw });
        this.authenticated = true;
        this.pw = "";
        await this.loadAll();
        if (this.tab === "status") this.openLogStream();
      } catch (e) {
        this.pushToast({ tone: "warning", title: "Couldn't sign in", message: e.message });
      }
    },
    async logout() {
      this.closeLogStream();
      try { await this.api("POST", "/api/auth/logout"); } catch (_) {}
      this.authenticated = false;
    },

    async loadAll() {
      await Promise.all([this.loadProxies(), this.loadSubs(), this.loadStatus()]);
    },
    async loadProxies() {
      try {
        const list = await this.api("GET", "/api/proxies");
        const testing = new Set(this.proxies.filter((p) => p._testing).map((p) => p.id));
        this.proxies = (list || []).map((p) => ({ ...p, _testing: testing.has(p.id) }));
      } catch (e) { this.pushToast({ tone: "warning", title: "Couldn't load proxies", message: e.message }); }
    },
    async loadSubs() {
      try {
        const list = await this.api("GET", "/api/subscriptions");
        const refreshing = new Set(this.subs.filter((s) => s._refreshing).map((s) => s.id));
        this.subs = (list || []).map((s) => ({ ...s, _refreshing: refreshing.has(s.id) }));
      } catch (e) { this.pushToast({ tone: "warning", title: "Couldn't load subscriptions", message: e.message }); }
    },
    async loadStatus() {
      try {
        this.status = await this.api("GET", "/api/xray/status");
        this.statusLoadedAt = Date.now();
      } catch (_) {}
    },

    // ===================================================================
    // derived values
    // ===================================================================
    get activeId() { return this.status.activeProxyId; },
    get filteredProxies() {
      const q = this.query.trim().toLowerCase();
      if (!q) return this.proxies;
      return this.proxies.filter((p) =>
        (p.name + " " + p.address + " " + p.protocol).toLowerCase().includes(q));
    },
    get countLabel() {
      const active = this.activeId ? 1 : 0;
      return this.proxies.length + " proxies · " + active + " active";
    },
    get runLabel() { return this.status.running ? "Running" : "Stopped"; },
    get processSubText() {
      if (!this.status.running) {
        return this.status.binaryOk ? "The core is not running." : (this.status.warning || "xray binary not found.");
      }
      const secs = this.status.uptime + Math.max(0, (this.now - this.statusLoadedAt) / 1000);
      return "Active for " + this.fmtUptime(secs);
    },
    get activeProxy() { return this.proxies.find((p) => p.id === this.activeId) || null; },
    get logsText() { return this.logs.join("\n"); },

    subName(id) { const s = this.subs.find((x) => x.id === id); return s ? s.name : null; },
    proxySource(p) { return p.subscriptionId ? (this.subName(p.subscriptionId) || "Subscription") : "Added manually"; },
    endpoint(p) { return p.address + ":" + p.port; },

    latInfo(p) {
      if (p.latency === 0 || p.latency === null || p.latency === undefined)
        return { text: "untested", cls: "lat-untested" };
      if (p.latency < 0) return { text: "timeout", cls: "lat-bad" };
      const n = p.latency;
      let cls = "lat-good";
      if (n >= 280) cls = "lat-bad";
      else if (n >= 120) cls = "lat-mid";
      return { text: n + " ms", cls };
    },

    // ===================================================================
    // formatting helpers
    // ===================================================================
    fmtUptime(secs) {
      const s = Math.max(0, Math.floor(secs));
      const h = Math.floor(s / 3600);
      const m = Math.floor((s % 3600) / 60);
      const sec = s % 60;
      if (h > 0) return h + "h " + m + "m";
      if (m > 0) return m + "m " + String(sec).padStart(2, "0") + "s";
      return sec + "s";
    },
    relTime(iso) {
      if (!iso) return "never";
      const t = new Date(iso).getTime();
      if (isNaN(t) || new Date(iso).getFullYear() < 2000) return "never";
      const mins = (this.now - t) / 60000;
      if (mins < 1) return "just now";
      if (mins < 60) return Math.round(mins) + "m ago";
      const hrs = mins / 60;
      if (hrs < 24) return Math.round(hrs) + "h ago";
      const days = hrs / 24;
      if (days < 2) return "yesterday";
      return Math.round(days) + "d ago";
    },
    subMeta(s) {
      const lead = s.error ? "Last tried " : "Refreshed ";
      return lead + this.relTime(s.lastFetch) + " · " + (s.count || 0) + " proxies";
    },

    // ===================================================================
    // proxy actions
    // ===================================================================
    toggleExpand(id) { this.expandedId = this.expandedId === id ? null : id; },

    async activate(id) {
      const p = this.proxies.find((x) => x.id === id);
      const prev = this.status.activeProxyId;
      this.status.activeProxyId = id; // optimistic
      this.expandedId = null;
      try {
        const r = await this.api("POST", "/api/xray/activate/" + id);
        if (r && r.status) { this.status = r.status; this.statusLoadedAt = Date.now(); }
        this.pushToast({ tone: "success", title: "Now using " + (p ? p.name : "proxy") });
      } catch (e) {
        this.status.activeProxyId = prev;
        this.pushToast({ tone: "warning", title: "Couldn't activate", message: e.message });
      }
    },

    async testOne(id) {
      const p = this.proxies.find((x) => x.id === id);
      if (p) p._testing = true;
      try {
        const r = await this.api("POST", "/api/health/test/" + id);
        if (p) { p._testing = false; p.latency = r.latency; }
        if (r.latency < 0) {
          this.pushToast({ tone: "warning", title: "Test failed", message: (p ? p.name : "Proxy") + " timed out" });
        }
      } catch (e) {
        if (p) p._testing = false;
        this.pushToast({ tone: "warning", title: "Test failed", message: e.message });
      }
    },

    async testAll() {
      if (this._testing) return;
      this._testing = true;
      this.proxies.forEach((p) => { p._testing = true; });
      this.pushToast({ tone: "default", title: "Testing all proxies", message: this.proxies.length + " proxies queued" });
      try {
        const resp = await fetch("/api/health/test-all");
        if (resp.status === 409) {
          this.proxies.forEach((p) => { p._testing = false; });
          this._testing = false;
          this.pushToast({ tone: "warning", title: "Already testing", message: "A test run is in progress." });
          return;
        }
        if (!resp.ok || !resp.body) throw new Error("HTTP " + resp.status);
        await this.readSSE(resp, (data) => {
          if (!data || !data.proxyId) return;
          const p = this.proxies.find((x) => x.id === data.proxyId);
          if (p) { p._testing = false; p.latency = data.latency; }
        });
      } catch (e) {
        this.pushToast({ tone: "warning", title: "Test run failed", message: e.message });
      } finally {
        this.proxies.forEach((p) => { p._testing = false; });
        this._testing = false;
      }
    },

    async deleteProxy(id) {
      const p = this.proxies.find((x) => x.id === id);
      const snapshot = this.proxies;
      this.proxies = this.proxies.filter((x) => x.id !== id); // optimistic
      if (this.expandedId === id) this.expandedId = null;
      try {
        await this.api("DELETE", "/api/proxies/" + id);
        if (this.status.activeProxyId === id) this.status.activeProxyId = "";
        this.pushToast({ tone: "default", title: "Removed " + (p ? p.name : "proxy") });
      } catch (e) {
        this.proxies = snapshot;
        this.pushToast({ tone: "warning", title: "Couldn't delete", message: e.message });
      }
    },

    openAddProxy() { this.modal = "addProxy"; this.newProxyUrl = ""; },
    async confirmAddProxy() {
      const url = this.newProxyUrl.trim();
      if (!url) { this.pushToast({ tone: "warning", title: "Paste a proxy link" }); return; }
      try {
        const p = await this.api("POST", "/api/proxies", { raw_url: url });
        this.proxies = [{ ...p, _testing: false }, ...this.proxies];
        this.modal = null; this.newProxyUrl = "";
        this.pushToast({ tone: "success", title: "Proxy added", message: "Tap it to test the connection." });
      } catch (e) {
        this.pushToast({ tone: "warning", title: "Unrecognized link", message: e.message });
      }
    },

    // ===================================================================
    // subscription actions
    // ===================================================================
    async refreshSub(id) {
      const sub = this.subs.find((x) => x.id === id);
      if (!sub || sub._refreshing) return;
      sub._refreshing = true;
      try {
        const r = await this.api("POST", "/api/subscriptions/" + id + "/refresh");
        sub._refreshing = false;
        sub.error = ""; sub.count = r.count; sub.lastFetch = new Date().toISOString();
        await this.loadProxies();
        this.pushToast({ tone: "success", title: "Refreshed " + sub.name, message: r.count + " proxies available." });
      } catch (e) {
        sub._refreshing = false;
        sub.error = e.message; sub.lastFetch = new Date().toISOString();
        this.pushToast({ tone: "warning", title: "Refresh failed", message: sub.name + " could not be reached." });
      }
    },
    refreshAllSubs() {
      this.pushToast({ tone: "default", title: "Refreshing subscriptions" });
      this.subs.forEach((s, i) => setTimeout(() => this.refreshSub(s.id), i * 250));
    },
    openAddSub() { this.modal = "addSub"; this.newSubName = ""; this.newSubUrl = ""; },
    async confirmAddSub() {
      const name = this.newSubName.trim();
      const url = this.newSubUrl.trim();
      if (!name || !url) { this.pushToast({ tone: "warning", title: "Name and URL required" }); return; }
      try {
        const sub = await this.api("POST", "/api/subscriptions", { name, url });
        this.subs = [...this.subs, { ...sub, _refreshing: false }];
        this.modal = null; this.newSubName = ""; this.newSubUrl = "";
        this.pushToast({ tone: "success", title: "Subscription added", message: "Pulling proxies…" });
        setTimeout(() => this.refreshSub(sub.id), 300);
      } catch (e) {
        this.pushToast({ tone: "warning", title: "Couldn't add", message: e.message });
      }
    },
    askDeleteSub(id) { this.modal = { t: "delSub", id }; },
    get delSub() {
      if (this.modal && this.modal.t === "delSub") return this.subs.find((x) => x.id === this.modal.id) || null;
      return null;
    },
    async deleteSub(keepProxies) {
      const id = this.modal.id;
      const sub = this.subs.find((x) => x.id === id);
      this.modal = null;
      try {
        await this.api("DELETE", "/api/subscriptions/" + id + "?keepProxies=" + (keepProxies ? "true" : "false"));
        this.subs = this.subs.filter((x) => x.id !== id);
        if (!keepProxies) await this.loadProxies();
        await this.loadStatus();
        this.pushToast({
          tone: "default", title: "Deleted " + (sub ? sub.name : "subscription"),
          message: keepProxies ? "Its proxies were kept." : "Its proxies were removed.",
        });
      } catch (e) {
        this.pushToast({ tone: "warning", title: "Couldn't delete", message: e.message });
      }
    },

    // ===================================================================
    // xray process actions
    // ===================================================================
    async startXray() { await this.procAction("start", "Xray started"); },
    async stopXray() { await this.procAction("stop", "Xray stopped"); },
    async restartXray() { await this.procAction("restart", "Restarting Xray"); },
    async procAction(action, okTitle) {
      try {
        const r = await this.api("POST", "/api/xray/" + action);
        if (r && r.status) { this.status = r.status; this.statusLoadedAt = Date.now(); }
        this.pushToast({ tone: action === "stop" ? "default" : "success", title: okTitle });
      } catch (e) {
        this.pushToast({ tone: "warning", title: "Couldn't " + action + " xray", message: e.message });
      }
    },
    async copyPort(val) {
      try { if (navigator.clipboard) await navigator.clipboard.writeText(String(val)); } catch (_) {}
      this.pushToast({ tone: "success", title: "Copied " + val });
    },
    clearLogs() { this.logs = []; },

    // ===================================================================
    // SSE: logs + test-all
    // ===================================================================
    openLogStream() {
      if (this._logSource || !this.authenticated) return;
      const es = new EventSource("/api/xray/logs");
      es.onmessage = (e) => {
        try {
          const line = JSON.parse(e.data);
          this.logs.push(line);
          if (this.logs.length > 300) this.logs.splice(0, this.logs.length - 300);
          this.$nextTick(() => this.scrollLog());
        } catch (_) {}
      };
      es.onerror = () => { /* EventSource auto-reconnects */ };
      this._logSource = es;
    },
    closeLogStream() {
      if (this._logSource) { this._logSource.close(); this._logSource = null; }
    },
    scrollLog() {
      const el = this.$refs.logFeed;
      if (el) el.scrollTop = el.scrollHeight;
    },
    async readSSE(resp, onData) {
      const reader = resp.body.getReader();
      const decoder = new TextDecoder();
      let buf = "";
      while (true) {
        const { value, done } = await reader.read();
        if (done) break;
        buf += decoder.decode(value, { stream: true });
        let idx;
        while ((idx = buf.indexOf("\n\n")) >= 0) {
          const chunk = buf.slice(0, idx);
          buf = buf.slice(idx + 2);
          for (const line of chunk.split("\n")) {
            if (line.startsWith("data: ")) {
              try { onData(JSON.parse(line.slice(6))); } catch (_) {}
            }
          }
        }
      }
    },

    // ===================================================================
    // toasts
    // ===================================================================
    pushToast(t) {
      const id = ++this._tid;
      this.toasts.push({ ...t, id });
      setTimeout(() => this.dismissToast(id), 3600);
    },
    dismissToast(id) { this.toasts = this.toasts.filter((t) => t.id !== id); },
    toastIcon(tone) {
      return { default: "sparkles", success: "check-circle-2", warning: "alert-triangle", danger: "alert-octagon", info: "info" }[tone] || "sparkles";
    },

    closeModal() { this.modal = null; },
  };
}
