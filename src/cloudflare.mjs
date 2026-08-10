// Cloudflare REST API. Worker deploy/delete still shell out to wrangler
// (see wrangler.mjs) — there's no good reason to reimplement that — but
// Hyperdrive config management goes straight to the API. `wrangler hyperdrive
// create --connection-string=...` puts the database password in argv, which
// is readable by any other local process via /proc/<pid>/cmdline for the
// call's duration. The REST API sends it once, over TLS, in a request body.

const BASE = "https://api.cloudflare.com/client/v4";

async function cfFetch(token, urlPath, options = {}) {
  const response = await fetch(`${BASE}${urlPath}`, {
    ...options,
    headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
  });
  const body = await response.json();
  if (!response.ok || body.success === false) {
    throw new Error(`Cloudflare API ${String(response.status)} on ${urlPath}: ${JSON.stringify(body)}`);
  }
  return body.result;
}

export async function getWorkersSubdomain(token, accountId) {
  const result = await cfFetch(token, `/accounts/${accountId}/workers/subdomain`);
  return result.subdomain;
}

export async function findHyperdriveConfigByName(token, accountId, name) {
  const configs = await cfFetch(token, `/accounts/${accountId}/hyperdrive/configs`);
  return configs.find((c) => c.name === name) ?? null;
}

export async function deleteHyperdriveConfig(token, accountId, configId) {
  await cfFetch(token, `/accounts/${accountId}/hyperdrive/configs/${configId}`, {
    method: "DELETE",
  });
}

/**
 * Creates a Hyperdrive config from a parsed connection (see
 * connection-uri.mjs), never a raw connection string. On failure, throws a
 * scrubbed error rather than cfFetch's default — Cloudflare's validation
 * error format for this endpoint isn't verified not to echo request fields
 * back, so this doesn't rely on that being true.
 */
export async function createHyperdriveConfig(token, accountId, name, connection) {
  try {
    const result = await cfFetch(token, `/accounts/${accountId}/hyperdrive/configs`, {
      method: "POST",
      body: JSON.stringify({
        name,
        origin: {
          scheme: connection.scheme,
          host: connection.host,
          port: connection.port,
          database: connection.database,
          user: connection.user,
          password: connection.password,
        },
        mtls: { sslmode: connection.sslmode },
      }),
    });
    return result.id;
  } catch {
    throw new Error(
      `Failed to create Hyperdrive config "${name}". The underlying error is not shown here ` +
        `because it may echo request details back — check the Cloudflare dashboard.`,
    );
  }
}
