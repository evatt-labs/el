// Package aws adapts the AWS Cloud Control API to the resource contract.
//
// # One engine, not one client per service
//
// Cloud Control exposes uniform GetResource/CreateResource/UpdateResource/
// DeleteResource/ListResources across 1,598 FULLY_MUTABLE public resource
// types, which maps 1:1 onto internal/resource's per-verb Resource interface
// (verified against the live Evatt Labs account, 2026-09-13 — see
// docs/workstreams.yaml's aws-provider-core findings). So this package is one
// generic Resource implementation, parameterized per registration by its
// CloudFormation TypeName and its identity lookup strategy (D26); adding a
// resource type is a registry entry, never a new client.
//
// # This slice is read-only
//
// Get works fully. Create, Update and Delete return ErrNotImplemented rather
// than stub silently — the diff-to-JSON-Patch engine, async ProgressEvent
// polling, and createOnlyProperties-driven replacement detection the write
// path needs are explicitly out of scope here and belong to a later
// workstream slice.
package aws
