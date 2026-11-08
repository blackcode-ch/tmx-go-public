import { createHash } from "node:crypto";
import { readdir, readFile } from "node:fs/promises";

const root = new URL("../dist/", import.meta.url);

async function files(directory, prefix = "") {
  const result = [];
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const relative = `${prefix}${entry.name}`;
    if (entry.isDirectory()) {
      result.push(...await files(new URL(`${entry.name}/`, directory), `${relative}/`));
    } else if (entry.isFile()) {
      result.push(relative);
    } else {
      throw new Error(`Unexpected non-file in dist: ${relative}`);
    }
  }
  return result;
}

// Sort relative POSIX paths, include dotfiles, hash raw bytes, and always emit LF.
// Keep this output outside dist so the manifest never includes itself.
for (const relative of (await files(root)).sort()) {
  const hash = createHash("sha256").update(await readFile(new URL(relative, root))).digest("hex");
  process.stdout.write(`${hash}  ${relative}\n`);
}
