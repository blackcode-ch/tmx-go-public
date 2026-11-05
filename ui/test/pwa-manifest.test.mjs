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

// The owner, 3 Nov: the grid logo is out; the Pocket dragon (Tiamat) is the mark everywhere: the
// header, the favicon and the app icons, the exact assets of the Python dashboard's Tiamatrix design.
test('the favicon, the app icon source and the header mark are the Pocket dragon', () => {
  const dragon = 'M12 12L20 12L20 20L28 20L28 12L36 12L36 20L44 20L44 12L52 12L52 28L60 28L60 36L52 36L52 44L36 44L36 52L44 52L44 60L20 60L20 52L12 52L12 44L4 44L4 28L12 28L12 36L20 36L20 44L28 44L28 36L20 36L20 28L12 28Z';
  for (const f of ['../public/favicon.svg', '../public/brand/header-mark.svg', '../../build/appicon.svg']) {
    const svg = readFileSync(new URL(f, import.meta.url), 'utf8');
    assert.ok(svg.includes(dragon), `${f} is not the Pocket dragon`);
    assert.ok(!svg.includes('M1 1H5V5H1Z'), `${f} still has the grid (living cell)`);
  }
});
