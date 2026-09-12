// Downloads the GoReleaser-built kraai binary for the current platform from
// GitHub Releases, verifies it against the release's checksums.txt (itself
// cosign-signed — see .goreleaser.yaml's checksum/signs config) before
// touching it, and installs it at ../.bin/kraai, which bin/kraai.js execs.
//
// STUBBED (see go-module-scaffold PR description): no GoReleaser release of
// this Go rewrite exists yet, so this script cannot succeed end-to-end
// today — every download attempt 404s, checksum verification included.
// It's still written the way it'll actually run once a real `v<version>`
// tag exists, rather than a fake no-op, so there's exactly one thing to
// re-verify (the URL 404s vs. doesn't) once a release is cut. Set
// KRAAI_SKIP_POSTINSTALL=1 to skip this step entirely (e.g. local
// development against this repo, offline CI).
import { createReadStream, createWriteStream } from "node:fs";
import { chmod, mkdir, readFile, rm } from "node:fs/promises";
import { createHash } from "node:crypto";
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
                "published release assets yet. See " +
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

function downloadToBuffer(url) {
  return download(url).then(
    (res) =>
      new Promise((resolve, reject) => {
        const chunks = [];
        res.on("data", (chunk) => chunks.push(chunk));
        res.on("end", () => resolve(Buffer.concat(chunks)));
        res.on("error", reject);
      }),
  );
}

function sha256File(filePath) {
  return new Promise((resolve, reject) => {
    const hash = createHash("sha256");
    const stream = createReadStream(filePath);
    stream.on("data", (chunk) => hash.update(chunk));
    stream.on("end", () => resolve(hash.digest("hex")));
    stream.on("error", reject);
  });
}

// checksums.txt is `sha256sum`-format: "<hex digest>  <filename>" per line,
// one shared file per release covering every platform's archive (see
// .goreleaser.yaml's checksum.name_template) — find the line for our
// archive specifically.
function findChecksum(checksumsText, archiveName) {
  for (const line of checksumsText.split("\n")) {
    const trimmed = line.trim();
    if (!trimmed) continue;
    const [digest, ...rest] = trimmed.split(/\s+/);
    const filename = rest[rest.length - 1];
    if (filename === archiveName) return digest.toLowerCase();
  }
  return null;
}

async function main() {
  if (process.env.KRAAI_SKIP_POSTINSTALL === "1") {
    console.log("kraai: KRAAI_SKIP_POSTINSTALL=1 set, skipping binary download.");
    return;
  }

  const { goos, goarch } = resolvePlatform();
  const { version } = JSON.parse(await readFile(path.join(packageRoot, "package.json"), "utf8"));

  const archiveName = `kraai_${version}_${goos}_${goarch}.tar.gz`;
  const releaseBase = `https://github.com/evatt-labs/kraai/releases/download/v${version}`;
  const url = `${releaseBase}/${archiveName}`;
  const checksumsUrl = `${releaseBase}/checksums.txt`;

  const tmpArchive = path.join(tmpdir(), `kraai-install-${process.pid}.tar.gz`);

  console.log(`kraai: downloading ${url}`);
  const response = await download(url);
  await new Promise((resolve, reject) => {
    const file = createWriteStream(tmpArchive);
    response.pipe(file);
    file.on("finish", resolve);
    file.on("error", reject);
  });

  // Verify integrity against the release's cosign-signed checksums.txt
  // (see .goreleaser.yaml's checksum/signs config) before extracting or
  // running anything from the archive — never trust an unverified download.
  console.log(`kraai: verifying checksum against ${checksumsUrl}`);
  const checksumsText = (await downloadToBuffer(checksumsUrl)).toString("utf8");
  const expectedDigest = findChecksum(checksumsText, archiveName);
  if (!expectedDigest) {
    await rm(tmpArchive, { force: true });
    throw new Error(`checksums.txt has no entry for ${archiveName} — refusing to install an unverified binary.`);
  }

  const actualDigest = await sha256File(tmpArchive);
  if (actualDigest !== expectedDigest) {
    await rm(tmpArchive, { force: true });
    throw new Error(
      `checksum mismatch for ${archiveName}: expected ${expectedDigest}, got ${actualDigest}. ` +
        "Refusing to install a binary that doesn't match the signed release checksums.",
    );
  }

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
