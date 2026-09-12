# Changelog

## [0.4.1](https://github.com/evatt-labs/el/compare/v0.4.0...v0.4.1) (2026-09-12)


### Features

* ship a GitHub Action for one preview environment per pull request ([#27](https://github.com/evatt-labs/el/issues/27)) ([747d4bd](https://github.com/evatt-labs/el/commit/747d4bda39f8353743fa9cfb8199227be991c9d1))

## [0.4.0](https://github.com/evatt-labs/el/compare/v0.3.0...v0.4.0) (2026-09-12)


### ⚠ BREAKING CHANGES

* the top-level `neon` config key is gone. Use `database: { provider: "neon", project, database, appRole }`. seed() receives ownerConnectionString/runSql/quoteLiteral only when a database provider is configured.

### Features

* make the database layer a pluggable provider, D1-only by default ([#22](https://github.com/evatt-labs/el/issues/22)) ([bcd8a4f](https://github.com/evatt-labs/el/commit/bcd8a4febdb07a6a1a726eec123c7e13dd62c14c))


### Bug Fixes

* **ci:** keep release tags as v&lt;version&gt;, not el-v&lt;version&gt; ([#25](https://github.com/evatt-labs/el/issues/25)) ([6b027bd](https://github.com/evatt-labs/el/commit/6b027bd748ab09b89b58de1eca52c239276222a1))
* **ci:** let release-please read release-please-config.json ([#24](https://github.com/evatt-labs/el/issues/24)) ([1626e46](https://github.com/evatt-labs/el/commit/1626e46c30ed42f5a23420c325e89ba2f6e4dc7e))

## [0.3.0](https://github.com/evatt-labs/el/compare/v0.2.0...v0.3.0) (2026-08-11)


### Features

* sign release artifacts and add a security policy ([#6](https://github.com/evatt-labs/el/issues/6)) ([5b2e34f](https://github.com/evatt-labs/el/commit/5b2e34fc7f529e2faed7c61e6f536c11841ade96))

## [0.2.0](https://github.com/evatt-labs/el/compare/v0.1.1...v0.2.0) (2026-08-11)


### Features

* add security scanning and test coverage reporting ([fccd212](https://github.com/evatt-labs/el/commit/fccd21279d1525de385ad774060df0cc6f3bf63d))

## 0.1.1 (2026-08-10)


### Bug Fixes

* correct npm bootstrap instructions in AGENTS.md ([f4c20e3](https://github.com/evatt-labs/el/commit/f4c20e355f4994def4ab1d16777dde34aa50fadc))


### Miscellaneous Chores

* pin the first release to 0.1.1 ([6926fa6](https://github.com/evatt-labs/el/commit/6926fa69c2d2d3216e9d29ef0dd8783b4bfb51f0))
