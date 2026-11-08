import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, existsSync } from 'node:fs';

const html = readFileSync(new URL('../index.html', import.meta.url), 'utf8');

test('index.html links the web app manifest and PWA meta tags', () => {
  assert.match(html, /<link rel="manifest" href="\/manifest\.webmanifest" \/>/);
  assert.match(html, /<meta name="theme-color" content="#202a25" \/>/);
  assert.match(html, /<meta name="mobile-web-app-capable" content="yes" \/>/);
  assert.match(html, /<meta name="apple-mobile-web-app-capable" content="yes" \/>/);
  assert.match(html, /<meta name="apple-mobile-web-app-status-bar-style" content="[^"]+" \/>/);
  assert.match(html, /<link rel="apple-touch-icon" href="\/apple-touch-icon\.png" \/>/);
});

test('manifest.webmanifest declares name, standalone display, and a maskable icon', () => {
  const manifestPath = new URL('../public/manifest.webmanifest', import.meta.url);
  assert.ok(existsSync(manifestPath), 'public/manifest.webmanifest is missing');
  const manifest = JSON.parse(readFileSync(manifestPath, 'utf8'));
  assert.equal(manifest.name, 'Tiamatrix');
  assert.equal(manifest.short_name, 'Tiamatrix');
  assert.equal(manifest.start_url, '/');
  assert.equal(manifest.scope, '/');
  assert.equal(manifest.display, 'standalone');
  assert.ok(manifest.theme_color);
  assert.ok(manifest.background_color);
  assert.ok(Array.isArray(manifest.icons) && manifest.icons.length >= 2);
  assert.ok(manifest.icons.some(i => i.purpose === 'maskable'), 'manifest is missing a maskable icon');
  for (const icon of manifest.icons) {
    const iconPath = new URL('../public' + icon.src, import.meta.url);
    assert.ok(existsSync(iconPath), `icon file missing: ${icon.src}`);
  }
});

test('favicon, desktop source and header use the canonical Living cell', () => {
  const canonical = readFileSync(new URL('../public/living-cell.svg', import.meta.url), 'utf8');
  assert.match(canonical, /id="living-cell"/);
  assert.match(canonical, /M1 1H5V5H1Z/);
  for (const f of ['../public/favicon.svg', '../public/brand/header-mark.svg', '../../build/appicon.svg']) {
    assert.equal(readFileSync(new URL(f, import.meta.url), 'utf8').trim(), canonical.trim(), f);
  }
  const mark = readFileSync(new URL('../src/components/brand-mark.tsx', import.meta.url), 'utf8');
  assert.match(mark, /src="\/living-cell.svg"/);
});

test('PNG icons have their advertised install sizes', () => {
  const icons = { 'icon-192.png': 192, 'icon-512.png': 512, 'icon-maskable-192.png': 192, 'icon-maskable-512.png': 512, 'apple-touch-icon.png': 180 };
  for (const [name, size] of Object.entries(icons)) {
    const png = readFileSync(new URL('../public/' + name, import.meta.url));
    assert.equal(png.readUInt32BE(16), size, name + ' width');
    assert.equal(png.readUInt32BE(20), size, name + ' height');
  }
});
