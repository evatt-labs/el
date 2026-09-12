// Generates "blue-honey-badger-12345" style environment names.

import { randomInt } from "node:crypto";

const COLORS = [
  "blue", "red", "green", "amber", "violet", "coral", "teal", "slate",
  "crimson", "azure", "olive", "copper", "indigo", "scarlet", "jade",
];

const ADJECTIVES = [
  "honey", "quiet", "swift", "lucky", "brave", "clever", "stormy", "lazy",
  "wild", "gentle", "rusty", "frosty", "sunny", "shadow", "rowdy",
];

const ANIMALS = [
  "badger", "otter", "falcon", "heron", "lynx", "raven", "marten", "wombat",
  "gecko", "puffin", "jackal", "bison", "civet", "tapir", "kestrel",
];

// randomInt rather than Math.random: the name ends up in a workers.dev
// hostname and, since the lockfile, in a path on disk. Neither is a secret
// and predictability of the name is not a security property here, but a
// CSPRNG costs nothing, and it keeps CodeQL's insecure-randomness rule
// from flagging every sink the name flows into.
function pick(list) {
  return list[randomInt(list.length)];
}

export function generateEnvironmentName() {
  const suffix = randomInt(10_000, 100_000);
  return `${pick(COLORS)}-${pick(ADJECTIVES)}-${pick(ANIMALS)}-${String(suffix)}`;
}

// Bounded word lengths (2-15): unbounded `[a-z]+` accepts arbitrarily long
// names that would fail downstream at Neon/Cloudflare anyway, but there's no
// reason to let something that long reach those APIs in the first place.
const NAME_PATTERN = /^[a-z]{2,15}-[a-z]{2,15}-[a-z]{2,15}-\d{5}$/;

export function isValidEnvironmentName(name) {
  return NAME_PATTERN.test(name);
}

/**
 * Builds the name for a resource `kraai` provisions on behalf of one binding —
 * `{env}-{serviceKey}-{binding}`, lowercased and hyphenated. R2 bucket names
 * specifically must be lowercase, DNS-compliant, and 63 characters or fewer;
 * this satisfies that for every resource type rather than having per-type
 * naming rules drift apart, since a binding name like `MY_QUEUE` is common
 * and would otherwise produce an invalid bucket name.
 */
export function resourceName(environmentName, serviceKey, binding) {
  const slug = binding.toLowerCase().replaceAll(/[^a-z0-9]+/g, "-").replaceAll(/^-+|-+$/g, "");
  const name = `${environmentName}-${serviceKey}-${slug}`;
  return name.length <= 63 ? name : name.slice(0, 63).replace(/-+$/, "");
}

/**
 * Builds a deterministic environment name for a pull request: one name per
 * (repo, PR number) pair, so the GitHub Action's re-run strategy (down then
 * up on every push) always targets the same environment instead of
 * generating a fresh random one every run and orphaning the last one.
 *
 * `repoName` is reduced to its letters only (lowercased, everything else
 * stripped), then clamped to the 2-15 character word length NAME_PATTERN
 * requires: truncated if longer, padded with "x" if shorter (an
 * empty-after-strip repo name becomes "xx" rather than failing).
 *
 * `prNumber` is zero-padded to NAME_PATTERN's fixed 5 digits. It is not
 * truncated or wrapped if it doesn't fit: a PR number that large would
 * silently collide with a different PR's name, so this throws instead.
 */
export function environmentNameForPullRequest(repoName, prNumber) {
  if (!Number.isInteger(prNumber) || prNumber < 1 || prNumber > 99999) {
    throw new Error(
      `Pull request number must be a positive integer no greater than 99999 (got ${JSON.stringify(prNumber)}). ` +
        "The numeric suffix of an environment name is exactly 5 digits, so it can't be truncated or wrapped.",
    );
  }

  let repoWord = repoName.toLowerCase().replaceAll(/[^a-z]/g, "");
  if (repoWord.length > 15) repoWord = repoWord.slice(0, 15);
  while (repoWord.length < 2) repoWord += "x";

  const padded = String(prNumber).padStart(5, "0");
  const name = `${repoWord}-pull-request-${padded}`;

  // Defensive: the construction above should always satisfy NAME_PATTERN,
  // but assert it rather than silently handing back a name kraai's own
  // validator would reject a moment later inside up()/down().
  if (!isValidEnvironmentName(name)) {
    throw new Error(`Generated name "${name}" is not a valid environment name (this is a bug in kraai).`);
  }
  return name;
}
