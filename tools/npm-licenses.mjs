// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

/**
 * Checks the licences of the installed npm packages of a Kogge package (like the Maven licence check): permissive
 * licences, MPL and LGPL are allowed; GPL, AGPL, SSPL and packages without a licence fail the check.
 *
 * Usage: node tools/npm-licenses.mjs <package directory> …
 *        node tools/npm-licenses.mjs <package directory> --append <notice file>
 *
 * With --append, the packages shipped in the package's build are added to the third-party notice file with their
 * licence texts: those of a production install (npm ci --omit=dev), i.e. every package the lock file does not
 * mark as dev-only, also optional ones (the link editor's bubble menu is an optional dependency of TipTap), except
 * platform-specific optional ones (native programs of build tools) and NOT_SHIPPED; the fonts of FONTS with theirs.
 * tools/sbom lists the same packages in the SBOM and keeps its own copies of KNOWN and NOT_SHIPPED.
 */

import { appendFileSync, existsSync, readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";

const ALLOWED = new Set([
  "MIT",
  "MIT-0",
  "ISC",
  "BSD-2-Clause",
  "BSD-3-Clause",
  "Apache-2.0",
  "0BSD",
  "Unlicense",
  "CC0-1.0",
  "MPL-2.0",
  "LGPL-2.1-only",
  "LGPL-2.1-or-later",
  "LGPL-3.0-only",
  "LGPL-3.0-or-later",
  "BlueOak-1.0.0",
  "Python-2.0",
  "EUPL-1.2",
  // Build-time browser data (caniuse-lite); attribution only.
  "CC-BY-4.0",
]);

// Packages whose manifest does not name their whole licence: what their licence files state, checked for that
// version. khroma's names none (its licence file is MIT); katex's names MIT, which its fonts are not (FONTS).
const KNOWN = new Map([
  ["katex@0.19.0", "MIT AND OFL-1.1"],
  ["khroma@2.1.0", "MIT"],
]);

// Packages that ship fonts with their code, besides @fontsource: their fonts may be under the SIL Open Font
// License, which allows bundling them with any software, and their licence must be recorded in KNOWN.
// The notices add what the package's licence file leaves out: the fonts' copyright (from their name tables) and
// licence.
const FONTS = new Map([
  [
    "katex",
    `The fonts in dist/fonts (KaTeX_AMS, KaTeX_Caligraphic, KaTeX_Fraktur, KaTeX_Main, KaTeX_Math, KaTeX_SansSerif,
KaTeX_Script, KaTeX_Size1, KaTeX_Size2, KaTeX_Size3, KaTeX_Size4, KaTeX_Typewriter), of which the web application
ships the woff2 files unmodified:

Copyright (c) 2009-2010, Design Science, Inc. (<www.mathjax.org>)
Copyright (c) 2014-2018 Khan Academy (<www.khanacademy.org>),
with Reserved Font Names KaTeX_AMS, KaTeX_Caligraphic, KaTeX_Fraktur, KaTeX_Main, KaTeX_Math, KaTeX_SansSerif,
KaTeX_Script, KaTeX_Size1, KaTeX_Size2, KaTeX_Size3, KaTeX_Size4 and KaTeX_Typewriter.

This Font Software is licensed under the SIL Open Font License, Version 1.1.`,
  ],
]);

// Packages installed with others but not shipped: the web build leaves them out and fails if one gets in.
const NOT_SHIPPED = new Map([
  [
    "elkjs",
    "Mermaid's optional ELK layout (EPL-2.0, which Kogge's licence policy does not allow) is left out of the web build (vite.config.ts)",
  ],
]);

/**
 * A licence expression is allowed if every alternative of an OR, or every part of an AND, is. Fonts are embedded
 * data, not linked code: the SIL Open Font License is allowed for packages that ship fonts.
 */
function allowed(expression, fonts = false) {
  const text = expression.replace(/[()]/g, " ").trim();
  if (text.includes(" OR ")) {
    return text.split(" OR ").some((part) => allowed(part, fonts));
  }
  if (text.includes(" AND ")) {
    return text.split(" AND ").every((part) => allowed(part, fonts));
  }
  return ALLOWED.has(text) || (fonts && text === "OFL-1.1");
}

/** Whether a package ships fonts. */
function shipsFonts(name) {
  return name.startsWith("@fontsource") || FONTS.has(name);
}

function licence(pkg) {
  const known = KNOWN.get(`${pkg.name}@${pkg.version}`);
  if (known) {
    return known;
  }
  if (typeof pkg.license === "string") {
    return pkg.license;
  }
  if (pkg.license?.type) {
    return pkg.license.type;
  }
  if (Array.isArray(pkg.licenses)) {
    return pkg.licenses.map((l) => l.type ?? l).join(" OR ");
  }
  return null;
}

function packages(directory, found = []) {
  if (!existsSync(directory)) {
    return found;
  }
  for (const name of readdirSync(directory)) {
    if (name.startsWith(".")) {
      continue;
    }
    const path = join(directory, name);
    if (name.startsWith("@")) {
      packages(path, found);
      continue;
    }
    const manifest = join(path, "package.json");
    if (existsSync(manifest)) {
      found.push(JSON.parse(readFileSync(manifest, "utf8")));
      packages(join(path, "node_modules"), found);
    }
  }
  return found;
}

/** The licence file of an installed package, if it has one. */
function licenceText(directory) {
  const file = readdirSync(directory).find((n) =>
    /^(licen[cs]e|copying)(\.|$)/i.test(n),
  );
  return file ? readFileSync(join(directory, file), "utf8").trim() : null;
}

/** The text of the SIL Open Font License 1.1, as the shipped font packages carry it in their licence files. */
function oflText(root, shipped) {
  for (const p of shipped) {
    const text = licenceText(join(root, p.path)) ?? "";
    const at = text.search(/-+\r?\nSIL OPEN FONT LICENSE Version 1\.1/);
    if (at >= 0) {
      return text.slice(at);
    }
  }
  throw new Error(
    `${root}: no shipped package carries the text of the SIL Open Font License 1.1, which the notices of fonts need`,
  );
}

/** Appends the packages a build ships, with their licence texts, to the notice file. */
function appendNotices(root, notices) {
  const lock = JSON.parse(
    readFileSync(join(root, "package-lock.json"), "utf8"),
  );
  const shipped = Object.entries(lock.packages ?? {})
    .filter(
      ([path, entry]) =>
        path.startsWith("node_modules/") &&
        !entry.dev &&
        !entry.link &&
        !(entry.optional && (entry.os || entry.cpu)) &&
        !NOT_SHIPPED.has(path.slice(path.lastIndexOf("node_modules/") + 13)),
    )
    .map(([path, entry]) => {
      const pkg = JSON.parse(
        readFileSync(join(root, path, "package.json"), "utf8"),
      );
      return {
        name: pkg.name,
        version: pkg.version,
        licence: licence(pkg) ?? entry.license ?? "(none)",
        path,
      };
    })
    .sort(
      (a, b) =>
        a.name.localeCompare(b.name) || a.version.localeCompare(b.version),
    );
  const ofl = shipped.some((p) => FONTS.has(p.name))
    ? oflText(root, shipped)
    : "";
  let out = `\n## npm packages of the web application\n\n| Package | Version | Licence |\n|---|---|---|\n`;
  for (const p of shipped) {
    out += `| ${p.name} | ${p.version} | ${p.licence} |\n`;
  }
  out += `\n### Licence texts of the npm packages\n`;
  for (const p of shipped) {
    const text = licenceText(join(root, p.path));
    out += `\n#### ${p.name} ${p.version}\n\n`;
    out += text
      ? "```text\n" + text + "\n```\n"
      : `The package names the licence ${p.licence} and has no licence file.\n`;
    const fonts = FONTS.get(p.name);
    if (fonts) {
      out += `\n${fonts}\n\n` + "```text\n" + ofl + "\n```\n";
    }
  }
  appendFileSync(notices, out);
  console.log(
    `${root}: ${shipped.length} shipped packages added to ${notices}`,
  );
}

const args = process.argv.slice(2);
const appendAt = args.indexOf("--append");
if (appendAt >= 0) {
  appendNotices(args[0], args[appendAt + 1]);
  args.splice(appendAt, 2);
}

let failed = false;
for (const root of args) {
  const installed = packages(join(root, "node_modules"));
  if (installed.length === 0) {
    console.error(`${root}: no installed packages; run npm ci first`);
    failed = true;
    continue;
  }
  const counts = new Map();
  for (const pkg of installed) {
    const expression = licence(pkg);
    if (NOT_SHIPPED.has(pkg.name)) {
      console.log(`${root}: ${pkg.name}@${pkg.version} (${expression}) is not shipped: ${NOT_SHIPPED.get(pkg.name)}`);
    } else if (!expression || !allowed(expression, shipsFonts(pkg.name))) {
      console.error(
        `${root}: ${pkg.name}@${pkg.version} has the licence ${expression ?? "(none)"}, which is not allowed`,
      );
      failed = true;
    } else if (
      FONTS.has(pkg.name) &&
      !KNOWN.has(`${pkg.name}@${pkg.version}`)
    ) {
      // Its manifest names the licence of its code only: the fonts' licence is checked for each version.
      console.error(
        `${root}: ${pkg.name}@${pkg.version} ships fonts: record its licence in KNOWN (and tools/sbom's knownLicences)`,
      );
      failed = true;
    }
    counts.set(expression, (counts.get(expression) ?? 0) + 1);
  }
  console.log(
    `${root}: ${installed.length} packages, ${[...counts].map(([l, n]) => `${l} ${n}`).join(", ")}`,
  );
}
process.exit(failed ? 1 : 0);
