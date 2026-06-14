#!/usr/bin/env node
// build-npm.mjs — assemble the npm packages for a release.
//
// For each target in npm/platforms.json this:
//   1. cross-compiles the depbisect binary (CGO_ENABLED=0, same flags as
//      goreleaser: -trimpath, -s -w, -X main.version=<version>);
//   2. writes a per-platform package (@depbisect/<os>-<cpu>) containing just
//      that binary, with matching `os`/`cpu` constraints;
//   3. syncs the launcher package (depbisect) version and optionalDependencies.
//
// The per-platform packages and the launcher's LICENSE are generated (and
// gitignored). Publish the platform packages first, then the launcher, so the
// optionalDependencies resolve. See .github/workflows/npm-publish.yml.
//
// Usage:  node scripts/build-npm.mjs <version>        (leading "v" is stripped)

import { execFileSync } from "node:child_process";
import {
  chmodSync,
  copyFileSync,
  mkdirSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const scriptDir = dirname(fileURLToPath(import.meta.url));
const repoRoot = join(scriptDir, "..");
const npmDir = join(repoRoot, "npm");
const launcherDir = join(npmDir, "depbisect");
const licenseSrc = join(repoRoot, "LICENSE");

function fail(message) {
  console.error(`build-npm: ${message}`);
  process.exit(1);
}

const rawVersion = process.argv[2] ?? process.env.VERSION;
if (!rawVersion) fail("usage: node scripts/build-npm.mjs <version>");
const version = String(rawVersion).replace(/^v/, "");
if (!/^\d+\.\d+\.\d+/.test(version)) {
  fail(`invalid version ${JSON.stringify(rawVersion)} (expected semver like 0.1.1)`);
}

try {
  execFileSync("go", ["version"], { stdio: "ignore" });
} catch {
  fail("the Go toolchain is required on PATH to cross-compile the binaries");
}

const platforms = JSON.parse(readFileSync(join(npmDir, "platforms.json"), "utf8"));

// Start clean so stale targets never leak into a release.
const scopeDir = join(npmDir, "@depbisect");
rmSync(scopeDir, { recursive: true, force: true });

const platformDirs = [];
for (const p of platforms) {
  const pkgName = `@depbisect/${p.os}-${p.cpu}`;
  const dir = join(scopeDir, `${p.os}-${p.cpu}`);
  const binary = p.os === "win32" ? "depbisect.exe" : "depbisect";
  const outPath = join(dir, binary);
  mkdirSync(dir, { recursive: true });

  console.log(`building ${pkgName}@${version}  (GOOS=${p.goos} GOARCH=${p.goarch})`);
  execFileSync(
    "go",
    [
      "build",
      "-trimpath",
      "-ldflags",
      `-s -w -X main.version=${version}`,
      "-o",
      outPath,
      "./cmd/depbisect",
    ],
    {
      cwd: repoRoot,
      stdio: "inherit",
      env: { ...process.env, CGO_ENABLED: "0", GOOS: p.goos, GOARCH: p.goarch },
    }
  );
  if (p.os !== "win32") chmodSync(outPath, 0o755);

  const pkgJSON = {
    name: pkgName,
    version,
    description: `Prebuilt depbisect binary for ${p.os}-${p.cpu}.`,
    repository: {
      type: "git",
      url: "git+https://github.com/skyneticist/depbisect.git",
    },
    license: "MIT",
    // Keep the binary on disk under Yarn Plug'n'Play instead of zipping it.
    preferUnplugged: true,
    os: [p.os],
    cpu: [p.cpu],
    files: [binary],
  };
  writeFileSync(join(dir, "package.json"), JSON.stringify(pkgJSON, null, 2) + "\n");
  copyFileSync(licenseSrc, join(dir, "LICENSE"));
  platformDirs.push(dir);
}

// Sync the launcher package: version + optionalDependencies pinned to this release.
const launcherPkgPath = join(launcherDir, "package.json");
const launcherPkg = JSON.parse(readFileSync(launcherPkgPath, "utf8"));
launcherPkg.version = version;
launcherPkg.optionalDependencies = Object.fromEntries(
  platforms.map((p) => [`@depbisect/${p.os}-${p.cpu}`, version])
);
writeFileSync(launcherPkgPath, JSON.stringify(launcherPkg, null, 2) + "\n");
copyFileSync(licenseSrc, join(launcherDir, "LICENSE"));

console.log("\nReady to publish (platform packages first, launcher last):");
for (const dir of platformDirs) console.log(`  npm publish ${dir}`);
console.log(`  npm publish ${launcherDir}`);
