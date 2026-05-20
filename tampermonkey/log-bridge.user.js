// ==UserScript==
// @name         admin-project-tool log bridge
// @namespace    https://github.com/McBrideMusings/admin-project-tool
// @version      0.1.0
// @description  Ships console.log/info/warn/error/debug from the browser to a locally-running admin-project-tool process, so client and server logs interleave in one file.
// @match        *://*/*
// @run-at       document-start
// @grant        GM.xmlHttpRequest
// @grant        unsafeWindow
// @connect      127.0.0.1
// @connect      100.64.0.11
// @connect      100.64.0.10
// ==/UserScript==

(function () {
  "use strict";

  // ----- Hardcoded for v1. Edit these two arrays to suit your tailnet. -----

  // Hosts to probe for a running admin-project-tool. The first is always
  // localhost. Remaining entries are Tailscale peer IPs reachable from the
  // browser's machine.
  const PROBE_HOSTS = [
    "127.0.0.1",
    "100.64.0.11",   // Pierce's Mac
    "100.64.0.10", // example-host
  ];

  // Union of all port ranges across your machines. Each machine's admin.toml
  // can set [log_bridge] port_range = [start, end] — add any custom ranges here.
  const PROBE_PORTS = [];
  for (let p = 9988; p <= 9999; p++) PROBE_PORTS.push(p); // default range

  // Coarse prefilter — never probe anything unless the current page's
  // hostname matches one of these patterns. Cheap and prevents probing on
  // every random tab you open.
  const HOSTNAME_PREFILTER = [
    /^localhost$/,
    /^127\.0\.0\.1$/,
    /^0\.0\.0\.0$/,
    /\.dev\.example-host\.com$/,
    /\.mac\.example-host\.com$/,
  ];

  // ----- Implementation -----

  if (!HOSTNAME_PREFILTER.some((re) => re.test(location.hostname))) return;

  const BATCH_INTERVAL_MS = 1000;

  // Convert a single-label DNS glob ("*.dev.example.internal") to a regex.
  function globToRegex(glob) {
    const escaped = glob.replace(/[.+?^${}()|[\]\\]/g, "\\$&").replace(/\*/g, "[^.]+");
    return new RegExp("^" + escaped + "$");
  }

  function hostMatches(hostname, patterns) {
    return patterns.some((p) => globToRegex(p).test(hostname));
  }

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

  async function probeAll() {
    const probes = [];
    for (const host of PROBE_HOSTS) {
      for (const port of PROBE_PORTS) {
        probes.push(
          gmRequest({ method: "GET", url: `http://${host}:${port}/health` }).then((r) => {
            if (!r || !r.ok) return null;
            try {
              const data = JSON.parse(r.body);
              return { host, port, ...data };
            } catch {
              return null;
            }
          })
        );
      }
    }
    const results = (await Promise.all(probes)).filter(Boolean);
    return results.filter((r) => hostMatches(location.hostname, r.hosts || []));
  }

  function pickTarget(matches) {
    if (matches.length === 0) return null;
    return matches.slice().sort((a, b) => (b.started_at || "").localeCompare(a.started_at || ""))[0];
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
            url: location.href,
          });
        } catch {
          // Never let log capture break the page.
        }
      };
    }
  }

  function startFlusher(target, buffer) {
    setInterval(() => {
      if (buffer.length === 0) return;
      const batch = buffer.splice(0, buffer.length);
      gmRequest({
        method: "POST",
        url: `http://${target.host}:${target.port}/log`,
        data: JSON.stringify(batch),
        headers: { "Content-Type": "application/json" },
      });
    }, BATCH_INTERVAL_MS);
  }

  (async () => {
    const dot = makeIndicator();
    const buffer = [];
    patchConsole(unsafeWindow.console, buffer); // Patch the PAGE's console, not the sandbox's.
    const matches = await probeAll();
    const target = pickTarget(matches);
    if (!target) {
      dot.style.background = "#888";
      dot.title = "admin-project-tool: no matching admin process found";
      buffer.length = 0; // discard — nobody is listening
      return;
    }
    dot.style.background = "#4caf50";
    dot.title = `admin-project-tool: ${target.host}:${target.port} (${target.project || "?"})`;
    console.log(`[log-bridge] connected to ${target.host}:${target.port} (${target.project || "?"}) from ${location.hostname}`);
    startFlusher(target, buffer);
  })();
})();
