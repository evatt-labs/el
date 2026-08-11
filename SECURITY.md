# Security Policy

## Reporting a vulnerability

Use GitHub's [private vulnerability reporting](https://github.com/evatt-labs/el/security/advisories/new)
for this repository. Do not open a public issue for a security problem.

You'll get an initial response within a few days. There's no fixed SLA
beyond that: this is a small, actively-used tool with one maintainer, not
a funded security program.

## Supported versions

Only the latest published version on npm gets fixes. There's no backport
policy for older versions.

## Scope

`el` runs with real Cloudflare and Neon credentials in scope (see
[Trust model](README.md#trust-model) in the README). In scope for a
report:

- Anything that leaks a credential (`NEON_API_KEY`,
  `CLOUDFLARE_API_TOKEN`, a Neon connection string) into logs, error
  messages, or process argv.
- Anything that lets a deployed ephemeral environment reach a production
  resource it shouldn't (see [Bindings are not inherited by
  default](README.md#bindings-are-not-inherited-by-default)).
- Command injection or arbitrary code execution beyond what
  `el.config.mjs` already intentionally runs as ordinary Node code.
- A CI/release supply-chain issue: an unpinned action, a workflow trigger
  that would let a fork's code run with this repo's secrets in scope.

Out of scope: `el.config.mjs` itself is designed to run as trusted code
with full access to your environment, the same as `webpack.config.js` --
that's not a vulnerability, it's documented behavior.
