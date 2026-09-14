# kraai

kraai is being rewritten in Go, shipped as a single binary. See
[`docs/BLUEPRINT.md`](docs/BLUEPRINT.md) for the design and
[`docs/workstreams.yaml`](docs/workstreams.yaml) for build order. Nothing
in the Go rewrite is implemented yet — this repo root is currently just
the design documents plus the pre-rewrite JS package described below.

## The current, published package

The existing npm package (`kraai`, currently 0.5.x) is JavaScript/Node
and lives at [`legacy-node/`](legacy-node/) now — frozen as the legacy
line during the rewrite, not under active feature development. See
[`legacy-node/README.md`](legacy-node/README.md) for its own
documentation. The design history that produced it lived at
`docs/archive/node-cli/` and was removed once the rewrite's own blueprint
superseded it; `git log -- docs/archive/node-cli` still has it.

## Contributing to the rewrite

Read `docs/BLUEPRINT.md` first. Each entry in `docs/workstreams.yaml` is
one buildable unit, in dependency order, with its own acceptance
criteria.
