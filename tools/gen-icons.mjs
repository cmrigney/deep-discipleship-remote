#!/usr/bin/env node
// Renders the app icon (green rounded square with a white play triangle, same
// geometry as relay/web/static/icon.svg) to PNGs with no dependencies.
// Usage: node tools/gen-icons.mjs
import { deflateSync } from 'node:zlib';
import { writeFileSync, mkdirSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const targets = [
  ...[16, 32, 48, 96, 128].map((s) => [s, `extension/src/icons/icon-${s}.png`]),
  ...[180, 192, 512].map((s) => [s, `relay/web/static/icon-${s}.png`]),
];

const BG = [0x16, 0xa3, 0x4a];
const FG = [0xff, 0xff, 0xff];
const SS = 4; // supersampling per axis

// Geometry in a 100x100 box.
const RADIUS = 22;
const TRI = [[38, 28], [74, 50], [38, 72]];
const STROKE = 3; // half of the SVG stroke-width, rounds the triangle

function inRoundedSquare(x, y) {
  const cx = Math.min(Math.max(x, RADIUS), 100 - RADIUS);
  const cy = Math.min(Math.max(y, RADIUS), 100 - RADIUS);
  return (x - cx) ** 2 + (y - cy) ** 2 <= RADIUS ** 2 && x >= 0 && y >= 0 && x <= 100 && y <= 100;
}

function distToSegment(px, py, [ax, ay], [bx, by]) {
  const dx = bx - ax, dy = by - ay;
  const t = Math.max(0, Math.min(1, ((px - ax) * dx + (py - ay) * dy) / (dx * dx + dy * dy)));
  return Math.hypot(px - (ax + t * dx), py - (ay + t * dy));
}

function inTriangle(x, y) {
  const sign = (p1, p2) => (x - p2[0]) * (p1[1] - p2[1]) - (p1[0] - p2[0]) * (y - p2[1]);
  const d1 = sign(TRI[0], TRI[1]), d2 = sign(TRI[1], TRI[2]), d3 = sign(TRI[2], TRI[0]);
  const inside = !((d1 < 0 || d2 < 0 || d3 < 0) && (d1 > 0 || d2 > 0 || d3 > 0));
  if (inside) return true;
  return Math.min(
    distToSegment(x, y, TRI[0], TRI[1]),
    distToSegment(x, y, TRI[1], TRI[2]),
    distToSegment(x, y, TRI[2], TRI[0]),
  ) <= STROKE;
}

function render(size) {
  const px = Buffer.alloc(size * size * 4);
  for (let y = 0; y < size; y++) {
    for (let x = 0; x < size; x++) {
      let bg = 0, fg = 0;
      for (let sy = 0; sy < SS; sy++) {
        for (let sx = 0; sx < SS; sx++) {
          const ux = ((x + (sx + 0.5) / SS) / size) * 100;
          const uy = ((y + (sy + 0.5) / SS) / size) * 100;
          if (!inRoundedSquare(ux, uy)) continue;
          if (inTriangle(ux, uy)) fg++; else bg++;
        }
      }
      const cover = bg + fg;
      const i = (y * size + x) * 4;
      if (cover === 0) continue;
      for (let c = 0; c < 3; c++) px[i + c] = Math.round((BG[c] * bg + FG[c] * fg) / cover);
      px[i + 3] = Math.round((cover / (SS * SS)) * 255);
    }
  }
  return encodePNG(size, size, px);
}

const CRC_TABLE = Array.from({ length: 256 }, (_, n) => {
  let c = n;
  for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
  return c >>> 0;
});
function crc32(buf) {
  let c = 0xffffffff;
  for (const b of buf) c = CRC_TABLE[(c ^ b) & 0xff] ^ (c >>> 8);
  return (c ^ 0xffffffff) >>> 0;
}
function chunk(type, data) {
  const len = Buffer.alloc(4);
  len.writeUInt32BE(data.length);
  const td = Buffer.concat([Buffer.from(type, 'ascii'), data]);
  const crc = Buffer.alloc(4);
  crc.writeUInt32BE(crc32(td));
  return Buffer.concat([len, td, crc]);
}
function encodePNG(w, h, rgba) {
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(w, 0);
  ihdr.writeUInt32BE(h, 4);
  ihdr[8] = 8;  // bit depth
  ihdr[9] = 6;  // RGBA
  const raw = Buffer.alloc((w * 4 + 1) * h);
  for (let y = 0; y < h; y++) {
    raw[y * (w * 4 + 1)] = 0; // filter: none
    rgba.copy(raw, y * (w * 4 + 1) + 1, y * w * 4, (y + 1) * w * 4);
  }
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk('IHDR', ihdr),
    chunk('IDAT', deflateSync(raw, { level: 9 })),
    chunk('IEND', Buffer.alloc(0)),
  ]);
}

for (const [size, rel] of targets) {
  const out = join(root, rel);
  mkdirSync(dirname(out), { recursive: true });
  writeFileSync(out, render(size));
  console.log('wrote', rel);
}
