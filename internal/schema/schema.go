// Package schema loads and validates JSON Schema documents.
package schema

import (
	"bytes"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Document is a compiled JSON Schema. Its definition is kept for structured
// output and is not affected by changes to the source file after loading.
type Document struct {
	schema     *jsonschema.Schema
	definition any
	raw        []byte
}

// Load reads a local schema file. Relative paths are resolved against the
// current working directory; callers with another base should resolve them first.
func Load(path string) (*Document, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve schema path %q: %w", path, err)
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("read schema %q: %w", abs, err)
	}
	uriPath := filepath.ToSlash(abs)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	uri := (&url.URL{Scheme: "file", Path: uriPath}).String()
	return Inline(raw, uri)
}

// Inline compiles a JSON object or boolean schema at baseURI, an absolute
// local file URI. Relative references resolve from this URI. An empty fragment
// (including a trailing #) denotes the root; other fragments are not allowed.
func Inline(raw []byte, baseURI string) (*Document, error) {
	u, err := url.Parse(baseURI)
	if err != nil || u.Scheme != "file" || u.Host != "" || u.User != nil ||
		u.Opaque != "" || !strings.HasPrefix(u.Path, "/") || u.RawQuery != "" ||
		u.ForceQuery || u.Fragment != "" || u.RawFragment != "" {
		return nil, fmt.Errorf("schema base URI must be an absolute local file URI without a non-root fragment: %q", baseURI)
	}
	u.Fragment = ""
	u.RawFragment = ""
	location := u.String()

	definition, err := Decode(raw)
	if err != nil {
		return nil, fmt.Errorf("parse schema at %s: %w", location, err)
	}
	switch definition.(type) {
	case map[string]any, bool:
	default:
		return nil, fmt.Errorf("schema at %s must be an object or boolean", location)
	}

	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(jsonschema.FileLoader{})
	if err := compiler.AddResource(location, definition); err != nil {
		return nil, fmt.Errorf("register schema at %s: %w", location, err)
	}
	compiled, err := compiler.Compile(location)
	if err != nil {
		return nil, fmt.Errorf("compile schema at %s: %w", location, err)
	}
	return &Document{schema: compiled, definition: definition, raw: bytes.Clone(raw)}, nil
}

// Decode parses exactly one JSON value, retaining numbers as json.Number for
// exact JSON Schema numeric validation.
func Decode(raw []byte) (any, error) {
	return jsonschema.UnmarshalJSON(bytes.NewReader(raw))
}

// Validate checks a decoded JSON value against the document.
func (d *Document) Validate(value any) error {
	if d == nil || d.schema == nil {
		return fmt.Errorf("nil schema document")
	}
	return d.schema.Validate(value)
}

// JSON returns a copy of the original schema document.
func (d *Document) JSON() []byte {
	if d == nil {
		return nil
	}
	return bytes.Clone(d.raw)
}

// NativeItemsSchema returns a schema for {"items":[...]} when moving the
// document under array items preserves its meaning. Agent adapters must still
// check whether their own native schema dialect supports the result.
func (d *Document) NativeItemsSchema() ([]byte, bool) {
	if d == nil || d.schema == nil || !relocatable(d.definition) {
		return nil, false
	}
	wrapped := make([]byte, 0, len(d.raw)+128)
	wrapped = append(wrapped, `{"type":"object","required":["items"],"additionalProperties":false,"properties":{"items":{"type":"array","items":`...)
	wrapped = append(wrapped, d.raw...)
	wrapped = append(wrapped, `}}}`...)
	return wrapped, true
}

func relocatable(v any) bool {
	switch v := v.(type) {
	case map[string]any:
		for key, child := range v {
			switch key {
			case "$schema", "$id", "id", "$ref", "$dynamicRef", "$recursiveRef",
				"$anchor", "$dynamicAnchor", "$recursiveAnchor", "$vocabulary":
				return false
			case "properties", "patternProperties", "$defs", "definitions", "dependentSchemas":
				if entries, ok := child.(map[string]any); ok {
					for _, entry := range entries {
						if !relocatable(entry) {
							return false
						}
					}
					continue
				}
			}
			if !relocatable(child) {
				return false
			}
		}
	case []any:
		for _, child := range v {
			if !relocatable(child) {
				return false
			}
		}
	}
	return true
}
