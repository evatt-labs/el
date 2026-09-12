# Archived: Node/JS environments blueprint

Superseded 2026-09-12. `kraai` is moving to a Go rewrite, shipped as a
single binary (Terraform/kubectl-style), with `npx kraai`/`npm install -g
kraai` preserved via a thin npm postinstall wrapper that fetches the
platform binary.

The architectural *decisions* in `BLUEPRINT.md` (D3–D20: state ownership,
adopt/external, plan/apply/destroy, the unified pluggable
provider/resource/backend/middleware registry, expanded lifecycle hooks,
CI-composable protected-environment gating, the generic S3-compatible
state backend) mostly carry forward as inputs to the Go-native blueprint.
What doesn't carry forward as-written: D1 (the `yaml` npm package), D2's
"hooks stay JS," and D18's "a plugin is an npm package or local file" —
these were JS-specific mechanics. In Go, plugins are expected to become
separate compiled binaries speaking a gRPC protocol (HashiCorp
`go-plugin` style, as Terraform/Vault/Packer providers do), not JS
modules.

`docs/workstreams.yaml` here targeted the JS codebase (`src/*.mjs`) and
does not apply to the Go rewrite; kept for reference only.

Kept for history and to avoid re-deriving decisions already made once.
Do not implement against this document.
