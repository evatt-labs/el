// Thin wrapper over the Neon management API — just what up.mjs/down.mjs need.

const BASE = "https://console.neon.tech/api/v2";

async function neonFetch(apiKey, urlPath, options = {}) {
  const response = await fetch(`${BASE}${urlPath}`, {
    ...options,
    headers: {
      Authorization: `Bearer ${apiKey}`,
      "Content-Type": "application/json",
      ...options.headers,
    },
  });
  const body = await response.json();
  if (!response.ok) {
    throw new Error(`Neon API ${String(response.status)} on ${urlPath}: ${JSON.stringify(body)}`);
  }
  return body;
}

/**
 * Every Neon account now has at least one organization, and /projects
 * (unlike every project-scoped endpoint below it) refuses to list anything
 * without an org_id, confirmed empirically: it 400s with "org_id is
 * required" otherwise. `orgId` is optional here so a caller that already
 * knows it (the Neon provider resolves it once via resolveOrgId, see
 * providers/neon.mjs) can skip the lookup this function would otherwise
 * need to do itself.
 */
export async function findProjectByName(apiKey, name, orgId) {
  const query = orgId ? `?org_id=${encodeURIComponent(orgId)}` : "";
  const { projects } = await neonFetch(apiKey, `/projects${query}`);
  const project = projects.find((p) => p.name === name);
  if (!project) throw new Error(`No Neon project named "${name}" found`);
  return project;
}

/** Lists every organization this API key's account belongs to. */
export async function findOrganizations(apiKey) {
  const { organizations } = await neonFetch(apiKey, "/users/me/organizations");
  return organizations;
}

export async function findDefaultBranch(apiKey, projectId) {
  const { branches } = await neonFetch(apiKey, `/projects/${projectId}/branches`);
  const branch = branches.find((b) => b.default);
  if (!branch) throw new Error(`Project ${projectId} has no default branch`);
  return branch;
}

export async function findBranchByName(apiKey, projectId, name) {
  const { branches } = await neonFetch(apiKey, `/projects/${projectId}/branches`);
  return branches.find((b) => b.name === name) ?? null;
}

export async function createBranch(apiKey, projectId, parentId, name) {
  // A branch with no `endpoints` gets no compute attached — storage-only,
  // no connection_uri, no ability to actually query it.
  const { branch } = await neonFetch(apiKey, `/projects/${projectId}/branches`, {
    method: "POST",
    body: JSON.stringify({
      branch: { name, parent_id: parentId },
      endpoints: [{ type: "read_write" }],
    }),
  });
  return branch;
}

export async function deleteBranch(apiKey, projectId, branchId) {
  await neonFetch(apiKey, `/projects/${projectId}/branches/${branchId}`, {
    method: "DELETE",
  });
}

export async function getConnectionUri(apiKey, projectId, { branchId, database, role }) {
  const query = new URLSearchParams({
    branch_id: branchId,
    database_name: database,
    role_name: role,
    pooled: "false",
  });
  const { uri } = await neonFetch(
    apiKey,
    `/projects/${projectId}/connection_uri?${query.toString()}`,
  );
  return uri;
}
