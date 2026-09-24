#!/usr/bin/env node
// Pixel-diff two snapshot directories produced by snap.js.
//
//   node visual/compare.js <baselineDir> <candidateDir> [diffDir] [maxDiffPixels]
//
// Prints one line per image with the number of differing pixels and writes a
// highlighted diff PNG per image into <diffDir>. Exits non-zero when any image
// differs by more than maxDiffPixels (default 0) or is missing / sized differently.

const fs = require('fs');
const path = require('path');
const { PNG } = require('pngjs');
const pixelmatch = require('pixelmatch');

const [baseDir, candDir, diffDirArg, maxArg] = process.argv.slice(2);
if (!baseDir || !candDir) {
  console.error('usage: node visual/compare.js <baselineDir> <candidateDir> [diffDir] [maxDiffPixels]');
  process.exit(2);
}
const diffDir = diffDirArg || path.join(candDir, '..', path.basename(candDir) + '-diff');
const maxDiff = Number(maxArg || 0);
fs.mkdirSync(diffDir, { recursive: true });

const names = fs.readdirSync(baseDir).filter(f => f.endsWith('.png')).sort();
let bad = 0;
const pad = Math.max(...names.map(n => n.length));
for (const name of names) {
  const candPath = path.join(candDir, name);
  if (!fs.existsSync(candPath)) { console.log(`${name.padEnd(pad)}  MISSING in candidate`); bad++; continue; }
  const a = PNG.sync.read(fs.readFileSync(path.join(baseDir, name)));
  const b = PNG.sync.read(fs.readFileSync(candPath));
  if (a.width !== b.width || a.height !== b.height) {
    console.log(`${name.padEnd(pad)}  SIZE MISMATCH ${a.width}x${a.height} vs ${b.width}x${b.height}`); bad++; continue;
  }
  const diff = new PNG({ width: a.width, height: a.height });
  const n = pixelmatch(a.data, b.data, diff.data, a.width, a.height, { threshold: 0.1, includeAA: false });
  const pct = ((n / (a.width * a.height)) * 100).toFixed(4);
  if (n > maxDiff) { bad++; fs.writeFileSync(path.join(diffDir, name), PNG.sync.write(diff)); }
  console.log(`${name.padEnd(pad)}  ${String(n).padStart(7)} px  (${pct}%)${n > maxDiff ? '  <-- DIFF' : ''}`);
}
const extra = fs.readdirSync(candDir).filter(f => f.endsWith('.png') && !names.includes(f));
for (const name of extra) console.log(`${name.padEnd(pad)}  EXTRA in candidate`);
console.log(`\n${names.length} images compared, ${bad} with differences${bad ? ` (diff PNGs in ${diffDir})` : ''}`);
process.exit(bad ? 1 : 0);
