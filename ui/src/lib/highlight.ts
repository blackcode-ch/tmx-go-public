// Syntax highlighting with shiki: core build, JavaScript regex engine (no wasm), a curated set
// of languages and dual light/dark themes switched by CSS (.dark). Loaded lazily on first use.
import type { HighlighterCore } from "shiki/core";

let hl: Promise<HighlighterCore> | null = null;

const LANGS: Record<string, () => Promise<unknown>> = {
  ts: () => import("shiki/langs/typescript.mjs"),
  tsx: () => import("shiki/langs/tsx.mjs"),
  js: () => import("shiki/langs/javascript.mjs"),
  jsx: () => import("shiki/langs/jsx.mjs"),
  json: () => import("shiki/langs/json.mjs"),
  go: () => import("shiki/langs/go.mjs"),
  python: () => import("shiki/langs/python.mjs"),
  rust: () => import("shiki/langs/rust.mjs"),
  bash: () => import("shiki/langs/bash.mjs"),
  css: () => import("shiki/langs/css.mjs"),
  html: () => import("shiki/langs/html.mjs"),
  yaml: () => import("shiki/langs/yaml.mjs"),
  toml: () => import("shiki/langs/toml.mjs"),
  sql: () => import("shiki/langs/sql.mjs"),
  markdown: () => import("shiki/langs/markdown.mjs"),
  diff: () => import("shiki/langs/diff.mjs"),
  prisma: () => import("shiki/langs/prisma.mjs"),
  powershell: () => import("shiki/langs/powershell.mjs"),
  swift: () => import("shiki/langs/swift.mjs"),
};

const ALIAS: Record<string, string> = {
  typescript: "ts",
  javascript: "js",
  mjs: "js",
  cjs: "js",
  sh: "bash",
  shell: "bash",
  zsh: "bash",
  console: "bash",
  shellscript: "bash",
  py: "python",
  rs: "rust",
  yml: "yaml",
  golang: "go",
  md: "markdown",
  ps1: "powershell",
  pwsh: "powershell",
  jsonc: "json",
  patch: "diff",
};

export function normalizeLang(lang: string | undefined): string | null {
  if (!lang) return null;
  const l = lang.toLowerCase();
  const k = ALIAS[l] ?? l;
  return LANGS[k] ? k : null;
}

async function highlighter(): Promise<HighlighterCore> {
  if (!hl) {
    hl = (async () => {
      const [{ createHighlighterCore }, { createJavaScriptRegexEngine }, light, dark] = await Promise.all([
        import("shiki/core"),
        import("shiki/engine/javascript"),
        import("shiki/themes/github-light-default.mjs"),
        import("shiki/themes/github-dark-default.mjs"),
      ]);
      return createHighlighterCore({
        themes: [light.default, dark.default],
        langs: [],
        engine: createJavaScriptRegexEngine({ forgiving: true }),
      });
    })();
  }
  return hl;
}

const loaded = new Set<string>();
const cache = new Map<string, string>();

/** HTML for code, or null when the language is unknown. */
export async function highlight(code: string, lang: string | undefined): Promise<string | null> {
  const l = normalizeLang(lang);
  if (!l || code.length > 200_000) return null;
  const key = l + "\0" + code;
  const hit = cache.get(key);
  if (hit) return hit;
  const h = await highlighter();
  if (!loaded.has(l)) {
    const mod = (await LANGS[l]()) as { default: Parameters<HighlighterCore["loadLanguage"]>[0] };
    await h.loadLanguage(mod.default);
    loaded.add(l);
  }
  const ids: Record<string, string> = { ts: "typescript", js: "javascript", bash: "bash" };
  const html = h.codeToHtml(code, {
    lang: ids[l] ?? l,
    themes: { light: "github-light-default", dark: "github-dark-default" },
    defaultColor: "light",
  });
  if (cache.size > 300) cache.clear();
  cache.set(key, html);
  return html;
}
