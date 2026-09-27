// ==UserScript==
// @name         Fixture bridge
// @version      1.2.3
// @connect      ${TM_FIXTURE_HOST}
// @match        *://*/*
// ==/UserScript==
const host = `${location.host}`;
const probe = "${TM_FIXTURE_PORT:-9988}";
