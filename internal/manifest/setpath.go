package manifest

import (
	"strconv"
	"strings"

	"github.com/evatt-labs/kraai/internal/kerrors"
)

// setStep is one step of a --set path: either a map key or (when IsIndex is
// true) a slice index. "a.b[0][1].c" parses to four steps: {Key:"a"},
// {Key:"b"}, {IsIndex:true,Index:0}, {IsIndex:true,Index:1}, {Key:"c"} —
// bracket groups are their own steps so nested lists compose without a
// separate "index-of-index" case.
type setStep struct {
	Key     string
	Index   int
	IsIndex bool
}

// setPath is a parsed --set left-hand side.
type setPath []setStep

// setAssignment is one fully-parsed "path=value" from a --set flag, with
// value already type-inferred per inferSetValue.
type setAssignment struct {
	Path  setPath
	Value any
}

// parseSetArgs parses every --set flag's raw string (each may itself
// contain comma-separated assignments, matching Helm's own --set grammar)
// into setAssignments, in the order given — later assignments (whether
// from the same flag or a later --set) win on conflicting paths, applied
// left to right by ApplySet's caller.
func parseSetArgs(args []string) ([]setAssignment, error) {
	var out []setAssignment
	for _, arg := range args {
		for _, raw := range splitUnescaped(arg, ',') {
			if raw == "" {
				return nil, kerrors.Validation("--set: empty assignment in %q", arg)
			}
			assignment, err := parseSetAssignment(raw)
			if err != nil {
				return nil, err
			}
			out = append(out, assignment)
		}
	}
	return out, nil
}

func parseSetAssignment(raw string) (setAssignment, error) {
	pathStr, valueStr, ok := splitFirstUnescaped(raw, '=')
	if !ok {
		return setAssignment{}, kerrors.Validation("--set: %q is missing '=' (expected path=value)", raw)
	}

	path, err := parseSetPath(pathStr)
	if err != nil {
		return setAssignment{}, err
	}

	return setAssignment{Path: path, Value: inferSetValue(valueStr)}, nil
}

// parseSetPath parses a --set left-hand side, dotted-path with optional
// bracket indices (Helm's own dotted-path convention).
func parseSetPath(pathStr string) (setPath, error) {
	if pathStr == "" {
		return nil, kerrors.Validation("--set: empty path")
	}

	var path setPath
	for _, segment := range splitUnescaped(pathStr, '.') {
		steps, err := parseSetSegment(segment)
		if err != nil {
			return nil, kerrors.Validation("--set: %q: %v", pathStr, err)
		}
		path = append(path, steps...)
	}
	return path, nil
}

// parseSetSegment parses one dot-separated segment, e.g. "databases[0]",
// into a key step followed by zero or more index steps.
func parseSetSegment(segment string) ([]setStep, error) {
	key, rest := segment, ""
	if bracket := strings.IndexByte(segment, '['); bracket >= 0 {
		key, rest = segment[:bracket], segment[bracket:]
	}
	if key == "" {
		return nil, kerrors.New("manifest: path segment %q has no key", segment)
	}
	steps := []setStep{{Key: key}}

	for len(rest) > 0 {
		if rest[0] != '[' {
			return nil, kerrors.New("manifest: malformed index near %q", rest)
		}
		end := strings.IndexByte(rest, ']')
		if end < 0 {
			return nil, kerrors.New("manifest: unterminated index in %q", segment)
		}
		idxStr := rest[1:end]
		idx, err := strconv.Atoi(idxStr)
		if err != nil || idx < 0 {
			return nil, kerrors.New("manifest: invalid index %q in %q", idxStr, segment)
		}
		steps = append(steps, setStep{IsIndex: true, Index: idx})
		rest = rest[end+1:]
	}
	return steps, nil
}

// inferSetValue mirrors Helm's --set (not --set-string) type inference:
// true/false, null/~, integers and floats convert; everything else stays a
// string.
func inferSetValue(s string) any {
	switch s {
	case "true":
		return true
	case "false":
		return false
	case "null", "~":
		return nil
	}
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return i
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}
	return s
}

// setPathValue returns a new tree with value set at path within root,
// copy-on-write at every level it touches so the caller's original value is
// never mutated in place. root is untyped because it recurses through
// mixed map[string]any / []any / scalar layers as path descends.
func setPathValue(root any, path setPath, value any) (any, error) {
	if len(path) == 0 {
		return value, nil
	}

	step := path[0]
	rest := path[1:]

	if step.IsIndex {
		return setIndexValue(root, step.Index, rest, value)
	}
	return setKeyValue(root, step.Key, rest, value)
}

func setKeyValue(root any, key string, rest setPath, value any) (any, error) {
	var existing map[string]any
	switch v := root.(type) {
	case nil:
		existing = nil
	case map[string]any:
		existing = v
	default:
		return nil, kerrors.Validation("--set: cannot set key %q: existing value is not an object", key)
	}

	next := make(map[string]any, len(existing)+1)
	for k, v := range existing {
		next[k] = v
	}

	child, err := setPathValue(next[key], rest, value)
	if err != nil {
		return nil, err
	}
	next[key] = child
	return next, nil
}

func setIndexValue(root any, index int, rest setPath, value any) (any, error) {
	var existing []any
	switch v := root.(type) {
	case nil:
		existing = nil
	case []any:
		existing = v
	default:
		return nil, kerrors.Validation("--set: cannot set index [%d]: existing value is not a list", index)
	}

	next := make([]any, len(existing))
	copy(next, existing)
	for len(next) <= index {
		next = append(next, nil)
	}

	child, err := setPathValue(next[index], rest, value)
	if err != nil {
		return nil, err
	}
	next[index] = child
	return next, nil
}

// splitUnescaped splits s on every unescaped occurrence of sep. "\<sep>"
// and "\\" are the only recognized escapes (each collapses to the single
// character it precedes); a backslash before anything else is kept
// literally, so values rarely need escaping at all.
func splitUnescaped(s string, sep byte) []string {
	var parts []string
	var cur strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' && i+1 < len(s) && (s[i+1] == sep || s[i+1] == '\\') {
			cur.WriteByte(s[i+1])
			i++
			continue
		}
		if c == sep {
			parts = append(parts, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteByte(c)
	}
	parts = append(parts, cur.String())
	return parts
}

// splitFirstUnescaped splits s at the first unescaped occurrence of sep,
// returning ok=false if sep never occurs unescaped.
func splitFirstUnescaped(s string, sep byte) (before, after string, ok bool) {
	var cur strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' && i+1 < len(s) && (s[i+1] == sep || s[i+1] == '\\') {
			cur.WriteByte(s[i+1])
			i++
			continue
		}
		if c == sep {
			return cur.String(), s[i+1:], true
		}
		cur.WriteByte(c)
	}
	return cur.String(), "", false
}
