// Best-effort delete helper shared by teardown paths (down.mjs and any
// database provider's down()). Reports a failure but never stops the rest
// of teardown. Extracted here rather than duplicated because both callers
// need identical semantics: a single flaky resource must not abort deletion
// of everything else `kraai down` is responsible for.

export async function tryDelete(label, fn) {
  try {
    await fn();
  } catch (error) {
    console.warn(`  (${label} failed, continuing)`, String(error.message ?? error));
  }
}
