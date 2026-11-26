package workflow

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var placeholder = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_.]*)\}`)

// Render substitutes "{field}" / "{a.b}" placeholders with values from vars.
// Missing values are an error so that a typo never silently collapses keys.
func Render(tmpl string, vars map[string]any) (string, error) {
	var missing []string
	out := placeholder.ReplaceAllStringFunc(tmpl, func(m string) string {
		path := m[1 : len(m)-1]
		v, ok := lookup(vars, path)
		if !ok {
			missing = append(missing, path)
			return m
		}
		return format(v)
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("template %q: unknown field(s) %s", tmpl, strings.Join(missing, ", "))
	}
	return out, nil
}

// RenderValue renders v if it is a string template; other values pass through.
// A string that is exactly one placeholder keeps the referenced value's type.
func RenderValue(v any, vars map[string]any) (any, error) {
	s, ok := v.(string)
	if !ok {
		return v, nil
	}
	if m := placeholder.FindStringSubmatch(s); m != nil && m[0] == s {
		val, ok := lookup(vars, m[1])
		if !ok {
			return nil, fmt.Errorf("template %q: unknown field %s", s, m[1])
		}
		return val, nil
	}
	return Render(s, vars)
}

func lookup(vars map[string]any, path string) (any, bool) {
	var cur any = vars
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[part]; !ok {
			return nil, false
		}
	}
	return cur, true
}

func format(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	case float64, bool, json.Number:
		return fmt.Sprint(t)
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}

// MatchWhere reports whether payload satisfies every field of where
// (equality; a list in where means "any of").
func MatchWhere(where map[string]any, payload map[string]any) bool {
	for k, want := range where {
		have, ok := lookup(payload, k)
		if !ok {
			return false
		}
		if options, isList := want.([]any); isList {
			matched := false
			for _, o := range options {
				if equal(o, have) {
					matched = true
					break
				}
			}
			if !matched {
				return false
			}
			continue
		}
		if !equal(want, have) {
			return false
		}
	}
	return true
}

func equal(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}
