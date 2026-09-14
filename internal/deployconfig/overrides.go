package deployconfig

import "github.com/evatt-labs/kraai/internal/wrangler"

// D1Resource is a freshly provisioned D1 database.
type D1Resource struct {
	Name string
	ID   string
}

// Binding names a binding a service declares, and for queues whether it also
// consumes from it.
type Binding struct {
	Binding  string
	Consumer bool
}

// D1Databases builds the d1_databases override for freshly provisioned
// databases, in the order the service declares them.
func D1Databases(bindings []Binding, resources map[string]D1Resource) []any {
	out := make([]any, 0, len(bindings))
	for _, b := range bindings {
		resource := resources[b.Binding]
		out = append(out, map[string]any{
			"binding":       b.Binding,
			"database_name": resource.Name,
			"database_id":   resource.ID,
		})
	}
	return out
}

// KVNamespaces builds the kv_namespaces override.
func KVNamespaces(bindings []Binding, ids map[string]string) []any {
	out := make([]any, 0, len(bindings))
	for _, b := range bindings {
		out = append(out, map[string]any{"binding": b.Binding, "id": ids[b.Binding]})
	}
	return out
}

// R2Buckets builds the r2_buckets override.
func R2Buckets(bindings []Binding, names map[string]string) []any {
	out := make([]any, 0, len(bindings))
	for _, b := range bindings {
		out = append(out, map[string]any{"binding": b.Binding, "bucket_name": names[b.Binding]})
	}
	return out
}

// Queues builds the queues override — producers for every declared binding,
// consumers for the ones the service consumes from.
//
// Consumer settings (batch size, retry policy, dead-letter queue) are copied
// from the committed configuration only when it declares exactly one
// consumer. With more than one, which base consumer belongs to which binding
// is genuinely ambiguous — the consumer entries carry no binding key to match
// on — so this falls back to wrangler's own defaults rather than guessing and
// silently applying one queue's retry policy to another.
func Queues(base wrangler.Config, bindings []Binding, names map[string]string) map[string]any {
	producers := make([]any, 0, len(bindings))
	for _, b := range bindings {
		producers = append(producers, map[string]any{"binding": b.Binding, "queue": names[b.Binding]})
	}

	settings := singleConsumerSettings(base)
	consumers := make([]any, 0, len(bindings))
	for _, b := range bindings {
		if !b.Consumer {
			continue
		}
		consumer := map[string]any{}
		for k, v := range settings {
			consumer[k] = v
		}
		consumer["queue"] = names[b.Binding]
		consumers = append(consumers, consumer)
	}

	return map[string]any{"producers": producers, "consumers": consumers}
}

// singleConsumerSettings returns the committed configuration's consumer
// settings when there is exactly one, and nothing otherwise.
func singleConsumerSettings(base wrangler.Config) map[string]any {
	queues, ok := base["queues"].(map[string]any)
	if !ok {
		return nil
	}
	consumers, ok := queues["consumers"].([]any)
	if !ok || len(consumers) != 1 {
		return nil
	}
	settings, ok := consumers[0].(map[string]any)
	if !ok {
		return nil
	}
	return settings
}
