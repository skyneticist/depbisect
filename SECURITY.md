# Security Policy

## Supported versions

DepBisect is pre-1.0. Security fixes are released against the latest published
version; please upgrade to the most recent release before reporting.

| Version | Supported          |
| ------- | ------------------ |
| latest  | :white_check_mark: |
| older   | :x:                |

## Reporting a vulnerability

**Please do not open a public issue for security problems.**

Report privately through GitHub's
[private vulnerability reporting](https://github.com/skyneticist/depbisect/security/advisories/new)
(the **Security → Report a vulnerability** tab). If you cannot use GitHub
Security Advisories, email **hunterhartline@gmail.com** with the details.

Please include:

- a description of the issue and its impact,
- the DepBisect version (`depbisect version`), OS, and package manager,
- steps to reproduce or a proof of concept, and
- any suggested remediation.

You can expect an acknowledgement within **3 business days** and a status update
within **10 business days**. Once a fix is available we will coordinate a
release and credit you in the advisory unless you prefer to remain anonymous.

## Scope and threat model

DepBisect installs dependency candidates in an isolated, throwaway git worktree
and runs your verification command with an exact argument vector (no shell).
**Installing dependencies executes their lifecycle scripts**, so bisecting
untrusted version ranges runs untrusted code by design — run it in the same
sandbox you would trust for `npm install` of those versions.

For the full threat model — what DepBisect will and will not touch, and what
reports do and do not contain — see [docs/security.md](docs/security.md).
