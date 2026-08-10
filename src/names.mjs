// Generates "blue-honey-badger-12345" style environment names.

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

function pick(list) {
  return list[Math.floor(Math.random() * list.length)];
}

export function generateEnvironmentName() {
  const suffix = Math.floor(10_000 + Math.random() * 90_000);
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
 * Builds the name for a resource `el` provisions on behalf of one binding —
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
