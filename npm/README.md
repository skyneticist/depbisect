# npm packaging

This directory holds the npm distribution of DepBisect: a thin `depbisect`
launcher package plus one prebuilt-binary package per platform.

## Layout

| Path | What it is | Committed? |
| ---- | ---------- | ---------- |
| `depbisect/` | The launcher package users install (`npm i -g depbisect`). Its `bin/depbisect.js` locates and executes the right platform binary. | yes |
| `@depbisect/<os>-<cpu>/` | Per-platform packages containing a single cross-compiled binary, declared as `optionalDependencies` of the launcher. | no — generated |
| `platforms.json` | The target matrix (`os`/`cpu` ↔ `GOOS`/`GOARCH`) consumed by the build script. | yes |

`scripts/build-npm.mjs <version>` generates everything: it cross-compiles each
binary (same flags as goreleaser), writes the `@depbisect/*` packages, and
syncs the launcher's `version` + pinned `optionalDependencies`. Note that the
sync **rewrites the committed `depbisect/package.json`**, so a local build
dirties the working tree; that file is intentionally committed at the last
published version.

## Publishing

**Normal path: CI.** Pushing a `v*.*.*` tag runs
`.github/workflows/npm-publish.yml`, which builds and publishes every package
with provenance via npm Trusted Publishing (OIDC — no token). The workflow can
also be run manually (`workflow_dispatch`) with an explicit version.

**Local path:** for the two cases CI can't cover, use:

```sh
scripts/publish-npm.sh <version> [--dry-run]
```

- **Bootstrapping** — Trusted Publishing only works for packages that already
  exist on npmjs.com, so the *first* version of every package must be
  published manually with this script, then a Trusted Publisher (GitHub
  Actions, repo `skyneticist/depbisect`, workflow `npm-publish.yml`) added to
  each package.
- **Recovery** — backfilling a version whose workflow run was missed.

The script publishes the platform packages before the launcher (so the
launcher's pinned `optionalDependencies` resolve), requires `npm login`
(except with `--dry-run`), and omits `--provenance`, which only works from CI.

## Gotchas

- **Publish order matters**: platform packages first, launcher last.
- **Versions are immutable on npm**: if a version was ever published (even
  out-of-band), the tag-triggered workflow for that same version will fail.
  Check `npm view depbisect versions` before choosing the next release tag.
- The npm-facing `depbisect/README.md` must use **absolute** URLs for images
  and links — npmjs.com does not resolve repo-relative paths.
