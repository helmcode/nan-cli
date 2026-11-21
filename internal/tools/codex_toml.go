package tools

import (
	"reflect"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
)

// config.toml as Codex reads it. Not ok when it is not valid TOML: the
// callers then leave the file exactly as it is, because Codex refuses it
// already and an edit made against a misreading could only make it worse.
func codexTOML(content string) (map[string]any, bool) {
	doc := map[string]any{}
	if err := toml.Unmarshal([]byte(content), &doc); err != nil {
		return nil, false
	}
	return doc, true
}

// Whether the edited file means exactly want. Every edit to the member's
// config.toml goes through here: lines are found by reading them one at a
// time, which a multi-line string can fool, so the result is parsed again and
// compared with what the edit was meant to do. Anything else and the edit is
// dropped.
func codexEditMeans(edited string, want map[string]any) bool {
	got, ok := codexTOML(edited)
	return ok && reflect.DeepEqual(got, want)
}

// One line read on its own as TOML: the key it assigns and the value, when it
// is a single key = value. Quoting and trailing comments are TOML's to
// resolve, so `"model_provider" = 'nan' # x` reads as model_provider, "nan".
// A dotted key (a.b = 1) reads as its first part with a table for a value,
// which no caller mistakes for a string.
func codexLine(line string) (key string, value any, ok bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "#") {
		return "", nil, false
	}
	doc, parsed := codexTOML(trimmed)
	if !parsed || len(doc) != 1 {
		return "", nil, false
	}
	for k, v := range doc {
		return k, v, true
	}
	return "", nil, false
}

// The table a header line opens, as TOML reads it: `[agents] # c`,
// `[ agents ]` and `["agents"]` are all "agents", `[model_providers.nan]` is
// "model_providers.nan". Not ok for anything that is not a [table] header,
// array-of-tables headers included.
func codexTablePath(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "[[") {
		return "", false
	}
	doc, ok := codexTOML(trimmed)
	if !ok {
		return "", false
	}
	var path []string
	for len(doc) == 1 {
		var next map[string]any
		for k, v := range doc {
			path = append(path, k)
			next, _ = v.(map[string]any)
		}
		if next == nil {
			return "", false
		}
		doc = next
	}
	if len(doc) != 0 || len(path) == 0 {
		return "", false
	}
	return strings.Join(path, "."), true
}

// Whether the line opens a table: a TOML-valid [table] header, or an
// [[array-of-tables]] one. A line that merely starts with "[" - the middle of
// a multi-line array - is not.
func codexIsHeader(line string) bool {
	if _, ok := codexTablePath(line); ok {
		return true
	}
	return strings.HasPrefix(strings.TrimSpace(line), "[[")
}

// Index of the first table header: everything above it is the root table.
func codexRootEnd(lines []string) int {
	for i, line := range lines {
		if codexIsHeader(line) {
			return i
		}
	}
	return len(lines)
}

// Index of the [table] header line, or -1 when there is none.
func codexTableHeader(lines []string, table string) int {
	for i, line := range lines {
		if path, ok := codexTablePath(line); ok && path == table {
			return i
		}
	}
	return -1
}

// Takes `key = value` out of the [table] block, and the block's header too
// when nothing but blank lines is left under it. want is updated to match,
// so the caller can verify the whole edit at the end. Lines are returned
// unchanged when the table has no such line of its own (an inline table, a
// dotted key at the root, or a different value).
func codexTableKeyDropped(lines []string, want map[string]any, table, key string, value any) []string {
	start := codexTableHeader(lines, table)
	if start < 0 {
		return lines
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if codexIsHeader(lines[i]) {
			end = i
			break
		}
	}
	at := -1
	for i := start + 1; i < end; i++ {
		if k, v, ok := codexLine(lines[i]); ok && k == key && reflect.DeepEqual(v, value) {
			at = i
			break
		}
	}
	if at < 0 {
		return lines
	}
	tbl, _ := want[table].(map[string]any)
	delete(tbl, key)
	empty := true
	for i := start + 1; i < end; i++ {
		if i != at && strings.TrimSpace(lines[i]) != "" {
			empty = false
		}
	}
	out := append([]string{}, lines[:start]...)
	if empty && len(tbl) == 0 {
		delete(want, table)
		// The blank line that separated the block from what came before goes
		// with it, so no double gap is left.
		for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
			out = out[:len(out)-1]
		}
		if end < len(lines) && len(out) > 0 {
			out = append(out, "")
		}
	} else {
		out = append(out, lines[start:at]...)
		out = append(out, lines[at+1:end]...)
	}
	return append(out, lines[end:]...)
}

// Takes `key = value` out of the root table (above the first header), and
// deletes it from want. Unchanged when the root has no such line of its own.
func codexRootKeyDropped(lines []string, want map[string]any, key string, value any) []string {
	rootEnd := codexRootEnd(lines)
	for i := 0; i < rootEnd; i++ {
		if k, v, ok := codexLine(lines[i]); ok && k == key && reflect.DeepEqual(v, value) {
			delete(want, key)
			return append(append([]string{}, lines[:i]...), lines[i+1:]...)
		}
	}
	return lines
}

// Replaces the string value of `key` in the [table] block with "", keeping
// the line's indentation and every other line as it is. Lines are returned
// unchanged when the table has no such line of its own (an inline table, a
// dotted key at the root, a different value, a multi-line string).
func codexTableKeyBlanked(lines []string, table, key, value string) []string {
	start := codexTableHeader(lines, table)
	if start < 0 {
		return lines
	}
	out := append([]string{}, lines...)
	for i := start + 1; i < len(out) && !codexIsHeader(out[i]); i++ {
		if k, v, ok := codexLine(out[i]); ok && k == key && v == value {
			indent := out[i][:len(out[i])-len(strings.TrimLeft(out[i], " \t"))]
			out[i] = indent + key + ` = ""`
			return out
		}
	}
	return lines
}

// Whether after is before with the [model_providers.nan] table taken out, and
// nothing else changed. An emptied [model_providers] is the same as none.
func codexProviderRemovalMeans(before, after string) bool {
	want, ok := codexTOML(before)
	if !ok {
		return false
	}
	if providers, _ := want["model_providers"].(map[string]any); providers != nil {
		delete(providers, "nan")
	}
	got, ok := codexTOML(after)
	if !ok {
		return false
	}
	for _, doc := range []map[string]any{want, got} {
		if providers, isMap := doc["model_providers"].(map[string]any); isMap && len(providers) == 0 {
			delete(doc, "model_providers")
		}
	}
	return reflect.DeepEqual(got, want)
}

// Whether the two documents differ in model_context_window keys at most: the
// root one and the ones inside [projects.*]. Both parse, or it is false.
func codexOnlyWindowsDiffer(before, after string) bool {
	var docs [2]map[string]any
	for i, content := range []string{before, after} {
		doc, ok := codexTOML(content)
		if !ok {
			return false
		}
		delete(doc, "model_context_window")
		projects, _ := doc["projects"].(map[string]any)
		for _, p := range projects {
			if tbl, isMap := p.(map[string]any); isMap {
				delete(tbl, "model_context_window")
			}
		}
		docs[i] = doc
	}
	return reflect.DeepEqual(docs[0], docs[1])
}
