# AGENTS.md

Maintainer/agent notes for working on this repo. Not user-facing — see
`README.md` for that.

## Releasing

Versioning and changelog generation are automated end to end. The only
manual step left is the one-time npm bootstrap below.

### Commit messages must follow Conventional Commits

`release-please` (`.github/workflows/release-please.yml`) parses commit
messages on every push to `main` to decide the next version and what goes
in `CHANGELOG.md`. Prefix every commit:

- `fix:` — patch release
- `feat:` — minor release
- `feat!:` / `fix!:` / a `BREAKING CHANGE:` footer — major release
- `chore:`, `docs:`, `test:`, `refactor:` — no release, but still shows up
  in the changelog under its own section

A commit with no recognized prefix is not picked up by release-please at
all: it won't appear in the changelog and won't trigger a version bump.

### The release PR

Every push to `main` that contains releasable commits opens or updates a
"release PR" (bumping `package.json`, `.release-please-manifest.json`, and
`CHANGELOG.md`). Nothing is released until that PR is merged. Merging it
is the actual release trigger: release-please tags the merge commit,
creates the GitHub Release with notes generated from the commits since
the last release, and pushes the tag.

### What the tag push does

`.github/workflows/publish.yml` runs on any `v*` tag push. It runs the
test suite, publishes to npm using npm's
[trusted publishing](https://docs.npmjs.com/trusted-publishers) (OIDC, no
`NPM_TOKEN` secret in this repo), then packs the same tree with
`npm pack` and attaches that tarball to the GitHub Release release-please
already created for that tag.

### One-time npm bootstrap

Trusted publishing only works once a package version already exists on
the registry: trusted-publisher configuration lives on the package's own
settings page on npmjs.com, so nothing can bootstrap its own trust on a
name that's never been published. Before the first `v*` tag exists, this
step must be done manually, once, from an authenticated npm account with
2FA:

```sh
npm publish --provenance --access public
```

Then, on npmjs.com, add `evatt-labs/el` and `publish.yml` as this
package's trusted publisher (or `npm trust github @evatt-labs/el`). Every
tag push after that publishes with no stored credential at all.
