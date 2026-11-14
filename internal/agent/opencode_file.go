package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// opencodeConfigName is the OpenCode project config every agent workspace
// carries (SPEC §4).
const opencodeConfigName = "opencode.jsonc"

// opencodeEngineDoc is the slice of opencode.jsonc that runtime config
// cares about. The agent definition blobs stay raw so unrelated shapes never
// break parsing.
type opencodeEngineDoc struct {
	Model        string          `json:"model"`
	DefaultAgent string          `json:"default_agent"`
	Agent        json.RawMessage `json:"agent"`
	Agents       json.RawMessage `json:"agents"`
}

// readOpencodeConfig reads <workspace>/opencode.jsonc and refuses symlinks.
func readOpencodeConfig(workspace string) (string, error) {
	path := filepath.Join(workspace, opencodeConfigName)
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("workspace: %s not found; the agent workspace is not scaffolded", opencodeConfigName)
		}
		return "", fmt.Errorf("workspace: stat %s: %w", opencodeConfigName, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("workspace: refusing symlinked %s", opencodeConfigName)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("workspace: %s is not a regular file", opencodeConfigName)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("workspace: read %s: %w", opencodeConfigName, err)
	}
	return string(raw), nil
}

// writeOpencodeConfig replaces <workspace>/opencode.jsonc atomically: a temp
// file in the same directory is written, fsynced, and renamed over the
// target. Symlinks are refused so the rename can never redirect the write.
func writeOpencodeConfig(workspace, content string) error {
	path := filepath.Join(workspace, opencodeConfigName)
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("workspace: stat %s: %w", opencodeConfigName, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("workspace: refusing symlinked %s", opencodeConfigName)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("workspace: %s is not a regular file", opencodeConfigName)
	}
	return atomicWriteFile(path, content, info.Mode().Perm())
}

// restoreOpencodeConfig recreates <workspace>/opencode.jsonc with the given
// content after a failed edit. Unlike writeOpencodeConfig it tolerates the
// editor having deleted the file or replaced it: symlinks and other
// non-directory entries are removed (the mode of a regular file is kept), an
// unexpected directory is preserved under a unique sibling name, and the
// file is recreated with a temp file + fsync + rename. Mode 0644 is the
// fallback when no regular file existed.
func restoreOpencodeConfig(workspace, content string) error {
	path := filepath.Join(workspace, opencodeConfigName)
	mode := os.FileMode(0644)
	info, err := os.Lstat(path)
	switch {
	case err == nil:
		if info.IsDir() {
			if err := preserveConflictingDir(path); err != nil {
				return err
			}
			break
		}
		if info.Mode().IsRegular() {
			mode = info.Mode().Perm()
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("workspace: remove %s: %w", opencodeConfigName, err)
		}
	case errors.Is(err, os.ErrNotExist):
		// Recreate from scratch with the fallback mode.
	default:
		return fmt.Errorf("workspace: stat %s: %w", opencodeConfigName, err)
	}
	return atomicWriteFile(path, content, mode)
}

// preserveConflictingDir moves an unexpected directory standing where the
// config file belongs to a unique sibling path, so restoring the original
// file never destroys what the editor put there. Any rename failure is
// reported instead of deleting the directory.
func preserveConflictingDir(path string) error {
	conflict := fmt.Sprintf("%s.conflict-%d", path, time.Now().UnixNano())
	if err := os.Rename(path, conflict); err != nil {
		return fmt.Errorf("workspace: preserve conflicting directory %s: %w", filepath.Base(path), err)
	}
	return nil
}

// atomicWriteFile writes content to path through a temp file in the same
// directory: temp + fsync + close + chmod + rename, so readers never see a
// partially written file.
func atomicWriteFile(path, content string, mode os.FileMode) error {
	name := filepath.Base(path)
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+name+".tmp-*")
	if err != nil {
		return fmt.Errorf("workspace: create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpPath)
	}
	if _, err := tmp.WriteString(content); err != nil {
		cleanup()
		return fmt.Errorf("workspace: write %s: %w", name, err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("workspace: sync %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("workspace: close temp file: %w", err)
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("workspace: chmod temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("workspace: replace %s: %w", name, err)
	}
	return nil
}

// readEngine returns the model, default agent, and defined agent ids of an
// agent workspace (opencode.jsonc plus .opencode/agents/*.md).
func readEngine(workspace string) (model, agentID string, defined []string, err error) {
	raw, err := readOpencodeConfig(workspace)
	if err != nil {
		return "", "", nil, err
	}
	doc, err := parseEngineDoc(raw)
	if err != nil {
		return "", "", nil, fmt.Errorf("workspace: %s: %w", opencodeConfigName, err)
	}
	defined, err = resolveDefinedAgents(workspace, doc)
	if err != nil {
		return "", "", nil, err
	}
	return doc.Model, doc.DefaultAgent, defined, nil
}

// resolveDefinedAgents lists every OpenCode agent id the workspace can
// reference: the two engine built-ins, entries in opencode.jsonc, and
// .opencode/agents/*.md files.
func resolveDefinedAgents(workspace string, doc opencodeEngineDoc) ([]string, error) {
	seen := map[string]bool{"build": true, "plan": true}
	for _, blob := range []json.RawMessage{doc.Agent, doc.Agents} {
		if len(blob) == 0 {
			continue
		}
		var entries map[string]json.RawMessage
		if err := json.Unmarshal(blob, &entries); err != nil {
			continue // an unusual shape here must not block unrelated edits
		}
		for name := range entries {
			if strings.TrimSpace(name) != "" {
				seen[name] = true
			}
		}
	}
	matches, err := filepath.Glob(filepath.Join(workspace, ".opencode", "agents", "*.md"))
	if err != nil {
		return nil, fmt.Errorf("workspace: list .opencode/agents: %w", err)
	}
	for _, match := range matches {
		name := strings.TrimSuffix(filepath.Base(match), ".md")
		if name != "" {
			seen[name] = true
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	slices.Sort(names)
	return names, nil
}

// validateOpencodeDocument checks that a full opencode.jsonc body parses and
// carries sane model/default_agent values.
func validateOpencodeDocument(workspace, raw string) error {
	doc, err := parseEngineDoc(raw)
	if err != nil {
		return fmt.Errorf("%s is not valid JSONC: %w", opencodeConfigName, err)
	}
	if model := strings.TrimSpace(doc.Model); model != "" && !strings.Contains(model, "/") {
		return fmt.Errorf("%s: model %q must look like provider/model", opencodeConfigName, model)
	}
	if agentID := strings.TrimSpace(doc.DefaultAgent); agentID != "" {
		defined, err := resolveDefinedAgents(workspace, doc)
		if err != nil {
			return err
		}
		if !slices.Contains(defined, agentID) {
			return fmt.Errorf("%s: default_agent %q is not defined (available: %s)",
				opencodeConfigName, agentID, strings.Join(defined, ", "))
		}
	}
	return nil
}

// validateEngineAgent rejects an unknown engine.agent target instead of
// inventing a prompt for it.
func validateEngineAgent(workspace, agentID string) error {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return fmt.Errorf("config: engine.agent must not be empty")
	}
	raw, err := readOpencodeConfig(workspace)
	if err != nil {
		return err
	}
	doc, err := parseEngineDoc(raw)
	if err != nil {
		return fmt.Errorf("workspace: %s: %w", opencodeConfigName, err)
	}
	defined, err := resolveDefinedAgents(workspace, doc)
	if err != nil {
		return err
	}
	if !slices.Contains(defined, agentID) {
		return fmt.Errorf("config: engine.agent %q is not defined (available: %s); define it in %s or .opencode/agents/ first",
			agentID, strings.Join(defined, ", "), opencodeConfigName)
	}
	return nil
}

// parseEngineDoc masks raw JSONC, rejects duplicate top-level keys, and
// decodes the engine fields. JSON decoding silently keeps the last duplicate,
// so an ambiguous document must never be read or edited: a targeted edit of
// the first occurrence would report a change while the parsed value stays the
// other one.
func parseEngineDoc(raw string) (opencodeEngineDoc, error) {
	masked, err := maskJSONC(raw)
	if err != nil {
		return opencodeEngineDoc{}, err
	}
	if key := duplicateTopLevelKey(masked); key != "" {
		return opencodeEngineDoc{}, fmt.Errorf("duplicate top-level key %q (ambiguous document)", key)
	}
	var doc opencodeEngineDoc
	if err := decodeJSONObject(masked, &doc); err != nil {
		return opencodeEngineDoc{}, err
	}
	return doc, nil
}

// EditOpencode opens <workspace>/opencode.jsonc with the provided editor and
// validates the result: it must stay parseable JSONC with sane
// model/default_agent values. Every unsuccessful path — an editor that exits
// with an error, deletes the file, replaces it with a symlink or directory,
// saves invalid content, or leaves an unreadable path — restores the previous
// content, so a bad edit can never leave the agent without a usable config.
func EditOpencode(workspace string, editor func(path string) error) (bool, error) {
	if editor == nil {
		return false, fmt.Errorf("workspace: no editor configured")
	}
	original, err := readOpencodeConfig(workspace)
	if err != nil {
		return false, err
	}
	rollback := func(cause error) (bool, error) {
		if restoreErr := restoreOpencodeConfig(workspace, original); restoreErr != nil {
			return false, fmt.Errorf("%w (restoring the previous %s failed: %v)", cause, opencodeConfigName, restoreErr)
		}
		return false, fmt.Errorf("%w (the previous %s was restored)", cause, opencodeConfigName)
	}

	if err := editor(filepath.Join(workspace, opencodeConfigName)); err != nil {
		return rollback(fmt.Errorf("edit %s: %w", opencodeConfigName, err))
	}
	updated, err := readOpencodeConfig(workspace)
	if err != nil {
		return rollback(fmt.Errorf("edit %s: %w", opencodeConfigName, err))
	}
	if err := validateOpencodeDocument(workspace, updated); err != nil {
		return rollback(err)
	}
	return updated != original, nil
}

// setOpencodeString updates one top-level string field of a JSONC document
// with a targeted text edit, preserving comments, formatting, and unrelated
// keys. A missing key is inserted before the final "}".
func setOpencodeString(raw, key, value string) (string, error) {
	masked, err := maskJSONC(raw)
	if err != nil {
		return "", fmt.Errorf("workspace: %s: %w", opencodeConfigName, err)
	}
	// JSON decoding keeps the last duplicate, so editing the first one would
	// not change the parsed value: reject the ambiguous document instead.
	if count := countTopLevelKey(masked, key); count > 1 {
		return "", fmt.Errorf("workspace: %s: duplicate top-level key %q (%d occurrences, ambiguous document)",
			opencodeConfigName, key, count)
	}
	quoted, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("workspace: quote %s value: %w", key, err)
	}

	valuePattern := regexp.MustCompile(`"` + regexp.QuoteMeta(key) + `"\s*:\s*"(?:[^"\\]|\\.)*"`)
	prefixPattern := regexp.MustCompile(`"` + regexp.QuoteMeta(key) + `"\s*:\s*`)
	for _, loc := range valuePattern.FindAllStringIndex(masked, -1) {
		if jsoncDepth(masked, loc[0]) != 1 {
			continue // nested or inside a string literal
		}
		prefix := prefixPattern.FindStringIndex(masked[loc[0]:loc[1]])
		if prefix == nil {
			continue
		}
		valueStart := loc[0] + prefix[1]
		return raw[:valueStart] + string(quoted) + raw[loc[1]:], nil
	}

	// The key exists at the top level but not with a string value: refuse
	// instead of inserting a duplicate.
	keyPattern := regexp.MustCompile(`"` + regexp.QuoteMeta(key) + `"\s*:`)
	for _, loc := range keyPattern.FindAllStringIndex(masked, -1) {
		if jsoncDepth(masked, loc[0]) == 1 {
			return "", fmt.Errorf("workspace: %s: %q exists but is not a string value", opencodeConfigName, key)
		}
	}
	return insertTopLevelString(raw, key, string(quoted))
}

// setOpencodeInstruction adds or removes exactly one string entry in the
// top-level "instructions" array with a targeted text edit. Comments,
// formatting, unrelated keys, and every other entry survive. A duplicate
// "instructions" key or a non-array value is rejected because either would
// make the edit ambiguous.
func setOpencodeInstruction(raw, instruction string, present bool) (string, error) {
	quoted, err := json.Marshal(instruction)
	if err != nil {
		return "", fmt.Errorf("workspace: quote instructions value: %w", err)
	}
	for {
		masked, err := maskJSONC(raw)
		if err != nil {
			return "", fmt.Errorf("workspace: %s: %w", opencodeConfigName, err)
		}
		valueStart, found, err := topLevelValueStart(masked, "instructions")
		if err != nil {
			return "", fmt.Errorf("workspace: %s: %w", opencodeConfigName, err)
		}
		if !found {
			if !present {
				return raw, nil
			}
			return insertTopLevelValue(raw, "instructions", "["+string(quoted)+"]")
		}
		if masked[valueStart] != '[' {
			return "", fmt.Errorf("workspace: %s: %q exists but is not an array", opencodeConfigName, "instructions")
		}
		valueEnd, err := jsonValueEnd(masked, valueStart)
		if err != nil {
			return "", fmt.Errorf("workspace: %s: instructions: %w", opencodeConfigName, err)
		}
		spans := instructionSpans(masked, valueStart, valueEnd, instruction)
		if len(spans) == 0 {
			if !present {
				return raw, nil
			}
			return insertInstruction(raw, masked, valueStart, valueEnd, string(quoted)), nil
		}
		if present {
			return raw, nil
		}
		// Remove one occurrence at a time so the remaining spans and comma
		// boundaries stay valid after each splice.
		raw = removeInstruction(raw, masked, spans[0])
	}
}

// byteSpan is a half-open byte range in a JSONC document.
type byteSpan struct {
	start int
	end   int
}

// topLevelValueStart returns the index of the value of a unique top-level key
// in a masked document. found is false when the key is absent.
func topLevelValueStart(masked, key string) (int, bool, error) {
	start := -1
	for _, loc := range topLevelKeyPattern.FindAllStringSubmatchIndex(masked, -1) {
		if jsoncDepth(masked, loc[0]) != 1 {
			continue
		}
		decoded, err := unquoteJSONKey(masked[loc[2]:loc[3]])
		if err != nil || decoded != key {
			continue
		}
		if start >= 0 {
			return 0, false, fmt.Errorf("duplicate top-level key %q (ambiguous document)", key)
		}
		start = loc[1]
		for start < len(masked) && isJSONCSpace(masked[start]) {
			start++
		}
	}
	if start < 0 {
		return 0, false, nil
	}
	if start >= len(masked) {
		return 0, false, fmt.Errorf("top-level key %q has no value", key)
	}
	return start, true, nil
}

// jsonValueEnd returns the index just past the JSON value starting at start.
func jsonValueEnd(masked string, start int) (int, error) {
	if start >= len(masked) {
		return 0, fmt.Errorf("missing value")
	}
	if masked[start] == '"' {
		end, ok := stringEnd(masked, start)
		if !ok {
			return 0, fmt.Errorf("unterminated string")
		}
		return end, nil
	}
	if masked[start] != '[' && masked[start] != '{' {
		for i := start; i < len(masked); i++ {
			if masked[i] == ',' || masked[i] == '}' || masked[i] == ']' || isJSONCSpace(masked[i]) {
				return i, nil
			}
		}
		return len(masked), nil
	}
	depth := 0
	for i := start; i < len(masked); {
		switch masked[i] {
		case '"':
			end, ok := stringEnd(masked, i)
			if !ok {
				return 0, fmt.Errorf("unterminated string")
			}
			i = end
		case '[', '{':
			depth++
			i++
		case ']', '}':
			depth--
			i++
			if depth == 0 {
				return i, nil
			}
		default:
			i++
		}
	}
	return 0, fmt.Errorf("unbalanced value starting at byte %d", start)
}

// instructionSpans returns the array-element string spans whose decoded value
// equals instruction. Strings nested inside objects or arrays are ignored.
func instructionSpans(masked string, open, close int, instruction string) []byteSpan {
	var spans []byteSpan
	depth := 1 // inside the top-level array
	for i := open + 1; i < close; {
		switch masked[i] {
		case '"':
			end, ok := stringEnd(masked, i)
			if !ok {
				return spans
			}
			if depth == 1 {
				var decoded string
				if json.Unmarshal([]byte(masked[i:end]), &decoded) == nil && decoded == instruction {
					spans = append(spans, byteSpan{start: i, end: end})
				}
			}
			i = end
		case '[', '{':
			depth++
			i++
		case ']', '}':
			depth--
			i++
		default:
			i++
		}
	}
	return spans
}

// insertInstruction appends one string element to the array at open..close,
// preserving the surrounding whitespace, comments, and formatting.
func insertInstruction(raw, masked string, open, close int, quoted string) string {
	indent := lineIndent(raw, open)
	elementIndent := indent + "  "
	bodyEnd := close - 1 // index of the closing ']'
	last := lastStructural(masked[:bodyEnd])
	if last <= open {
		// Empty array: keep a single-line array inline and expand a
		// multi-line one.
		if strings.Contains(raw[open+1:bodyEnd], "\n") {
			return raw[:open+1] + "\n" + elementIndent + quoted + raw[open+1:]
		}
		return raw[:open+1] + quoted + raw[open+1:]
	}
	separator := raw[last+1 : bodyEnd]
	if !strings.Contains(separator, "\n") {
		if token, index := nextJSONCToken(raw, last+1); token == ',' && index < bodyEnd {
			return raw[:index+1] + " " + quoted + raw[index+1:]
		}
		return raw[:last+1] + ", " + quoted + raw[bodyEnd:]
	}
	comma := ","
	if token, index := nextJSONCToken(raw, last+1); token == ',' && index < bodyEnd {
		comma = ""
	}
	if !strings.HasSuffix(separator, "\n") {
		separator += "\n"
	}
	return raw[:last+1] + comma + separator + elementIndent + quoted + "\n" + indent + raw[bodyEnd:]
}

// removeInstruction deletes one array element plus one adjacent comma,
// leaving comments and every other entry in place. When the entry sits alone
// on its line, the whole line is removed so the surviving array keeps a clean
// shape.
func removeInstruction(raw, masked string, span byteSpan) string {
	prev := lastStructural(masked[:span.start])
	commaBefore := prev >= 0 && masked[prev] == ','
	nextToken, nextIndex := nextJSONCToken(raw, span.end)
	commaAfter := nextToken == ','

	lineStart := strings.LastIndexByte(raw[:span.start], '\n') + 1
	prefixClean := strings.Trim(raw[lineStart:span.start], " \t\r") == ""
	lineEnd := len(raw)
	if index := strings.IndexByte(raw[span.end:], '\n'); index >= 0 {
		lineEnd = span.end + index + 1
	}
	tailClean := false
	switch {
	case commaAfter && nextIndex < lineEnd:
		// The comma is removed with the line; anything else (a comment,
		// the closing bracket) keeps the line intact.
		tailClean = strings.Trim(raw[span.end:nextIndex], " \t\r") == "" &&
			strings.Trim(raw[nextIndex+1:lineEnd], " \t\r\n") == ""
	case commaAfter:
		tailClean = false
	default:
		tailClean = strings.Trim(raw[span.end:lineEnd], " \t\r\n") == ""
	}
	if prefixClean && tailClean {
		if commaBefore {
			return raw[:prev] + raw[prev+1:lineStart] + raw[lineEnd:]
		}
		return raw[:lineStart] + raw[lineEnd:]
	}

	if commaBefore {
		return raw[:prev] + raw[prev+1:span.start] + raw[span.end:]
	}
	if commaAfter {
		return raw[:span.start] + raw[span.end:nextIndex] + raw[nextIndex+1:]
	}
	return raw[:span.start] + raw[span.end:]
}

// nextJSONCToken returns the first byte at or after pos that is neither JSON
// whitespace nor inside a comment, plus its index. raw must already be valid
// JSONC.
func nextJSONCToken(raw string, pos int) (byte, int) {
	for pos < len(raw) {
		switch {
		case isJSONCSpace(raw[pos]):
			pos++
		case raw[pos] == '/' && pos+1 < len(raw) && raw[pos+1] == '/':
			for pos < len(raw) && raw[pos] != '\n' {
				pos++
			}
		case raw[pos] == '/' && pos+1 < len(raw) && raw[pos+1] == '*':
			pos += 2
			for pos+1 < len(raw) && !(raw[pos] == '*' && raw[pos+1] == '/') {
				pos++
			}
			if pos+1 < len(raw) {
				pos += 2
			}
		default:
			return raw[pos], pos
		}
	}
	return 0, pos
}

// lineIndent returns the leading whitespace of the line containing pos.
func lineIndent(raw string, pos int) string {
	start := strings.LastIndexByte(raw[:pos], '\n') + 1
	line := raw[start:pos]
	trimmed := strings.TrimLeft(line, " \t")
	return line[:len(line)-len(trimmed)]
}

// countTopLevelKey counts depth-1 occurrences of a key in a masked document.
// Occurrences are compared by decoded value, so a unicode-escaped spelling
// of the key counts the same as the plain one.
func countTopLevelKey(masked, key string) int {
	count := 0
	for _, loc := range topLevelKeyPattern.FindAllStringSubmatchIndex(masked, -1) {
		if jsoncDepth(masked, loc[0]) != 1 {
			continue
		}
		decoded, err := unquoteJSONKey(masked[loc[2]:loc[3]])
		if err != nil || decoded != key {
			continue
		}
		count++
	}
	return count
}

// insertTopLevelString appends "<key>": <value> to the root object.
func insertTopLevelString(raw, key, quotedValue string) (string, error) {
	return insertTopLevelValue(raw, key, quotedValue)
}

// insertTopLevelValue appends "<key>": <rawValue> to the root object, adding a
// comma after the last structural entry when one is missing. Bytes between the
// last entry and the closing brace (whitespace and comments) are kept.
func insertTopLevelValue(raw, key, rawValue string) (string, error) {
	masked, err := maskJSONC(raw)
	if err != nil {
		return "", fmt.Errorf("workspace: %s: %w", opencodeConfigName, err)
	}
	last := lastStructural(masked)
	if last < 0 || masked[last] != '}' {
		return "", fmt.Errorf("workspace: %s: top-level value is not an object", opencodeConfigName)
	}
	quotedKey, err := json.Marshal(key)
	if err != nil {
		return "", err
	}
	entry := string(quotedKey) + ": " + rawValue
	indent := trimJSONCIndent(masked)

	prev := lastStructural(masked[:last])
	if prev < 0 || masked[prev] == '{' {
		return raw[:last] + "\n" + indent + entry + "\n" + raw[last:], nil
	}

	separator := strings.TrimRight(raw[prev+1:last], " \t\r")
	if !strings.HasSuffix(separator, "\n") {
		separator += "\n"
	}
	comma := ","
	// JSONC allows a trailing comma before the closing brace; reuse it
	// instead of producing a double comma.
	if token, _ := nextJSONCToken(raw, prev+1); token == ',' {
		comma = ""
	}
	return raw[:prev+1] + comma + separator + indent + entry + "\n" + raw[last:], nil
}
