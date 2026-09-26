package schema

import (
	"bytes"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fileURI(path string) string {
	uriPath := filepath.ToSlash(path)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	return (&url.URL{Scheme: "file", Path: uriPath}).String()
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustDecode(t *testing.T, raw string) any {
	t.Helper()
	value, err := Decode([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func assertAcceptsAndRejects(t *testing.T, doc *Document, valid, invalid string) {
	t.Helper()
	if err := doc.Validate(mustDecode(t, valid)); err != nil {
		t.Fatalf("%s should be accepted: %v", valid, err)
	}
	if err := doc.Validate(mustDecode(t, invalid)); err == nil {
		t.Fatalf("%s should be rejected", invalid)
	}
}

func TestLoadFileReferences(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a folder #1")
	path := filepath.Join(dir, "root #.json")
	writeFile(t, filepath.Join(dir, "defs.json"), `{"$defs":{"value":{"type":"integer","minimum":2}}}`)
	writeFile(t, path, `{"$schema":"https://json-schema.org/draft/2020-12/schema","$ref":"defs.json#/$defs/value"}`)

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{path}
	if relative, err := filepath.Rel(cwd, path); err == nil {
		paths = append(paths, relative)
	} else if filepath.VolumeName(cwd) == filepath.VolumeName(path) {
		t.Fatal(err)
	}
	for _, name := range paths {
		t.Run(name, func(t *testing.T) {
			doc, err := Load(name)
			if err != nil {
				t.Fatal(err)
			}
			assertAcceptsAndRejects(t, doc, "2", "1")
		})
	}
}

func TestReferencedSchemaUsesItsOwnDraft(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "root.json")
	writeFile(t, root, `{"$schema":"https://json-schema.org/draft/2020-12/schema","$ref":"draft7.json"}`)
	writeFile(t, filepath.Join(dir, "draft7.json"), `{"$schema":"http://json-schema.org/draft-07/schema#","type":"string","format":"email"}`)

	doc, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	assertAcceptsAndRejects(t, doc, `"person@example.com"`, `"not-an-email"`)
}

func TestInlineBaseAndRootFragment(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "schemas", "defs.json"), `{"$defs":{"value":{"type":"string"}}}`)
	base := fileURI(filepath.Join(dir, "config.md"))
	for _, tc := range []struct {
		name, raw, valid, invalid string
	}{
		{
			name:    "relative file from id",
			raw:     `{"$id":"schemas/root.json","$ref":"defs.json#/$defs/value"}`,
			valid:   `"ok"`,
			invalid: `1`,
		},
		{
			name:    "local root fragment from id",
			raw:     `{"$id":"schemas/root.json","$defs":{"value":{"type":"integer"}},"$ref":"#/$defs/value"}`,
			valid:   `1`,
			invalid: `"no"`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := Inline([]byte(tc.raw), base+"#")
			if err != nil {
				t.Fatal(err)
			}
			assertAcceptsAndRejects(t, doc, tc.valid, tc.invalid)
		})
	}
}

func TestInvalidSchemasAndURIs(t *testing.T) {
	base := fileURI(filepath.Join(t.TempDir(), "config.md"))
	for _, tc := range []struct {
		name, raw string
	}{
		{"array root", `[]`},
		{"null root", `null`},
		{"number root", `10`},
		{"string root", `"schema"`},
		{"invalid JSON", `{"type":`},
		{"multiple JSON values", `{} {}`},
		{"invalid keyword", `{"type":17}`},
		{"draft-04 boolean subschema", `{"$schema":"http://json-schema.org/draft-04/schema#","properties":{"x":true}}`},
		{"unsupported draft", `{"$schema":"https://json-schema.org/draft/2030-01/schema"}`},
		{"missing meta schema", `{"$schema":"file:///nonexistent-sagepipe-meta-schema.json"}`},
		{"http reference", `{"$ref":"https://example.invalid/sagepipe-schema.json"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Inline([]byte(tc.raw), base); err == nil {
				t.Fatal("expected schema compilation to fail")
			}
		})
	}
	for _, uri := range []string{
		"config.md", "http://example.invalid/schema.json", "file:relative.json",
		base + "#/$defs/part", base + "?query=1", "file://other-host/path",
	} {
		t.Run(uri, func(t *testing.T) {
			if _, err := Inline([]byte(`true`), uri); err == nil {
				t.Fatal("expected invalid base URI to be rejected")
			}
		})
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("expected missing schema file to fail")
	}
}

func TestDraftsAndFormats(t *testing.T) {
	base := fileURI(filepath.Join(t.TempDir(), "schema.json"))
	for _, tc := range []struct {
		name, draft string
	}{
		{"draft-04", "http://json-schema.org/draft-04/schema#"},
		{"draft-06", "http://json-schema.org/draft-06/schema#"},
		{"draft-07", "http://json-schema.org/draft-07/schema#"},
		{"draft-2019-09", "https://json-schema.org/draft/2019-09/schema"},
		{"draft-2020-12", "https://json-schema.org/draft/2020-12/schema"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(map[string]any{
				"$schema": tc.draft, "type": "integer", "minimum": 2,
			})
			if err != nil {
				t.Fatal(err)
			}
			doc, err := Inline(raw, base)
			if err != nil {
				t.Fatal(err)
			}
			assertAcceptsAndRejects(t, doc, "2", "1")
		})
	}

	for _, tc := range []struct {
		name, raw    string
		invalidEmail bool
	}{
		{"draft-07 assertion", `{"$schema":"http://json-schema.org/draft-07/schema#","format":"email"}`, true},
		{"draft-2020-12 annotation", `{"$schema":"https://json-schema.org/draft/2020-12/schema","format":"email"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := Inline([]byte(tc.raw), base)
			if err != nil {
				t.Fatal(err)
			}
			err = doc.Validate(mustDecode(t, `"not-an-email"`))
			if (err != nil) != tc.invalidEmail {
				t.Fatalf("unexpected email validation result: %v", err)
			}
		})
	}
}

func TestRequiredVocabs(t *testing.T) {
	dir := t.TempDir()
	meta := filepath.Join(dir, "meta.json")
	base := fileURI(filepath.Join(dir, "schema.json"))
	for _, tc := range []struct {
		name, vocab                         string
		required, compileError, rejectEmail bool
	}{
		{name: "format assertion", vocab: "https://json-schema.org/draft/2020-12/vocab/format-assertion", required: true, rejectEmail: true},
		{name: "unsupported required", vocab: "https://example.invalid/vocab/extra", required: true, compileError: true},
		{name: "unsupported optional", vocab: "https://example.invalid/vocab/extra"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			metaJSON, err := json.Marshal(map[string]any{
				"$schema":     "https://json-schema.org/draft/2020-12/schema",
				"$id":         fileURI(meta),
				"$vocabulary": map[string]bool{tc.vocab: tc.required},
				"$ref":        "https://json-schema.org/draft/2020-12/schema",
			})
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, meta, string(metaJSON))
			raw, err := json.Marshal(map[string]any{"$schema": fileURI(meta), "format": "email"})
			if err != nil {
				t.Fatal(err)
			}
			doc, err := Inline(raw, base)
			if tc.compileError {
				if err == nil {
					t.Fatal("expected unsupported required vocabulary to fail")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			err = doc.Validate(mustDecode(t, `"not-an-email"`))
			if (err != nil) != tc.rejectEmail {
				t.Fatalf("unexpected format assertion result: %v", err)
			}
		})
	}
}

func TestDecodePreservesNumericPrecision(t *testing.T) {
	base := fileURI(filepath.Join(t.TempDir(), "schema.json"))
	for _, tc := range []struct {
		name, schema, valid, invalid string
	}{
		{"large integer", `{"minimum":9007199254740993,"maximum":9007199254740993}`, `9007199254740993`, `9007199254740992`},
		{"exact decimal", `{"multipleOf":0.01,"maximum":0.3}`, `0.3`, `0.30000000000000004`},
		{"fraction", `{"multipleOf":0.01}`, `0.29`, `0.291`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := Inline([]byte(tc.schema), base)
			if err != nil {
				t.Fatal(err)
			}
			valid := mustDecode(t, tc.valid)
			if _, ok := valid.(json.Number); !ok {
				t.Fatalf("expected json.Number, got %T", valid)
			}
			if err := doc.Validate(valid); err != nil {
				t.Fatal(err)
			}
			if err := doc.Validate(mustDecode(t, tc.invalid)); err == nil {
				t.Fatal("rounded value incorrectly accepted")
			}
		})
	}
	for _, raw := range []string{`1 2`, `{"value": 1} {"value": 2}`, `1x`} {
		if _, err := Decode([]byte(raw)); err == nil {
			t.Fatalf("expected extra content to fail: %q", raw)
		}
	}
}

func TestBooleanSchemas(t *testing.T) {
	base := fileURI(filepath.Join(t.TempDir(), "schema.json"))
	for _, tc := range []struct {
		raw, invalid string
	}{
		{"true", ""},
		{"false", `null`},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			doc, err := Inline([]byte(tc.raw), base)
			if err != nil {
				t.Fatal(err)
			}
			if err := doc.Validate(mustDecode(t, tc.raw)); tc.raw == "true" && err != nil {
				t.Fatal(err)
			} else if tc.raw == "false" && err == nil {
				t.Fatal("false schema accepted a value")
			}
			wrapped, ok := doc.NativeItemsSchema()
			if !ok {
				t.Fatal("boolean schema should be safely wrappable")
			}
			response, err := Inline(wrapped, base)
			if err != nil {
				t.Fatal(err)
			}
			if err := response.Validate(mustDecode(t, `{"items":[]}`)); err != nil {
				t.Fatal(err)
			}
			if tc.invalid != "" && response.Validate(mustDecode(t, `{"items":[null]}`)) == nil {
				t.Fatal("false items schema accepted an item")
			}
		})
	}
}

func TestNativeItemsSchema(t *testing.T) {
	base := fileURI(filepath.Join(t.TempDir(), "schema.json"))
	raw := []byte(`{"type":"object","required":["id"],"properties":{"id":{"type":"integer"},"$ref":{"type":"string"}},"additionalProperties":false}`)
	doc, err := Inline(raw, base)
	if err != nil {
		t.Fatal(err)
	}
	copyOfRaw := doc.JSON()
	copyOfRaw[0] = 'X'
	if !bytes.Equal(doc.JSON(), raw) {
		t.Fatal("JSON returned a mutable view of the source")
	}
	wrapped, ok := doc.NativeItemsSchema()
	if !ok {
		t.Fatal("simple schema should be safely wrappable")
	}
	response, err := Inline(wrapped, base)
	if err != nil {
		t.Fatal(err)
	}
	for _, valid := range []string{`{"items":[]}`, `{"items":[{"id":1}]}`} {
		if err := response.Validate(mustDecode(t, valid)); err != nil {
			t.Fatalf("%s: %v", valid, err)
		}
	}
	for _, invalid := range []string{`{}`, `{"items":null}`, `{"items":[{"id":"1"}]}`, `{"items":[],"extra":1}`} {
		if err := response.Validate(mustDecode(t, invalid)); err == nil {
			t.Fatalf("%s should be rejected", invalid)
		}
	}
	wrapped[0] = 'X'
	if again, ok := doc.NativeItemsSchema(); !ok || !bytes.HasPrefix(again, []byte(`{"type":"object"`)) {
		t.Fatal("native schema returned a mutable view")
	}
}

func TestNativeItemsSchemaFallsBack(t *testing.T) {
	base := fileURI(filepath.Join(t.TempDir(), "schema.json"))
	for _, tc := range []struct {
		name, raw string
	}{
		{"dialect", `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"string"}`},
		{"id", `{"$id":"sub/schema.json","type":"string"}`},
		{"legacy id", `{"id":"sub/schema.json","type":"string"}`},
		{"ref", `{"$defs":{"value":{"type":"string"}},"$ref":"#/$defs/value"}`},
		{"nested ref", `{"type":"object","properties":{"name":{"$ref":"#/$defs/value"}},"$defs":{"value":{"type":"string"}}}`},
		{"dynamic ref", `{"$dynamicAnchor":"node","$dynamicRef":"#node"}`},
		{"recursive ref", `{"$recursiveRef":"#"}`},
		{"anchor", `{"$anchor":"part","type":"string"}`},
		{"vocabulary", `{"$vocabulary":{"https://example.invalid/vocab/extra":false}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := Inline([]byte(tc.raw), base)
			if err != nil {
				t.Fatal(err)
			}
			if wrapped, ok := doc.NativeItemsSchema(); ok || wrapped != nil {
				t.Fatal("unsafe schema should fall back to local validation")
			}
		})
	}
	var nilDoc *Document
	if err := nilDoc.Validate(nil); err == nil || !strings.Contains(err.Error(), "nil schema") {
		t.Fatalf("nil document must return an error: %v", err)
	}
	if wrapped, ok := nilDoc.NativeItemsSchema(); ok || wrapped != nil {
		t.Fatal("nil document should not be wrappable")
	}
}
