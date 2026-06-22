// ==UserScript==
// @name         admin-project-tool log bridge
// @namespace    https://github.com/McBrideMusings/admin-project-tool
// @version      0.2.0
// @description  Ships console.log/info/warn/error/debug from the browser to a locally-running admin-project-tool listener, so client and server logs interleave in one file. localhost/127.0.0.1 is always watched; any other host is opt-in via a single Tampermonkey menu command. Forwards only when a matching listener is actually running.
// @match        *://*/*
// @run-at       document-start
// @grant        GM.xmlHttpRequest
// @grant        GM_getValue
// @grant        GM_setValue
// @grant        GM_registerMenuCommand
// @grant        unsafeWindow
// @connect      127.0.0.1
// @connect      100.64.0.11
// @connect      100.64.0.10
// ==/UserScript==

// ----------------------------------------------------------------------------
// How this stays project-agnostic (one script for every project, no baked URLs):
//
//   - WHERE it can run: `@match *://*/*` covers every page, including any prod
//     webview iframe. No per-project @match line.
//   - WHERE it sends: GM.xmlHttpRequest only ever talks to the LISTENER (your Mac
//     / localhost), never the prod site — so `@connect` is your stable machines,
//     not per-project prod URLs.
//   - WHICH pages to forward: decided at runtime, never hardcoded:
//       • localhost / 127.0.0.1 / 0.0.0.0 → always a candidate (no setup).
//       • any other host → only if it matches a glob you saved via the menu
//         command "Log bridge: watch <host>". Saved in Tampermonkey storage
//         (GM_setValue), not in this file.
//   - WHETHER to actually forward: even on a candidate page, it only forwards if
//     a listener is running AND its advertised [log_bridge].hosts (from that
//     project's admin.toml, served at /health) match this hostname. So the prod
//     hostname authority lives in each project's admin.toml — never here.
//
// Net: localhost works with zero setup; a prod app needs ONE menu click the first
// time you visit it, ever. Nothing project-specific is in this script.
// ----------------------------------------------------------------------------

(function () {
  "use strict";

  // ----- Edit these to suit your tailnet. -----

  // Hosts to probe for a running admin-project-tool listener. localhost first;
  // then this machine's Tailscale IPs (the browser runs on the Mac).
  const PROBE_HOSTS = [
    "127.0.0.1",
    "100.64.0.11",   // primary dev machine (tailnet)
    "100.64.0.10", // second machine (tailnet)
  ];

  // Must cover every machine's [log_bridge] port_range (default 9988-9999).
  const PROBE_PORTS = [];
  for (let p = 9988; p <= 9999; p++) PROBE_PORTS.push(p);

  // localhost variants are always watched — no registration needed.
  const LOCAL_HOST_RES = [/^localhost$/, /^127\.0\.0\.1$/, /^0\.0\.0\.0$/];

  // Tampermonkey-storage key holding the user's opt-in host globs (JSON array).
  const STORE_KEY = "watchedHostGlobs";

  const BATCH_INTERVAL_MS = 1000;

  // ----- host matching -----

  // Convert a single-label DNS glob ("myapp-*-web.example.net") to a regex.
  // "*" matches one DNS label segment (no dots).
  function globToRegex(glob) {
    const escaped = glob.replace(/[.+?^${}()|[\]\\]/g, "\\$&").replace(/\*/g, "[^.]+");
    return new RegExp("^" + escaped + "$");
  }

  function hostMatches(hostname, patterns) {
    return patterns.some((p) => globToRegex(p).test(hostname));
  }

  function isLocalHost(hostname) {
    return LOCAL_HOST_RES.some((re) => re.test(hostname));
  }

  function getWatched() {
    try {
      const v = JSON.parse(GM_getValue(STORE_KEY, "[]"));
      return Array.isArray(v) ? v : [];
    } catch {
      return [];
    }
  }

  function setWatched(arr) {
    GM_setValue(STORE_KEY, JSON.stringify(arr));
  }

  // Which saved glob (if any) currently matches this host.
  function matchingWatchedGlob(hostname) {
    return getWatched().find((g) => {
      try {
        return globToRegex(g).test(hostname);
      } catch {
        return false;
      }
    });
  }

  // Auto-derive a durable glob from a concrete host: within the first DNS label,
  // wildcard any "-"-joined segment that contains a digit (version ids, subreddit
  // ids, build numbers), then collapse adjacent wildcards. Leaves digit-free hosts
  // exact. e.g. myapp-ab12cd-0-0-87-web.example.net
  //          →  myapp-*-web.example.net
  function suggestGlob(hostname) {
    const labels = hostname.split(".");
    const wild = labels[0].split("-").map((s) => (/\d/.test(s) ? "*" : s));
    // Only wildcard if at least one literal segment survives. Otherwise a label
    // like "2024" would collapse to a bare "*", matching every sibling subdomain
    // — keep the label exact in that case.
    if (wild.some((s) => s !== "*")) {
      const collapsed = [];
      for (const s of wild) {
        if (s === "*" && collapsed[collapsed.length - 1] === "*") continue;
        collapsed.push(s);
      }
      labels[0] = collapsed.join("-");
    }
    return labels.join(".");
  }

  // ----- on-page toast (modal-free; works inside sandboxed iframes) -----

  function toast(text) {
    const show = () => {
      if (!document.body) return;
      const el = document.createElement("div");
      el.textContent = text;
      el.style.cssText = [
        "position:fixed",
        "left:50%",
        "bottom:16px",
        "transform:translateX(-50%)",
        "max-width:90vw",
        "padding:8px 14px",
        "background:#1b1b2e",
        "color:#e6e6f0",
        "font:13px/1.4 system-ui,sans-serif",
        "border:1px solid #4caf50",
        "border-radius:8px",
        "z-index:2147483647",
        "pointer-events:none",
        "box-shadow:0 4px 16px rgba(0,0,0,0.4)",
      ].join(";");
      document.body.appendChild(el);
      setTimeout(() => el.remove(), 4000);
    };
    if (document.body) show();
    else document.addEventListener("DOMContentLoaded", show, { once: true });
  }

  // ----- menu command (registered on EVERY page so you can watch a new host) -----

  (function registerMenu() {
    const host = location.hostname;
    if (isLocalHost(host)) {
      GM_registerMenuCommand(`Log bridge: ${host} is always watched (local)`, () => {
        toast("Log bridge: localhost is always watched — no action needed.");
      });
      return;
    }
    const existing = matchingWatchedGlob(host);
    if (existing) {
      GM_registerMenuCommand(`Log bridge: unwatch ${host}`, () => {
        setWatched(getWatched().filter((g) => g !== existing));
        toast(`Log bridge: stopped watching "${existing}". Reload to apply.`);
      });
    } else {
      GM_registerMenuCommand(`Log bridge: watch ${host}`, () => {
        const glob = suggestGlob(host);
        const watched = getWatched();
        if (!watched.includes(glob)) {
          watched.push(glob);
          setWatched(watched);
        }
        toast(`Log bridge: now watching "${glob}". Reload — a listener must be running to forward.`);
      });
    }
  })();

  // ----- candidacy gate: no probe / no console patch / no dot otherwise -----

  const HOST = location.hostname;
  const isCandidate = isLocalHost(HOST) || !!matchingWatchedGlob(HOST);
  if (!isCandidate) return;

  // ----- forwarding implementation -----

  function gmRequest(opts) {
    return new Promise((resolve) => {
      GM.xmlHttpRequest({
        ...opts,
        onload: (r) => resolve({ ok: r.status >= 200 && r.status < 300, status: r.status, body: r.responseText }),
        onerror: () => resolve(null),
        ontimeout: () => resolve(null),
        timeout: 500,
      });
    });
  }

  // Probe hosts × ports in parallel and resolve as soon as one listener's
  // advertised hosts (from that project's admin.toml, served at /health) match
  // this page — the final gate that keeps prod URLs out of the script. Short-
  // circuits on first match so it returns immediately instead of waiting for
  // every dead host:port to time out. First matching responder wins (if you run
  // two listeners for the same host, the newest-started one isn't guaranteed).
  function findTarget() {
    const endpoints = [];
    for (const host of PROBE_HOSTS) for (const port of PROBE_PORTS) endpoints.push({ host, port });
    return new Promise((resolve) => {
      if (endpoints.length === 0) return resolve(null);
      let pending = endpoints.length;
      let settled = false;
      const finish = (val) => { if (!settled) { settled = true; resolve(val); } };
      for (const { host, port } of endpoints) {
        gmRequest({ method: "GET", url: `http://${host}:${port}/health` }).then((r) => {
          if (!settled && r && r.ok) {
            try {
              const data = JSON.parse(r.body);
              if (hostMatches(location.hostname, data.hosts || [])) {
                return finish({ host, port, ...data });
              }
            } catch {
              // ignore unparseable /health
            }
          }
          if (--pending === 0) finish(null);
        });
      }
    });
  }

  function makeIndicator() {
    const dot = document.createElement("div");
    dot.style.cssText = [
      "position:fixed",
      "right:6px",
      "bottom:6px",
      "width:8px",
      "height:8px",
      "border-radius:50%",
      "background:#888",
      "z-index:2147483647",
      "pointer-events:none",
      "transition:background 200ms",
      "opacity:0.7",
    ].join(";");
    dot.title = "admin-project-tool log bridge: searching…";
    const attach = () => {
      if (document.body) document.body.appendChild(dot);
      else document.addEventListener("DOMContentLoaded", () => document.body.appendChild(dot), { once: true });
    };
    attach();
    return dot;
  }

  function patchConsole(target, buffer) {
    const methods = ["log", "info", "warn", "error", "debug"];
    for (const m of methods) {
      const orig = target[m].bind(target);
      target[m] = (...args) => {
        orig(...args);
        try {
          const message = args
            .map((a) => {
              if (typeof a === "string") return a;
              try { return JSON.stringify(a); } catch { return String(a); }
            })
            .join(" ");
          buffer.push({
            timestamp: new Date().toISOString(),
            level: m,
            message,
            host: location.hostname, // host only — never the full URL (avoids leaking auth tokens in the query string)
          });
        } catch {
          // Never let log capture break the page.
        }
      };
    }
  }

  function startFlusher(target, buffer) {
    const flush = () => {
      if (buffer.length === 0) return;
      const batch = buffer.splice(0, buffer.length);
      gmRequest({
        method: "POST",
        url: `http://${target.host}:${target.port}/log`,
        data: JSON.stringify(batch),
        headers: { "Content-Type": "application/json" },
      });
    };
    const id = setInterval(flush, BATCH_INTERVAL_MS);
    // Stop the timer (and flush what's left) when the page goes away — otherwise
    // it keeps firing against a stale target for the page's lifetime.
    window.addEventListener("pagehide", () => { clearInterval(id); flush(); }, { once: true });
  }

  (async () => {
    const dot = makeIndicator();
    const buffer = [];
    patchConsole(unsafeWindow.console, buffer); // Patch the PAGE's console, not the sandbox's.
    const target = await findTarget();
    if (!target) {
      dot.title = "admin-project-tool: no matching listener running"; // dot stays grey (#888 from creation)
      buffer.length = 0; // discard — nobody is listening
      return;
    }
    dot.style.background = "#4caf50";
    dot.title = `admin-project-tool: ${target.host}:${target.port} (${target.project || "?"})`;
    console.log(`[log-bridge] connected to ${target.host}:${target.port} (${target.project || "?"}) from ${location.hostname}`);
    startFlusher(target, buffer);
  })();
})();
