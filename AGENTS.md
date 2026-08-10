# AGENTS.md

Maintainer/agent notes for working on this repo. Not user-facing — see
`README.md` for that.

## Releasing

`.github/workflows/publish.yml` publishes on any `v*` tag push, using npm's
[trusted publishing](https://docs.npmjs.com/trusted-publishers) (OIDC) — no
`NPM_TOKEN` secret lives in this repo.

That only works once a package version already exists on the registry:
trusted-publisher configuration lives on the package's own settings page on
npmjs.com, so nothing can bootstrap its own trust on a name that's never
been published. The first release is manual:

```sh
npm publish --provenance --access public
```

...then, on npmjs.com, add `evatt-labs/el` and `publish.yml` as this
package's trusted publisher (or `npm trust github @evatt-labs/el`). Every
tag push after that publishes with no stored credential at all.
