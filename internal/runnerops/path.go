/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package pulumido

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// pathSegment is one step of a dot path: a map key or an array index.
type pathSegment struct {
	key   string
	index int
	isIdx bool
}

// parsePath splits a path like `a.b[2].c["dotted.key"]` into segments.
// Bare keys are separated by dots; array indexes use [n]; keys containing
// '.', '[', ']' or '"' use bracket-quoted form ["..."] with backslash
// escapes for '"' and '\'.
func parsePath(path string) ([]pathSegment, error) {
	var segs []pathSegment
	i, n := 0, len(path)
	for i < n {
		if len(segs) > 0 {
			switch path[i] {
			case '.':
				i++
			case '[':
				// bracket segment follows directly
			default:
				return nil, fmt.Errorf("path %q: unexpected character %q at %d", path, path[i], i)
			}
		}
		if i >= n {
			return nil, fmt.Errorf("path %q ends with a separator", path)
		}
		if path[i] == '[' {
			seg, next, err := parseBracket(path, i)
			if err != nil {
				return nil, err
			}
			segs = append(segs, seg)
			i = next
			continue
		}
		start := i
		for i < n && path[i] != '.' && path[i] != '[' {
			i++
		}
		if i == start {
			return nil, fmt.Errorf("path %q contains an empty segment at %d", path, start)
		}
		segs = append(segs, pathSegment{key: path[start:i]})
	}
	if len(segs) == 0 {
		return nil, fmt.Errorf("path %q is empty", path)
	}
	return segs, nil
}

// parseBracket parses either [<digits>] or ["quoted"] starting at path[i]
// (which must be '['), returning the segment and the index just past ']'.
func parseBracket(path string, i int) (pathSegment, int, error) {
	n := len(path)
	i++ // consume '['
	if i < n && path[i] == '"' {
		i++
		var b strings.Builder
		for i < n && path[i] != '"' {
			if path[i] == '\\' && i+1 < n {
				i++
			}
			b.WriteByte(path[i])
			i++
		}
		if i >= n {
			return pathSegment{}, 0, fmt.Errorf("path %q: unterminated quoted segment", path)
		}
		i++ // consume closing '"'
		if i >= n || path[i] != ']' {
			return pathSegment{}, 0, fmt.Errorf("path %q: missing ] after quoted segment", path)
		}
		return pathSegment{key: b.String()}, i + 1, nil
	}
	end := strings.IndexByte(path[i:], ']')
	if end < 0 {
		return pathSegment{}, 0, fmt.Errorf("path %q has unbalanced brackets", path)
	}
	idx, err := strconv.Atoi(path[i : i+end])
	if err != nil || idx < 0 {
		return pathSegment{}, 0, fmt.Errorf("path %q has an invalid index %q", path, path[i:i+end])
	}
	return pathSegment{index: idx, isIdx: true}, i + end + 1, nil
}

// AppendKeySegment appends a map key to a dot path, using bracket-quoted
// form when the key contains path metacharacters.
func AppendKeySegment(path, key string) string {
	if key != "" && !strings.ContainsAny(key, `.[]"\`) {
		if path == "" {
			return key
		}
		return path + "." + key
	}
	escaped := strings.ReplaceAll(key, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return path + `["` + escaped + `"]`
}

// SetPath sets value at a dot path inside props, creating intermediate maps
// as needed. Array segments must address existing elements.
func SetPath(props map[string]any, path string, value any) error {
	segs, err := parsePath(path)
	if err != nil {
		return err
	}
	if segs[0].isIdx {
		return fmt.Errorf("path %q must start with a key", path)
	}
	var cur any = props
	for i, seg := range segs {
		last := i == len(segs)-1
		switch node := cur.(type) {
		case map[string]any:
			if seg.isIdx {
				return fmt.Errorf("path %q: index applied to object", path)
			}
			if last {
				node[seg.key] = value
				return nil
			}
			next, ok := node[seg.key]
			if !ok || next == nil {
				if segs[i+1].isIdx {
					return fmt.Errorf("path %q: cannot create array element %q", path, seg.key)
				}
				m := map[string]any{}
				node[seg.key] = m
				cur = m
				continue
			}
			cur = next
		case []any:
			if !seg.isIdx {
				return fmt.Errorf("path %q: key %q applied to array", path, seg.key)
			}
			if seg.index >= len(node) {
				return fmt.Errorf("path %q: index %d out of range (len %d)", path, seg.index, len(node))
			}
			if last {
				node[seg.index] = value
				return nil
			}
			cur = node[seg.index]
		default:
			return fmt.Errorf("path %q: cannot descend into %s", path, jsonKind(cur))
		}
	}
	return nil
}

// GetPath reads the value at a dot path inside v. The boolean reports
// whether the full path exists.
func GetPath(v any, path string) (any, bool) {
	segs, err := parsePath(path)
	if err != nil {
		return nil, false
	}
	cur := v
	for _, seg := range segs {
		switch node := cur.(type) {
		case map[string]any:
			if seg.isIdx {
				return nil, false
			}
			next, ok := node[seg.key]
			if !ok {
				return nil, false
			}
			cur = next
		case []any:
			if !seg.isIdx || seg.index >= len(node) {
				return nil, false
			}
			cur = node[seg.index]
		default:
			return nil, false
		}
	}
	return cur, true
}

// RenderScalar renders a resolved value for use inside a string template:
// scalars use their natural string form, composites compact JSON.
func RenderScalar(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case json.Number:
		return t.String()
	case bool:
		return strconv.FormatBool(t)
	case float64:
		raw, _ := json.Marshal(t)
		return string(raw)
	default:
		raw, err := json.Marshal(t)
		if err != nil {
			return fmt.Sprintf("%v", t)
		}
		return string(raw)
	}
}
