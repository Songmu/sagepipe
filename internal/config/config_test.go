package config

import (
	"encoding/json"
	"errors"
	"flag"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func makeDir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
}

func TestParseDefaultsAndPrompt(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, filepath.Join(root, "prompt.md"), "Translate this.\n\n")
	tests := []struct {
		name, config, wantPrompt string
	}{
		{"no config", "", ""},
		{"prompt without frontmatter", "prompt.md", "Translate this.\n\n"},
		{"empty frontmatter", "empty.md", ""},
		{"comment-only frontmatter", "comment.md", "Prompt\n"},
	}
	writeConfig(t, filepath.Join(root, "empty.md"), "---\n---\n")
	writeConfig(t, filepath.Join(root, "comment.md"), "---\n# Metadata\n---\nPrompt\n")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var args []string
			if tt.config != "" {
				args = []string{"--config", tt.config}
			}
			c, err := Parse(args, root)
			if err != nil {
				t.Fatal(err)
			}
			if c.CWD != root || !reflect.DeepEqual(c.Agent, AgentConfig{Provider: "copilot", Protocol: "acp", CWD: root}) ||
				c.Mode != "auto" || c.Prompt != tt.wantPrompt || c.AllowedTools != "" ||
				c.InputSchema.Present || c.OutputSchema.Present || c.Timeout != 0 ||
				c.MaxInputBytes != 65536 || c.MaxLineBytes != 1048576 || c.MaxResponseBytes != 8388608 {
				t.Fatalf("unexpected defaults: %+v", c)
			}
		})
	}
	writeConfig(t, filepath.Join(root, "windows.md"), "---\r\nmode: map\r\n---\r\nPrompt\r\n")
	c, err := Parse([]string{"--config", "windows.md"}, root)
	if err != nil || c.Mode != "map" || c.Prompt != "Prompt\r\n" {
		t.Fatalf("CRLF frontmatter and prompt: %+v, %v", c, err)
	}
}

func TestParsePathsSchemasAndOverrides(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	configDir := filepath.Join(project, "config dir")
	agentDir := filepath.Join(configDir, "agent")
	overrideDir := filepath.Join(project, "override")
	for _, dir := range []string{configDir, agentDir, overrideDir} {
		makeDir(t, dir)
	}
	writeConfig(t, filepath.Join(project, "config.md"), `---
cwd: ./config dir
agent:
  provider: copilot
  protocol: cli
  model: inherited
  cwd: ./config dir/agent
allowed-tools: Read Grep
input_schema: ./input.json
output_schema:
  type: number
  minimum: 9007199254740993
  multipleOf: 0.123456789012345678901
max_input_bytes: 128
max_line_bytes: 256
max_response_bytes: 512
timeout: 2m
concurrency: [invalid, but, ignored]
---

Original prompt.
`)
	tests := []struct {
		name  string
		args  []string
		check func(*testing.T, Config)
	}{
		{
			name: "config paths and inline JSON",
			args: []string{"--config", "project/config.md"},
			check: func(t *testing.T, c Config) {
				t.Helper()
				if c.CWD != configDir || c.Agent.CWD != agentDir ||
					c.Agent.Protocol != "cli" || c.Agent.Model != "inherited" ||
					c.AllowedTools != "Read Grep" || c.Prompt != "\nOriginal prompt.\n" ||
					c.MaxInputBytes != 128 || c.MaxLineBytes != 256 || c.MaxResponseBytes != 512 ||
					c.Timeout != 2*time.Minute {
					t.Fatalf("unexpected file settings: %+v", c)
				}
				if !reflect.DeepEqual(c.InputSchema, SchemaSpec{Present: true, Path: filepath.Join(project, "input.json")}) {
					t.Fatalf("input schema: %+v", c.InputSchema)
				}
				if !c.OutputSchema.Present || c.OutputSchema.Path != "" ||
					c.OutputSchema.BaseURI != directoryURI(project) {
					t.Fatalf("output schema: %+v", c.OutputSchema)
				}
				if !json.Valid(c.OutputSchema.JSON) ||
					!strings.Contains(string(c.OutputSchema.JSON), "9007199254740993") ||
					!strings.Contains(string(c.OutputSchema.JSON), "0.123456789012345678901") {
					t.Fatalf("schema numbers must retain precision: %s", c.OutputSchema.JSON)
				}
			},
		},
		{
			name: "flag paths use effective cwd and clear allowed tools",
			args: []string{
				"--config", "config.md", "-C", "project",
				"--agent", "claude", "--model", "replacement",
				"--agent-cwd", "./override", "--allowed-tools=",
				"--prompt=", "--mode", "reduce", "--timeout", "3s",
				"--input-schema", "./true", "--output-schema", ` {"$ref":"schema.json"} `,
			},
			check: func(t *testing.T, c Config) {
				t.Helper()
				if c.CWD != project || !reflect.DeepEqual(c.Agent, AgentConfig{
					Provider: "claude", Protocol: "cli", Model: "replacement", CWD: overrideDir,
				}) || c.AllowedTools != "" || c.Prompt != "" || c.Mode != "reduce" ||
					c.Timeout != 3*time.Second {
					t.Fatalf("unexpected overrides: %+v", c)
				}
				if c.InputSchema.Path != filepath.Join(project, "true") ||
					c.InputSchema.BaseURI != "" || !c.InputSchema.Present ||
					string(c.OutputSchema.JSON) != `{"$ref":"schema.json"}` ||
					c.OutputSchema.BaseURI != directoryURI(project) {
					t.Fatalf("unexpected schema overrides: %+v %+v", c.InputSchema, c.OutputSchema)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Parse(tt.args, root)
			if err != nil {
				t.Fatal(err)
			}
			tt.check(t, c)
		})
	}

	t.Run("config cwd overridden after loading", func(t *testing.T) {
		c, err := Parse([]string{"--config", "config.md", "-C", "project"}, root)
		if err != nil {
			t.Fatal(err)
		}
		if c.CWD != project {
			t.Fatalf("C should override file cwd: %s", c.CWD)
		}
		if c.OutputSchema.BaseURI != directoryURI(project) {
			t.Fatalf("inline schema refs stay relative to file: %s", c.OutputSchema.BaseURI)
		}
	})
	t.Run("file URI escapes directory names", func(t *testing.T) {
		base := directoryURI(configDir)
		u, err := url.Parse(base)
		if err != nil || u.Scheme != "file" || u.Path != configDir+"/" {
			t.Fatalf("invalid base URI: %q (%v)", base, err)
		}
	})
	t.Run("agent replacement inherits top-level allowed tools", func(t *testing.T) {
		c, err := Parse([]string{"--config", "project/config.md", "--agent", "claude"}, root)
		if err != nil {
			t.Fatal(err)
		}
		if c.AllowedTools != "Read Grep" || !reflect.DeepEqual(c.Agent, AgentConfig{
			Provider: "claude", Protocol: "cli", CWD: configDir,
		}) {
			t.Fatalf("unexpected agent replacement: %+v", c)
		}
	})
	t.Run("CLI inline refs use effective cwd, not config dir", func(t *testing.T) {
		c, err := Parse([]string{
			"--config", "project/config.md", "--output-schema", `{"$ref":"local.json"}`,
		}, root)
		if err != nil {
			t.Fatal(err)
		}
		u, err := url.Parse(c.OutputSchema.BaseURI)
		if err != nil {
			t.Fatal(err)
		}
		ref, _ := url.Parse("local.json")
		if got := u.ResolveReference(ref).Path; got != filepath.Join(configDir, "local.json") {
			t.Fatalf("CLI schema reference resolved to %s", got)
		}
	})
	t.Run("file cwd can be overridden when its directory is absent", func(t *testing.T) {
		path := filepath.Join(project, "missing-dir.md")
		writeConfig(t, path, "---\ncwd: ./missing\n---\n")
		c, err := Parse([]string{"--config", "missing-dir.md", "-C", "project"}, root)
		if err != nil || c.CWD != project || c.Agent.CWD != project {
			t.Fatalf("override failed: %+v, %v", c, err)
		}
	})
}

func TestParseCustomAgent(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "settings")
	agentDir := filepath.Join(configDir, "work")
	makeDir(t, agentDir)
	writeConfig(t, filepath.Join(configDir, "pipe.md"), `---
agent:
  protocol: acp
  command: ./bin/agent-acp
  args: [--stdio, ./literal-path]
  cwd: ./work
---
`)
	c, err := Parse([]string{"--config", "settings/pipe.md"}, root)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.Agent, AgentConfig{
		Protocol: "acp", Command: filepath.Join(configDir, "bin/agent-acp"),
		Args: []string{"--stdio", "./literal-path"}, CWD: agentDir,
	}) {
		t.Fatalf("custom agent: %+v", c.Agent)
	}
	replaced, err := Parse([]string{"--config", "settings/pipe.md", "--agent", "codex"}, root)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(replaced.Agent, AgentConfig{Provider: "codex", Protocol: "cli", CWD: root}) {
		t.Fatalf("agent replacement retained custom fields: %+v", replaced.Agent)
	}
	copilotCLI, err := Parse([]string{"--agent", "copilot", "--protocol", "cli"}, root)
	if err != nil || copilotCLI.Agent.Protocol != "cli" {
		t.Fatalf("explicit Copilot CLI: %+v, %v", copilotCLI.Agent, err)
	}
}

func TestParseSchemaForms(t *testing.T) {
	root := t.TempDir()
	for _, tt := range []struct {
		name, yamlValue string
		isPath          bool
		json            string
	}{
		{"false YAML", "false", false, "false"},
		{"true YAML", "true", false, "true"},
		{"string true is a path", `"true"`, true, ""},
		{"string object is a path", `'{"type":"string"}'`, true, ""},
		{"inline mapping", "{type: object}", false, `{"type":"object"}`},
		{"nested numbers and strings", `{properties: {n: {minimum: 0.123456789012345678901, maximum: 1.234567890123456789e20}}, title: "0.1"}`, false, `{"properties":{"n":{"minimum":0.123456789012345678901,"maximum":1.234567890123456789e20}},"title":"0.1"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			writeConfig(t, filepath.Join(root, "schema.md"), "---\noutput_schema: "+tt.yamlValue+"\n---\n")
			c, err := Parse([]string{"--config", "schema.md"}, root)
			if err != nil {
				t.Fatal(err)
			}
			t.Run("large CLI schema numbers are not parsed as float64", func(t *testing.T) {
				raw := `{"minimum":1e1000}`
				c, err := Parse([]string{"--input-schema", raw}, root)
				if err != nil || string(c.InputSchema.JSON) != raw {
					t.Fatalf("lost number precision: %q, %v", c.InputSchema.JSON, err)
				}
			})
			s := c.OutputSchema
			if !s.Present {
				t.Fatal("schema was not marked present")
			}
			if tt.isPath {
				if s.Path != filepath.Join(root, strings.Trim(tt.yamlValue, `"'`)) || s.JSON != nil {
					t.Fatalf("expected path: %+v", s)
				}
			} else if string(s.JSON) != tt.json || s.BaseURI != directoryURI(root) {
				t.Fatalf("expected inline JSON %q: %+v", tt.json, s)
			}
		})
	}
	for _, tt := range []struct {
		name, value string
		isPath      bool
		json        string
	}{
		{"JSON false", " false ", false, "false"},
		{"JSON true", "true", false, "true"},
		{"JSON object", `{"type":"object"}`, false, `{"type":"object"}`},
		{"boolean filename", "./false", true, ""},
		{"array filename", "[schema]", true, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Parse([]string{"--input-schema", tt.value}, root)
			if err != nil {
				t.Fatal(err)
			}
			s := c.InputSchema
			if !s.Present {
				t.Fatal("schema was not marked present")
			}
			if tt.isPath {
				if s.Path != filepath.Join(root, tt.value) || s.JSON != nil || s.BaseURI != "" {
					t.Fatalf("expected schema path: %+v", s)
				}
			} else if string(s.JSON) != tt.json || s.BaseURI != directoryURI(root) {
				t.Fatalf("expected inline JSON %q: %+v", tt.json, s)
			}
		})
	}
}

func TestParseRejectsInvalidOptions(t *testing.T) {
	root := t.TempDir()
	makeDir(t, filepath.Join(root, "directory"))
	writeConfig(t, filepath.Join(root, "file"), "not a directory")
	tests := []struct {
		name, front string
		args        []string
		want        string
	}{
		{"no concurrency flag", "", []string{"--concurrency", "2"}, "flag provided but not defined"},
		{"positional", "", []string{"extra"}, "unexpected positional"},
		{"unknown provider flag", "", []string{"--agent", "other"}, "unsupported provider"},
		{"claude acp", "", []string{"--agent", "claude", "--protocol", "acp"}, "combination"},
		{"codex acp", "", []string{"--agent", "codex", "--protocol", "acp"}, "combination"},
		{"unknown protocol", "", []string{"--protocol", "something"}, "combination"},
		{"unknown file protocol even when overridden", "agent: {provider: copilot, protocol: other}", []string{"--agent", "claude"}, "protocol: unsupported"},
		{"custom cli", "agent: {protocol: cli, command: my-agent}", nil, "custom agents require"},
		{"custom missing protocol", "agent: {command: my-agent}", nil, "require protocol"},
		{"custom missing command", "agent: {protocol: acp}", nil, "require command"},
		{"custom protocol override", "agent: {protocol: acp, command: my-agent}", []string{"--protocol", "cli"}, "cannot override"},
		{"custom allowed tools", "agent: {protocol: acp, command: my-agent}\nallowed-tools: Read", nil, "allowed-tools"},
		{"custom allowed tools cleared", "agent: {protocol: acp, command: my-agent}\nallowed-tools: Read", []string{"--allowed-tools="}, ""},
		{"builtin custom command", "agent: {provider: copilot, command: custom}", nil, "command is only"},
		{"builtin args", "agent: {provider: claude, args: []}", nil, "args are only"},
		{"bad agent name", "agent: invalid", nil, "unsupported provider"},
		{"bad agent type", "agent: [copilot]", nil, "expected a provider"},
		{"bad agent args type", "agent: {protocol: acp, command: custom, args: [12]}", nil, "args"},
		{"unknown agent key", "agent: {provider: copilot, modle: wrong}", nil, "unknown field"},
		{"mode type", "mode: true", nil, "mode: expected a string"},
		{"mode value", "mode: unknown", nil, "mode: unsupported"},
		{"invalid file mode even when overridden", "mode: wrong", []string{"--mode", "map"}, "mode: unsupported"},
		{"null mode", "mode: null", nil, "null is not allowed"},
		{"allowed tools type", "allowed-tools: [Read]", nil, "allowed-tools: expected a string"},
		{"cwd type", "cwd: 42", nil, "cwd: expected a string"},
		{"cwd empty", `cwd: ""`, nil, "cwd must be"},
		{"cwd missing", "cwd: ./missing", nil, "cwd"},
		{"cwd file", "cwd: ./file", nil, "not a directory"},
		{"agent cwd missing", "agent: {provider: copilot, cwd: ./missing}", nil, "agent.cwd"},
		{"negative limit file", "max_input_bytes: -1", nil, "max_input_bytes must be positive"},
		{"zero limit file", "max_line_bytes: 0", nil, "max_line_bytes must be positive"},
		{"fractional limit file", "max_response_bytes: 1.5", nil, "expected an integer"},
		{"integral float limit file", "max_input_bytes: 1.0", nil, "expected an integer"},
		{"string limit file", `max_input_bytes: "10"`, nil, "expected an integer"},
		{"overflow limit file", "max_input_bytes: 9223372036854775808", nil, "expected an integer"},
		{"negative limit flag", "", []string{"--max-input-bytes=-1"}, "max_input_bytes must be positive"},
		{"zero limit flag", "", []string{"--max-response-bytes=0"}, "max_response_bytes must be positive"},
		{"invalid limit flag", "", []string{"--max-line-bytes=nope"}, "invalid value"},
		{"timeout type", "timeout: 1", nil, "timeout: expected a string"},
		{"timeout invalid file", "timeout: tomorrow", nil, "timeout:"},
		{"timeout zero file", "timeout: 0s", nil, "must be positive"},
		{"timeout negative flag", "", []string{"--timeout=-1s"}, "must be positive"},
		{"timeout empty flag", "", []string{"--timeout="}, "invalid duration"},
		{"bad schema array", "input_schema: [string]", nil, "expected a schema object"},
		{"bad schema null", "output_schema: null", nil, "expected a schema object"},
		{"bad schema number", "output_schema: 42", nil, "expected a schema object"},
		{"bad schema string", `output_schema: ""`, nil, "nonempty"},
		{"invalid inline JSON", "", []string{"--output-schema", `{"type":`}, "invalid JSON schema"},
		{"empty schema CLI", "", []string{"--input-schema="}, "nonempty"},
		{"missing C", "", []string{"-C", "missing"}, "-C"},
		{"C is file", "", []string{"-C", "file"}, "not a directory"},
		{"empty C", "", []string{"-C="}, "-C"},
		{"empty config", "", []string{"--config="}, "--config"},
		{"empty agent cwd", "", []string{"--agent-cwd="}, "--agent-cwd"},
		{"empty model", "", []string{"--model="}, "--model"},
		{"version delegated to root", "", []string{"--version"}, "flag provided but not defined"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.front != "" {
				writeConfig(t, filepath.Join(root, "config.md"), "---\n"+tt.front+"\n---\n")
			} else {
				writeConfig(t, filepath.Join(root, "config.md"), "---\n---\n")
			}
			args := tt.args
			if tt.front != "" {
				args = append([]string{"--config", "config.md"}, args...)
			}
			_, err := Parse(args, root)
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Parse(%q): got %v; want error containing %q", args, err, tt.want)
			}
		})
	}
}

func TestParseRejectsInvalidFilesAndUTF8(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{"missing closing marker", []byte("---\nmode: map\n"), "closing ---"},
		{"bad YAML", []byte("---\nmode: [\n---\n"), "frontmatter"},
		{"nonmapping frontmatter", []byte("---\n[mode, map]\n---\n"), "frontmatter"},
		{"invalid UTF-8 in prompt", []byte{'a', 0xff}, "invalid UTF-8"},
		{"invalid UTF-8 in frontmatter", []byte("---\nmode: \xff\n---\n"), "invalid UTF-8"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writeConfig(t, filepath.Join(root, "bad.md"), string(tt.data))
			_, err := Parse([]string{"--config", "bad.md"}, root)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v; want error containing %q", err, tt.want)
			}
		})
	}
	if _, err := Parse([]string{"--prompt", string([]byte{0xff})}, root); err == nil {
		t.Fatal("invalid UTF-8 in CLI prompt was accepted")
	}
	if _, err := Parse([]string{"--output-schema", string([]byte{0xff})}, root); err == nil {
		t.Fatal("invalid UTF-8 in CLI schema was accepted")
	}
	if _, err := Parse([]string{"-h"}, root); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help should preserve flag.ErrHelp: %v", err)
	}
}

func TestParseHelpDoesNotWriteGlobally(t *testing.T) {
	const child = "SAGEPIPE_CONFIG_HELP_TEST_CHILD"
	if os.Getenv(child) == "1" {
		for _, arg := range []string{"-h", "--help"} {
			if _, err := Parse([]string{arg}, t.TempDir()); err != flag.ErrHelp {
				t.Fatalf("Parse(%q) = %v, want flag.ErrHelp", arg, err)
			}
		}
		_ = Usage()
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestParseHelpDoesNotWriteGlobally$")
	cmd.Env = append(os.Environ(), child+"=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("help subprocess: %v\n%s", err, output)
	}
	if string(output) != "PASS\n" {
		t.Fatalf("help wrote to process stdout or stderr: %q", output)
	}
}

func TestUsage(t *testing.T) {
	usage := Usage()
	if !strings.HasPrefix(usage, "Usage: sagepipe [options] < stdin > stdout\n\nOptions:\n") {
		t.Fatalf("unexpected usage header: %q", usage)
	}
	for _, name := range []string{"-C", "-h", "--help", "--config", "--agent", "--input-schema", "--output-schema"} {
		if !strings.Contains(usage, "  "+name+" ") {
			t.Errorf("usage omits %s: %s", name, usage)
		}
	}
	for _, name := range []string{"--version", "--concurrency"} {
		if strings.Contains(usage, name) {
			t.Errorf("usage includes unsupported %s", name)
		}
	}
	newFlagSet(new(flagOptions)).VisitAll(func(f *flag.Flag) {
		prefix := "--"
		if len(f.Name) == 1 {
			prefix = "-"
		}
		count := 0
		for _, line := range strings.Split(usage, "\n") {
			if strings.HasPrefix(line, "  "+prefix+f.Name+" ") && strings.HasSuffix(line, f.Usage) {
				count++
			}
		}
		if count != 1 {
			t.Errorf("usage lists %s (%q) %d times, want once", prefix+f.Name, f.Usage, count)
		}
	})
}
