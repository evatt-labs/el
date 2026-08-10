// Cloudflare REST API for the pieces wrangler's CLI doesn't expose as JSON
// (Hyperdrive list/delete). Worker deploy/delete and Hyperdrive create still
// shell out to wrangler itself — see wrangler.mjs.

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
