// Maps Node's process.platform/process.arch to the GOOS/GOARCH pair
// .goreleaser.yaml actually builds (linux/darwin, amd64/arm64 — see
// .goreleaser.yaml's builds.goos/goarch). Kept as its own module so both
// the postinstall downloader and any future diagnostics can import it
// without duplicating the mapping.

const GOOS = {
  linux: "linux",
  darwin: "darwin",
};

const GOARCH = {
  x64: "amd64",
  arm64: "arm64",
};

/**
 * @returns {{ goos: string, goarch: string }}
 * @throws {Error} if the current platform/arch isn't one GoReleaser builds.
 */
export function resolvePlatform() {
  const goos = GOOS[process.platform];
  const goarch = GOARCH[process.arch];

  if (!goos || !goarch) {
    throw new Error(
      `kraai has no prebuilt binary for platform "${process.platform}/${process.arch}". ` +
        `Supported: ${Object.keys(GOOS).join(", ")} x ${Object.keys(GOARCH).join(", ")}. ` +
        "See .goreleaser.yaml for the current build matrix.",
    );
  }

  return { goos, goarch };
}
