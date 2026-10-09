package main

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/BackendStack21/odek/internal/guard"
	"github.com/BackendStack21/odek/internal/mcpclient"
)

// newMCPModelTool builds the model-facing tool for one approved MCP tool
// definition. A malicious MCP server controls the tool name, description,
// and parameter schema — all of which flow into the model's tool catalogue
// as effectively trusted instructions ("tool poisoning"). The untrusted
// wrapper only guards the tool's runtime output, so:
//
//   - the schema sent to the provider keeps only structural JSON-Schema
//     keywords (mcpModelSchema): every free-text field — description, title,
//     examples, string defaults, $comment, unknown/vendor keys, over-long
//     enum or const strings — is removed at every nesting level;
//   - that free text is rendered as bounded parameter documentation and
//     appended to the server description, and the combined text is scanned
//     and wrapped in the nonce'd untrusted boundary by sanitizeMCPToolDoc.
//
// The original def.InputSchema is never mutated; the approval key hashes it.
func newMCPModelTool(client *mcpclient.Client, serverName string, def mcpclient.ToolDef, g guard.Guard, guardCfg guard.Config) *untrustedToolWrapper {
	schema, docs := mcpModelSchema(def.InputSchema)
	inner := &mcpclient.ToolAdapter{
		Client:      client,
		ToolName:    def.Name,
		Desc:        sanitizeMCPToolDoc(serverName, def.Name, def.Description, renderMCPParamDocs(docs), g, guardCfg),
		ParamSchema: schema,
	}
	return &untrustedToolWrapper{
		inner:  inner,
		source: "mcp:" + serverName + ":" + def.Name,
	}
}

const (
	// maxMCPSchemaWordRunes bounds format names and $ref pointers kept in
	// the provider schema.
	maxMCPSchemaWordRunes = 128
	// maxMCPEnumStringRunes and maxMCPEnumStringSpaces bound a string kept
	// as an enum/const value or default: a short token, not a sentence.
	// Anything longer is moved to the wrapped parameter documentation.
	maxMCPEnumStringRunes  = 64
	maxMCPEnumStringSpaces = 3
	// maxMCPSchemaPatternRunes bounds a kept `pattern` regex.
	maxMCPSchemaPatternRunes = 512
	// maxMCPPatternKeyRunes bounds a kept patternProperties regex key.
	maxMCPPatternKeyRunes = 128
	// maxMCPDocLabelRunes bounds the keyword label of a lifted doc entry.
	maxMCPDocLabelRunes = 64
	// maxMCPEnumValues bounds a kept enum list.
	maxMCPEnumValues = 256
	// maxMCPParamDocFieldRunes bounds each rendered documentation value.
	maxMCPParamDocFieldRunes = 512
	// maxMCPParamDocsRunes bounds the whole rendered parameter documentation.
	maxMCPParamDocsRunes = 4 * 1024
)

// mcpParamDoc is one piece of server-supplied free text lifted out of a
// schema: the location it documents and the keyword it came from.
type mcpParamDoc struct {
	path  string
	field string
	text  string
}

// mcpSchemaSubschemaKeys hold a single subschema (or, for items, possibly
// an array of subschemas; additionalProperties/additionalItems may be bool).
var mcpSchemaSubschemaKeys = map[string]string{
	"items":                 "[]",
	"additionalItems":       "[+]",
	"additionalProperties":  "{*}",
	"unevaluatedProperties": "{*}",
	"unevaluatedItems":      "[+]",
	"propertyNames":         "{name}",
	"contains":              "[contains]",
	"not":                   "(not)",
	"if":                    "(if)",
	"then":                  "(then)",
	"else":                  "(else)",
}

// mcpSchemaSubschemaArrayKeys hold an array of subschemas.
var mcpSchemaSubschemaArrayKeys = map[string]bool{
	"anyOf": true, "oneOf": true, "allOf": true, "prefixItems": true,
}

// mcpSchemaSubschemaMapKeys hold a name → subschema map whose names are not
// property names the model fills in.
var mcpSchemaSubschemaMapKeys = map[string]bool{
	"patternProperties": true, "$defs": true, "definitions": true, "dependentSchemas": true,
}

var mcpSchemaNumericKeys = map[string]bool{
	"minimum": true, "maximum": true, "exclusiveMinimum": true, "exclusiveMaximum": true,
	"multipleOf": true, "minLength": true, "maxLength": true, "minItems": true,
	"maxItems": true, "minContains": true, "maxContains": true, "minProperties": true,
	"maxProperties": true,
}

var mcpSchemaBoolKeys = map[string]bool{
	"uniqueItems": true, "nullable": true, "deprecated": true, "readOnly": true, "writeOnly": true,
}

// exclusiveMinimum/Maximum are booleans in draft-04.
var mcpSchemaNumericOrBoolKeys = map[string]bool{
	"exclusiveMinimum": true, "exclusiveMaximum": true,
}

var mcpSchemaJSONTypes = map[string]bool{
	"string": true, "number": true, "integer": true, "boolean": true,
	"object": true, "array": true, "null": true,
}

// mcpModelSchema returns a structural copy of an MCP input schema that is
// safe to send to the provider, plus the free text it removed. Only known
// structural keywords are kept, each only when its value has the expected
// shape; everything else is either lifted into the returned docs (text the
// model benefits from reading, wrapped later) or dropped. The input is not
// modified.
func mcpModelSchema(schema any) (any, []mcpParamDoc) {
	if schema == nil {
		return nil, nil
	}
	var docs []mcpParamDoc
	out := stripMCPSchemaNode(schema, "", &docs)
	if out == nil {
		out = map[string]any{"type": "object"}
	}
	return out, docs
}

// stripMCPSchemaNode handles one subschema. Booleans are valid schemas and
// pass through; anything else that is not an object is dropped.
func stripMCPSchemaNode(node any, path string, docs *[]mcpParamDoc) any {
	switch x := node.(type) {
	case bool:
		return x
	case map[string]any:
		return stripMCPSchemaObject(x, path, docs)
	default:
		return nil
	}
}

func stripMCPSchemaObject(obj map[string]any, path string, docs *[]mcpParamDoc) map[string]any {
	out := make(map[string]any, len(obj))
	addDoc := func(field string, v any) {
		if text := mcpDocText(v); text != "" {
			field = truncateRunesNotice(strings.Join(strings.Fields(field), " "), maxMCPDocLabelRunes, "…")
			*docs = append(*docs, mcpParamDoc{path: path, field: field, text: text})
		}
	}
	var keptProps map[string]any
	for _, k := range sortedSchemaKeys(obj) {
		v := obj[k]
		switch {
		case k == "type":
			if t := mcpSchemaType(v); t != nil {
				out[k] = t
			}
		case k == "properties":
			props, ok := v.(map[string]any)
			if !ok {
				continue
			}
			keptProps = make(map[string]any, len(props))
			for _, name := range sortedSchemaKeys(props) {
				if !isSchemaName(name) {
					// The name itself is server text: lift it into the
					// wrapped docs, never into the provider schema.
					addDoc("dropped parameter name", name)
					continue
				}
				if sub := stripMCPSchemaNode(props[name], joinSchemaPath(path, name), docs); sub != nil {
					keptProps[name] = sub
				}
			}
			if len(keptProps) > 0 || path == "" {
				out[k] = keptProps
			}
		case k == "required":
			if names := mcpNameList(v); len(names) > 0 {
				out[k] = names
			}
		case k == "dependentRequired":
			m, ok := v.(map[string]any)
			if !ok {
				continue
			}
			kept := make(map[string]any, len(m))
			for _, name := range sortedSchemaKeys(m) {
				if !isSchemaName(name) {
					continue
				}
				if names := mcpNameList(m[name]); len(names) > 0 {
					kept[name] = names
				}
			}
			if len(kept) > 0 {
				out[k] = kept
			}
		case k == "enum":
			if list, ok := v.([]any); ok && mcpBoundedScalars(list) {
				out[k] = append([]any(nil), list...)
			} else {
				addDoc("allowed values", v)
			}
		case k == "const":
			if mcpBoundedScalars([]any{v}) {
				out[k] = v
			} else {
				addDoc("required value", v)
			}
		case k == "default":
			// Only a number, boolean or null default stays: strings, objects
			// and arrays carry server-chosen text (values or keys).
			if _, isStr := v.(string); !isStr && mcpBoundedScalars([]any{v}) {
				out[k] = v
			} else {
				addDoc("default", v)
			}
		case k == "format":
			if s, ok := v.(string); ok && utf8.RuneCountInString(s) <= maxMCPSchemaWordRunes && isSchemaWord(s) {
				out[k] = s
			}
		case k == "pattern":
			if s, ok := v.(string); ok && utf8.RuneCountInString(s) <= maxMCPSchemaPatternRunes {
				out[k] = s
			} else {
				addDoc("pattern", v)
			}
		case k == "$ref":
			if s, ok := v.(string); ok && (s == "#" || strings.HasPrefix(s, "#/")) && utf8.RuneCountInString(s) <= maxMCPSchemaWordRunes && isSchemaWord(s) {
				out[k] = s
			}
		case mcpSchemaNumericOrBoolKeys[k]:
			if isJSONNumber(v) {
				out[k] = v
			} else if b, ok := v.(bool); ok {
				out[k] = b
			}
		case mcpSchemaNumericKeys[k]:
			if isJSONNumber(v) {
				out[k] = v
			}
		case mcpSchemaBoolKeys[k]:
			if b, ok := v.(bool); ok {
				out[k] = b
			}
		case mcpSchemaSubschemaKeys[k] != "":
			subPath := path + mcpSchemaSubschemaKeys[k]
			if k == "items" {
				if arr, ok := v.([]any); ok {
					out[k] = stripMCPSchemaArray(arr, subPath, docs)
					continue
				}
			}
			if sub := stripMCPSchemaNode(v, subPath, docs); sub != nil {
				out[k] = sub
			}
		case mcpSchemaSubschemaArrayKeys[k]:
			if arr, ok := v.([]any); ok {
				out[k] = stripMCPSchemaArray(arr, path+"("+k+")", docs)
			}
		case mcpSchemaSubschemaMapKeys[k]:
			m, ok := v.(map[string]any)
			if !ok {
				continue
			}
			kept := make(map[string]any, len(m))
			for _, name := range sortedSchemaKeys(m) {
				// patternProperties keys are regexes: a short token with no
				// whitespace. $defs/definitions/dependentSchemas keys are
				// names and follow the property-name charset.
				if k == "patternProperties" {
					if utf8.RuneCountInString(name) > maxMCPPatternKeyRunes || !isSchemaWord(name) {
						continue
					}
				} else if !isSchemaName(name) {
					continue
				}
				if sub := stripMCPSchemaNode(m[name], path+"("+k+":"+name+")", docs); sub != nil {
					kept[name] = sub
				}
			}
			if len(kept) > 0 {
				out[k] = kept
			}
		default:
			// description, title, examples, $comment, $schema, $id and any
			// vendor or unknown keyword: server free text, never kept in the
			// provider schema.
			addDoc(k, v)
		}
	}
	// required names only properties that survived in this object, so the
	// stripped schema never demands a parameter the model cannot see.
	if req, ok := out["required"].([]any); ok && keptProps != nil {
		kept := make([]any, 0, len(req))
		for _, r := range req {
			if _, ok := keptProps[r.(string)]; ok {
				kept = append(kept, r)
			}
		}
		if len(kept) > 0 {
			out["required"] = kept
		} else {
			delete(out, "required")
		}
	}
	return out
}

func stripMCPSchemaArray(arr []any, path string, docs *[]mcpParamDoc) []any {
	out := make([]any, 0, len(arr))
	for i, el := range arr {
		sub := stripMCPSchemaNode(el, path+"#"+strconv.Itoa(i+1), docs)
		if sub == nil {
			// Keep positions stable (prefixItems is positional).
			sub = true
		}
		out = append(out, sub)
	}
	return out
}

func joinSchemaPath(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

func sortedSchemaKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// mcpSchemaType keeps a `type` value only when it names JSON types.
func mcpSchemaType(v any) any {
	switch x := v.(type) {
	case string:
		if mcpSchemaJSONTypes[x] {
			return x
		}
	case []any:
		out := make([]any, 0, len(x))
		for _, el := range x {
			s, ok := el.(string)
			if !ok || !mcpSchemaJSONTypes[s] {
				return nil
			}
			out = append(out, s)
		}
		return out
	}
	return nil
}

// mcpNameList keeps the entries of a list that are valid schema names
// (isSchemaName), dropping anything else.
func mcpNameList(v any) []any {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]any, 0, len(arr))
	for _, el := range arr {
		if s, ok := el.(string); ok && isSchemaName(s) {
			out = append(out, s)
		}
	}
	return out
}

// isSchemaName reports whether s is an identifier-shaped schema name:
// 1-64 characters from [A-Za-z0-9_.$@:-]. Property names, $defs names and
// required entries outside this charset are server free text and never
// reach the provider schema. Real MCP servers use snake_case, camelCase or
// kebab-case names, which all fit.
func isSchemaName(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '_' || c == '.' || c == '$' || c == '@' || c == ':' || c == '-':
		default:
			return false
		}
	}
	return true
}

// mcpBoundedScalars reports whether every value is a JSON scalar and every
// string among them is a short token — the shape of a real enum value, not
// a sentence: at most maxMCPEnumStringRunes runes, single-line, and at most
// maxMCPEnumStringSpaces spaces.
func mcpBoundedScalars(list []any) bool {
	if len(list) > maxMCPEnumValues {
		return false
	}
	for _, el := range list {
		switch x := el.(type) {
		case nil, bool:
		case string:
			if utf8.RuneCountInString(x) > maxMCPEnumStringRunes || strings.ContainsAny(x, "\r\n") ||
				strings.Count(x, " ") > maxMCPEnumStringSpaces {
				return false
			}
		default:
			if !isJSONNumber(x) {
				return false
			}
		}
	}
	return true
}

func isJSONNumber(v any) bool {
	switch v.(type) {
	case float64, float32, int, int64, int32, json.Number:
		return true
	}
	return false
}

// isSchemaWord reports whether s is a single token of printable ASCII
// (format names, local $ref pointers).
func isSchemaWord(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] <= ' ' || s[i] >= 0x7f {
			return false
		}
	}
	return true
}

// mcpDocText renders a lifted value as single-line, bounded text.
func mcpDocText(v any) string {
	var s string
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		s = x
	default:
		data, err := json.Marshal(x)
		if err != nil {
			return ""
		}
		s = string(data)
	}
	s = strings.Join(strings.Fields(s), " ")
	return truncateRunesNotice(s, maxMCPParamDocFieldRunes, "…")
}

// renderMCPParamDocs renders lifted schema text as a bounded plain-text
// block. The result is server-controlled text and must only ever reach the
// model through sanitizeMCPToolDoc's wrapper.
func renderMCPParamDocs(docs []mcpParamDoc) string {
	if len(docs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Parameter documentation (from the server's input schema):\n")
	for _, d := range docs {
		where := d.path
		if where == "" {
			where = "(input)"
		}
		b.WriteString("- ")
		b.WriteString(where)
		b.WriteString(" [")
		b.WriteString(d.field)
		b.WriteString("]: ")
		b.WriteString(d.text)
		b.WriteByte('\n')
	}
	return truncateRunesNotice(b.String(), maxMCPParamDocsRunes,
		"\n[odek: parameter documentation truncated]\n")
}

// truncateRunesNotice caps s at max runes, ending with notice when cut.
func truncateRunesNotice(s string, max int, notice string) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	keep := max - utf8.RuneCountInString(notice)
	if keep < 0 {
		keep = 0
	}
	return string([]rune(s)[:keep]) + notice
}
