// Downloads the GoReleaser-built kraai binary for the current platform from
// GitHub Releases and installs it at ../.bin/kraai, which bin/kraai.js execs.
//
// STUBBED (see go-module-scaffold PR description): no GoReleaser release of
// this Go rewrite exists yet, so this script cannot succeed end-to-end
// today — every download attempt 404s. It's still written the way it'll
// actually run once a real `v<version>` tag exists, rather than a fake
// no-op, so there's exactly one thing to re-verify (the URL 404s vs.
// doesn't) once a release is cut. Set KRAAI_SKIP_POSTINSTALL=1 to skip this
// step entirely (e.g. local development against this repo, offline CI).
import { createWriteStream } from "node:fs";
import { chmod, mkdir, readFile, rm } from "node:fs/promises";
import https from "node:https";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { spawnSync } from "node:child_process";

import { resolvePlatform } from "./platform.js";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const packageRoot = path.resolve(__dirname, "..");
const binDir = path.join(packageRoot, ".bin");
const binPath = path.join(binDir, "kraai");

const MAX_REDIRECTS = 5;

function download(url, redirectsLeft = MAX_REDIRECTS) {
  return new Promise((resolve, reject) => {
    https
      .get(url, (res) => {
        const { statusCode, headers } = res;

        if (statusCode >= 300 && statusCode < 400 && headers.location) {
          res.resume(); // discard body
          if (redirectsLeft <= 0) {
            reject(new Error(`too many redirects fetching ${url}`));
            return;
          }
          resolve(download(headers.location, redirectsLeft - 1));
          return;
        }

        if (statusCode !== 200) {
          res.resume();
          reject(
            new Error(
              `GET ${url} returned ${statusCode} — this kraai version has no ` +
                "published release binary for your platform yet. See " +
                "https://github.com/evatt-labs/kraai/releases.",
            ),
          );
          return;
        }

        resolve(res);
      })
      .on("error", reject);
  });
}

async function main() {
  if (process.env.KRAAI_SKIP_POSTINSTALL === "1") {
    console.log("kraai: KRAAI_SKIP_POSTINSTALL=1 set, skipping binary download.");
    return;
  }

  const { goos, goarch } = resolvePlatform();
  const { version } = JSON.parse(await readFile(path.join(packageRoot, "package.json"), "utf8"));

  const archiveName = `kraai_${version}_${goos}_${goarch}.tar.gz`;
  const url = `https://github.com/evatt-labs/kraai/releases/download/v${version}/${archiveName}`;

  const tmpArchive = path.join(tmpdir(), `kraai-install-${process.pid}.tar.gz`);

  console.log(`kraai: downloading ${url}`);
  const response = await download(url);
  await new Promise((resolve, reject) => {
    const file = createWriteStream(tmpArchive);
    response.pipe(file);
    file.on("finish", resolve);
    file.on("error", reject);
  });

  await mkdir(binDir, { recursive: true });

  // GoReleaser packages unix archives as tar.gz (see .goreleaser.yaml
  // archives.formats); tar ships on every platform this package supports
  // (linux, darwin — see scripts/platform.js), so shelling out avoids a
  // tar-parsing dependency for a two-file archive.
  const extract = spawnSync("tar", ["-xzf", tmpArchive, "-C", binDir, "kraai"], {
    stdio: "inherit",
  });
  await rm(tmpArchive, { force: true });

  if (extract.status !== 0) {
    throw new Error(`failed to extract ${archiveName} (tar exited ${extract.status})`);
  }

  await chmod(binPath, 0o755);
  console.log(`kraai: installed ${binPath}`);
}

main().catch((err) => {
  console.error(`kraai postinstall failed: ${err.message}`);
  process.exit(1);
});
