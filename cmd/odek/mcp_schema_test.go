package main

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestMCPModelSchema_NilAndNonObject(t *testing.T) {
	if s, d := mcpModelSchema(nil); s != nil || d != nil {
		t.Errorf("nil schema = %v, %v", s, d)
	}
	s, _ := mcpModelSchema("just a string")
	if m, ok := s.(map[string]any); !ok || m["type"] != "object" {
		t.Errorf("non-object schema = %v, want {type:object}", s)
	}
}

func TestMCPModelSchema_StructuralKeywords(t *testing.T) {
	longName := strings.Repeat("n", 65)
	in := map[string]any{
		"type":    []any{"object", "null"},
		"$schema": "http://json-schema.org/draft-07/schema#",
		"$defs": map[string]any{
			"Item": map[string]any{"type": "string", "description": "DEF_DOC"},
		},
		"properties": map[string]any{
			"ref":    map[string]any{"$ref": "#/$defs/Item"},
			"bad":    map[string]any{"$ref": "https://evil.example/schema", "type": "not-a-type"},
			longName: map[string]any{"type": "string"},
			"tuple": map[string]any{
				"type":        "array",
				"prefixItems": []any{map[string]any{"type": "string", "title": "TUPLE_DOC"}, "junk"},
				"items":       false,
			},
			"choice": map[string]any{
				"anyOf": []any{map[string]any{"type": "string"}, map[string]any{"type": "integer", "description": "ANY_DOC"}},
			},
			"num": map[string]any{
				"type": "number", "exclusiveMinimum": true, "multipleOf": float64(2),
				"minimum": "zero", "uniqueItems": "yes",
			},
			"fmt": map[string]any{"type": "string", "format": "date time", "pattern": strings.Repeat("a", maxMCPSchemaPatternRunes+1)},
			"obj": map[string]any{
				"type":    "object",
				"enum":    []any{map[string]any{"a": "OBJ_ENUM"}},
				"const":   "CONST " + strings.Repeat("x", maxMCPSchemaWordRunes),
				"default": map[string]any{"k": float64(1)},
			},
			"nested": map[string]any{
				"type":                 "object",
				"additionalProperties": map[string]any{"type": "string", "description": "ADDL_DOC"},
				"patternProperties":    map[string]any{"^x": map[string]any{"type": "string"}},
				"dependentRequired":    map[string]any{"a": []any{"b", float64(3)}},
				"not":                  map[string]any{"type": "null"},
			},
		},
		"required": []any{"ref", longName, float64(7)},
	}
	before, _ := json.Marshal(in)
	out, docs := mcpModelSchema(in)
	after, _ := json.Marshal(in)
	if string(before) != string(after) {
		t.Fatal("input schema mutated")
	}
	got := out.(map[string]any)
	js, _ := json.Marshal(got)
	for _, banned := range []string{"$schema", "DEF_DOC", "evil.example", "not-a-type", longName,
		"TUPLE_DOC", "ANY_DOC", "date time", "OBJ_ENUM", "CONST", "ADDL_DOC", `"zero"`, `"yes"`} {
		if strings.Contains(string(js), banned) {
			t.Errorf("%q kept in model schema: %s", banned, js)
		}
	}
	if req := got["required"].([]any); len(req) != 1 || req[0] != "ref" {
		t.Errorf("required = %v, want [ref]", req)
	}
	props := got["properties"].(map[string]any)
	if props["ref"].(map[string]any)["$ref"] != "#/$defs/Item" {
		t.Errorf("local $ref dropped: %v", props["ref"])
	}
	tuple := props["tuple"].(map[string]any)
	if pi := tuple["prefixItems"].([]any); len(pi) != 2 || pi[1] != true {
		t.Errorf("prefixItems positions not kept: %v", pi)
	}
	if tuple["items"] != false {
		t.Errorf("boolean items lost: %v", tuple)
	}
	num := props["num"].(map[string]any)
	if num["exclusiveMinimum"] != true || num["multipleOf"] != float64(2) {
		t.Errorf("numeric keywords: %v", num)
	}
	if _, ok := props["obj"].(map[string]any)["default"]; ok {
		t.Error("object default kept")
	}
	nested := props["nested"].(map[string]any)
	if dr := nested["dependentRequired"].(map[string]any)["a"].([]any); len(dr) != 1 {
		t.Errorf("dependentRequired = %v", dr)
	}
	if nested["not"] == nil || nested["patternProperties"] == nil {
		t.Errorf("subschemas lost: %v", nested)
	}

	rendered := renderMCPParamDocs(docs)
	for _, want := range []string{"DEF_DOC", "TUPLE_DOC", "ANY_DOC", "OBJ_ENUM", "CONST", "ADDL_DOC", "(input) [$schema]"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("docs missing %q:\n%s", want, rendered)
		}
	}
}

func TestRenderMCPParamDocs_Bounded(t *testing.T) {
	if renderMCPParamDocs(nil) != "" {
		t.Error("empty docs should render empty")
	}
	var docs []mcpParamDoc
	for i := 0; i < 50; i++ {
		docs = append(docs, mcpParamDoc{path: "p", field: "description", text: mcpDocText(strings.Repeat("word\nline ", 200))})
	}
	if n := utf8.RuneCountInString(docs[0].text); n > maxMCPParamDocFieldRunes {
		t.Errorf("field = %d runes, want <= %d", n, maxMCPParamDocFieldRunes)
	}
	if strings.Contains(docs[0].text, "\n") {
		t.Error("doc field not flattened to one line")
	}
	out := renderMCPParamDocs(docs)
	if n := utf8.RuneCountInString(out); n > maxMCPParamDocsRunes {
		t.Errorf("rendered = %d runes, want <= %d", n, maxMCPParamDocsRunes)
	}
	if !strings.Contains(out, "parameter documentation truncated") {
		t.Error("missing truncation notice")
	}
	if truncateRunesNotice("abcdef", 2, "...") != "..." {
		t.Error("notice longer than cap")
	}
}

func TestMCPModelSchema_EnumBounds(t *testing.T) {
	many := make([]any, maxMCPEnumValues+1)
	for i := range many {
		many[i] = float64(i)
	}
	out, docs := mcpModelSchema(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"a": map[string]any{"enum": many},
			"b": map[string]any{"enum": []any{"x\ny"}},
			"c": map[string]any{"enum": []any{nil, true, float64(1), "ok"}},
			"d": map[string]any{"enum": "not-a-list"},
		},
	})
	props := out.(map[string]any)["properties"].(map[string]any)
	for _, k := range []string{"a", "b", "d"} {
		if _, ok := props[k].(map[string]any)["enum"]; ok {
			t.Errorf("enum %s should be moved to docs", k)
		}
	}
	if len(props["c"].(map[string]any)["enum"].([]any)) != 4 {
		t.Error("scalar enum dropped")
	}
	if len(docs) != 3 {
		t.Errorf("docs = %d, want 3", len(docs))
	}
}
