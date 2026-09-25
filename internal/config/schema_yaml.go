package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
)

// YAMLToJSON decodes YAML numbers as float64. Walk the syntax tree instead so
// JSON-compatible numeric literals retain their exact spelling in schemas.
func schemaYAMLToJSON(raw yaml.RawMessage) ([]byte, error) {
	file, err := parser.ParseBytes(raw, 0)
	if err != nil {
		return nil, err
	}
	if len(file.Docs) != 1 {
		return nil, fmt.Errorf("expected a single YAML schema")
	}
	if file.Docs[0].Body == nil {
		return []byte("null"), nil
	}
	var buf bytes.Buffer
	if err := writeSchemaJSON(&buf, file.Docs[0].Body, make(map[ast.Node]bool)); err != nil {
		return nil, err
	}
	if !json.Valid(buf.Bytes()) {
		return nil, fmt.Errorf("schema is not valid JSON")
	}
	return buf.Bytes(), nil
}

func writeSchemaJSON(buf *bytes.Buffer, node ast.Node, active map[ast.Node]bool) error {
	if active[node] {
		return fmt.Errorf("cyclic schema alias")
	}
	active[node] = true
	defer delete(active, node)

	switch n := node.(type) {
	case *ast.MappingNode:
		buf.WriteByte('{')
		seen := make(map[string]bool, len(n.Values))
		for i, entry := range n.Values {
			var key string
			if err := yaml.NodeToValue(entry.Key, &key); err != nil {
				return fmt.Errorf("schema mapping key: %w", err)
			}
			if seen[key] {
				return fmt.Errorf("duplicate schema key %q", key)
			}
			seen[key] = true
			if i > 0 {
				buf.WriteByte(',')
			}
			encoded, _ := json.Marshal(key)
			buf.Write(encoded)
			buf.WriteByte(':')
			if err := writeSchemaJSON(buf, entry.Value, active); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	case *ast.SequenceNode:
		buf.WriteByte('[')
		for i, value := range n.Values {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeSchemaJSON(buf, value, active); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case *ast.StringNode:
		encoded, _ := json.Marshal(n.Value)
		buf.Write(encoded)
	case *ast.LiteralNode:
		encoded, _ := json.Marshal(n.GetValue())
		buf.Write(encoded)
	case *ast.BoolNode:
		buf.WriteString(strconv.FormatBool(n.Value))
	case *ast.NullNode:
		buf.WriteString("null")
	case *ast.IntegerNode:
		return writeSchemaNumber(buf, n.Token.Value, n.Value)
	case *ast.FloatNode:
		return writeSchemaNumber(buf, n.Token.Value, n.Value)
	case *ast.AnchorNode:
		return writeSchemaJSON(buf, n.Value, active)
	case *ast.AliasNode:
		return writeSchemaJSON(buf, n.Value, active)
	default:
		return fmt.Errorf("unsupported schema YAML node %T", node)
	}
	return nil
}

func writeSchemaNumber(buf *bytes.Buffer, literal string, value any) error {
	literal = strings.TrimPrefix(literal, "+")
	if strings.HasPrefix(literal, "-.") {
		literal = "-0" + literal[1:]
	} else if strings.HasPrefix(literal, ".") {
		literal = "0" + literal
	}
	if strings.HasSuffix(literal, ".") {
		literal += "0"
	}
	if json.Valid([]byte(literal)) {
		buf.WriteString(literal)
		return nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("schema number %q: %w", literal, err)
	}
	buf.Write(encoded)
	return nil
}
