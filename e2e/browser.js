// Resolves the Chromium executable Playwright should launch.
//
// Playwright normally downloads the exact Chromium build it was released with.
// In sandboxed CI images a different build is often preinstalled under
// PLAYWRIGHT_BROWSERS_PATH and network installs are not allowed. When the
// build this Playwright version expects is missing, fall back to the newest
// preinstalled `chromium-*` build (or PW_CHROMIUM_EXECUTABLE when set).
// Returns undefined when Playwright's own build is present, so the default
// behaviour is unchanged on developer machines.

const fs = require('fs');
const os = require('os');
const path = require('path');

function resolveChromiumExecutable() {
  if (process.env.PW_CHROMIUM_EXECUTABLE) return process.env.PW_CHROMIUM_EXECUTABLE;
  try {
    const expected = require('playwright-core').chromium.executablePath();
    if (expected && fs.existsSync(expected)) return undefined;
  } catch (_) {}
  const root = process.env.PLAYWRIGHT_BROWSERS_PATH || path.join(os.homedir(), '.cache', 'ms-playwright');
  let dirs = [];
  try { dirs = fs.readdirSync(root); } catch (_) { return undefined; }
  const builds = dirs
    .map(d => /^chromium-(\d+)$/.exec(d))
    .filter(Boolean)
    .sort((a, b) => Number(b[1]) - Number(a[1]));
  for (const m of builds) {
    for (const rel of ['chrome-linux/chrome', 'chrome-linux64/chrome', 'chrome-mac/Chromium.app/Contents/MacOS/Chromium', 'chrome-win/chrome.exe']) {
      const candidate = path.join(root, m[0], rel);
      if (fs.existsSync(candidate)) return candidate;
    }
  }
  return undefined;
}

module.exports = { resolveChromiumExecutable };
