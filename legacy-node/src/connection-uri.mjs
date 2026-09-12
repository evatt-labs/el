// Parses a Postgres connection URI into its parts once, so nothing else in
// the codebase re-derives them — and, more importantly, so nothing else
// needs to pass the URI itself around as a single opaque string that ends up
// somewhere it shouldn't (argv, an error message, a log line).

export function parseConnectionUri(uri) {
  const url = new URL(uri);
  return {
    scheme: url.protocol.replace(":", ""),
    host: url.hostname,
    port: url.port === "" ? 5432 : Number(url.port),
    user: decodeURIComponent(url.username),
    password: decodeURIComponent(url.password),
    database: url.pathname.replace(/^\//, ""),
    sslmode: url.searchParams.get("sslmode") ?? "require",
  };
}
