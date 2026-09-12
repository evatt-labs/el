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

// --- D1 --------------------------------------------------------------------

export async function createD1Database(token, accountId, name) {
  const result = await cfFetch(token, `/accounts/${accountId}/d1/database`, {
    method: "POST",
    body: JSON.stringify({ name }),
  });
  return result.uuid;
}

export async function findD1DatabaseByName(token, accountId, name) {
  const databases = await cfFetch(token, `/accounts/${accountId}/d1/database`);
  return databases.find((d) => d.name === name) ?? null;
}

export async function deleteD1Database(token, accountId, databaseId) {
  await cfFetch(token, `/accounts/${accountId}/d1/database/${databaseId}`, { method: "DELETE" });
}

// --- KV ----------------------------------------------------------------------

export async function createKvNamespace(token, accountId, title) {
  const result = await cfFetch(token, `/accounts/${accountId}/storage/kv/namespaces`, {
    method: "POST",
    body: JSON.stringify({ title }),
  });
  return result.id;
}

export async function findKvNamespaceByTitle(token, accountId, title) {
  const namespaces = await cfFetch(token, `/accounts/${accountId}/storage/kv/namespaces`);
  return namespaces.find((ns) => ns.title === title) ?? null;
}

export async function deleteKvNamespace(token, accountId, namespaceId) {
  await cfFetch(token, `/accounts/${accountId}/storage/kv/namespaces/${namespaceId}`, {
    method: "DELETE",
  });
}

// --- R2 ------------------------------------------------------------------
// R2 buckets are referenced by name directly — there is no separate id, so
// no find-by-name lookup is needed before delete the way D1/KV/Queues need.

export async function createR2Bucket(token, accountId, name) {
  const result = await cfFetch(token, `/accounts/${accountId}/r2/buckets`, {
    method: "POST",
    body: JSON.stringify({ name }),
  });
  return result.name;
}

/**
 * R2 refuses to delete a non-empty bucket. An ephemeral bucket only exists
 * for the environment's lifetime, but the preview app itself may have
 * written objects to it during testing — so this empties it first rather
 * than assuming it's already empty. Best-effort: a bucket that fails to
 * empty is reported, not silently left behind.
 */
export async function deleteR2Bucket(token, accountId, name) {
  let cursor;
  do {
    const page = await cfFetch(
      token,
      `/accounts/${accountId}/r2/buckets/${name}/objects${cursor ? `?cursor=${cursor}` : ""}`,
    );
    for (const object of page.objects ?? []) {
      await cfFetch(token, `/accounts/${accountId}/r2/buckets/${name}/objects/${encodeURIComponent(object.key)}`, {
        method: "DELETE",
      });
    }
    cursor = page.cursor;
  } while (cursor);

  await cfFetch(token, `/accounts/${accountId}/r2/buckets/${name}`, { method: "DELETE" });
}

// --- Queues ------------------------------------------------------------------

export async function createQueue(token, accountId, name) {
  const result = await cfFetch(token, `/accounts/${accountId}/queues`, {
    method: "POST",
    body: JSON.stringify({ queue_name: name }),
  });
  return result.queue_id;
}

export async function findQueueByName(token, accountId, name) {
  const queues = await cfFetch(token, `/accounts/${accountId}/queues`);
  return queues.find((q) => q.queue_name === name) ?? null;
}

export async function deleteQueue(token, accountId, queueId) {
  await cfFetch(token, `/accounts/${accountId}/queues/${queueId}`, { method: "DELETE" });
}
