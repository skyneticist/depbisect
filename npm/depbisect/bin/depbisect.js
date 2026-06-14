#!/usr/bin/env node
"use strict";

// Thin launcher for the `depbisect` npm package.
//
// The real program is a single Go binary, shipped per platform as an
// optionalDependency (e.g. @depbisect/linux-x64). npm/pnpm/yarn/bun install
// only the package whose `os`/`cpu` match the host, so there is no install
// script, no network fetch at install time, and no arbitrary code execution.
//
// This launcher resolves that binary and execs it, forwarding the arguments,
// stdio, and — importantly — the exact exit code. depbisect's exit codes
// (0 through 5) are part of its contract, so they must pass through unchanged.
// No shell is involved, matching depbisect's own no-shell execution guarantee.

const { execFileSync } = require("node:child_process");

const platform = process.platform; // "linux" | "darwin" | "win32" | ...
const arch = process.arch; // "x64" | "arm64" | ...
const pkg = `@depbisect/${platform}-${arch}`;
const binary = platform === "win32" ? "depbisect.exe" : "depbisect";

let binaryPath;
try {
  binaryPath = require.resolve(`${pkg}/${binary}`);
} catch {
  console.error(
    `depbisect: no prebuilt binary found for ${platform}-${arch}.\n\n` +
      `The matching package (${pkg}) was not installed. This typically happens when\n` +
      `optional dependencies were skipped (e.g. --no-optional / --omit=optional) or the\n` +
      `platform is unsupported.\n\n` +
      `Reinstall without skipping optional dependencies, or download a binary directly:\n` +
      `  https://github.com/skyneticist/depbisect/releases\n`
  );
  process.exit(1);
}

try {
  execFileSync(binaryPath, process.argv.slice(2), { stdio: "inherit" });
} catch (err) {
  // A non-zero exit from depbisect surfaces here as err.status.
  if (typeof err.status === "number") process.exit(err.status);
  // Terminated by a signal (e.g. Ctrl-C) — exit non-zero.
  if (err.signal) process.exit(1);
  // Failed to spawn (missing/unexecutable binary, etc.).
  console.error(`depbisect: failed to run ${binaryPath}: ${err.message}`);
  process.exit(1);
}
