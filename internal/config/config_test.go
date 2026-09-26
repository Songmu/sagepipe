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
			if c.CWD != root || !reflect.DeepEqual(c.Agent, AgentConfig{Provider: "copilot", Protocol: "cli", CWD: root}) ||
				c.Mode != "auto" || c.Prompt != tt.wantPrompt || c.AllowedTools != "" ||
				c.InputSchema.Present || c.OutputSchema.Present || c.Timeout != 0 ||
				c.Verbosity != 0 ||
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

func TestParseVerbosity(t *testing.T) {
	root := t.TempDir()
	for _, tt := range []struct {
		name, wantPrompt string
		args             []string
		want             int
	}{
		{name: "default"},
		{name: "short", args: []string{"-v"}, want: 1},
		{name: "long", args: []string{"--verbose"}, want: 1},
		{name: "repeated", args: []string{"-v", "--verbose"}, want: 2},
		{name: "grouped", args: []string{"-vv"}, want: 2},
		{name: "grouped and repeated", args: []string{"-vv", "-v"}, want: 3},
		{name: "longer group", args: []string{"-vvv"}, want: 3},
		{name: "explicit false", args: []string{"-v", "--verbose=false"}},
		{name: "option value", args: []string{"--prompt", "-vv"}, wantPrompt: "-vv"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Parse(tt.args, root)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Verbosity != tt.want {
				t.Fatalf("verbosity = %d, want %d", cfg.Verbosity, tt.want)
			}
			if cfg.Prompt != tt.wantPrompt {
				t.Fatalf("prompt = %q, want %q", cfg.Prompt, tt.wantPrompt)
			}
		})
	}
	if _, err := Parse([]string{"-vx"}, root); err == nil {
		t.Fatal("unexpected combined flag was accepted")
	}
}

func TestParseIgnoreFailures(t *testing.T) {
	root := t.TempDir()
	for _, tt := range []struct {
		name string
		args []string
		want bool
	}{
		{"default", nil, false},
		{"short", []string{"-i"}, true},
		{"long", []string{"--ignore-failures"}, true},
		{"explicit false", []string{"--ignore-failures", "-i=false"}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Parse(tt.args, root)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.IgnoreFailures != tt.want {
				t.Fatalf("IgnoreFailures = %t, want %t", cfg.IgnoreFailures, tt.want)
			}
		})
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
  args: [--disable-builtin-mcps, --disable-mcp-server=workiq]
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
custom_metadata: [invalid, but, ignored]
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
					!reflect.DeepEqual(c.Agent.Args, []string{"--disable-builtin-mcps", "--disable-mcp-server=workiq"}) ||
					c.AllowedTools != "Read Grep" || c.Prompt != "\nOriginal prompt.\n" ||
					c.MaxInputBytes != 128 || c.MaxLineBytes != 256 || c.MaxResponseBytes != 512 ||
					c.Timeout != 2*time.Minute || c.Verbosity != 0 {
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
				"-vv",
				"--input-schema", "./true", "--output-schema", ` {"$ref":"schema.json"} `,
			},
			check: func(t *testing.T, c Config) {
				t.Helper()
				if c.CWD != project || !reflect.DeepEqual(c.Agent, AgentConfig{
					Provider: "claude", Protocol: "cli", Model: "replacement", CWD: "./override",
				}) || c.AllowedTools != "" || c.Prompt != "" || c.Mode != "reduce" ||
					c.Timeout != 3*time.Second || c.Verbosity != 2 {
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
		if c.Agent.CWD != agentDir {
			t.Fatalf("C should not override explicit agent cwd: %s", c.Agent.CWD)
		}
		if c.OutputSchema.BaseURI != directoryURI(project) {
			t.Fatalf("inline schema refs stay relative to file: %s", c.OutputSchema.BaseURI)
		}
	})
	t.Run("long cwd flag", func(t *testing.T) {
		c, err := Parse([]string{"--cwd", "project"}, root)
		if err != nil {
			t.Fatal(err)
		}
		if c.CWD != project || c.Agent.CWD != project {
			t.Fatalf("--cwd should set cwd and the inherited agent cwd: %+v", c)
		}
	})
	t.Run("last CLI cwd locates config and overrides file cwd", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			args []string
			want string
		}{
			{"last short flag", []string{"--cwd", "project", "-C", "."}, root},
			{"last long flag", []string{"-C", ".", "--cwd", "project"}, project},
		} {
			t.Run(tc.name, func(t *testing.T) {
				writeConfig(t, filepath.Join(tc.want, "cwd.md"), "---\ncwd: ./missing\n---\n")
				c, err := Parse(append(tc.args, "--config", "cwd.md"), root)
				if err != nil {
					t.Fatal(err)
				}
				if c.CWD != tc.want || c.Agent.CWD != tc.want {
					t.Fatalf("CLI cwd should locate the file and override its cwd: %+v", c)
				}
			})
		}
	})
	t.Run("file URI escapes directory names", func(t *testing.T) {
		base := directoryURI(configDir)
		u, err := url.Parse(base)
		wantPath := filepath.ToSlash(configDir)
		if !strings.HasPrefix(wantPath, "/") {
			wantPath = "/" + wantPath
		}
		if err != nil || u.Scheme != "file" || u.Path != wantPath+"/" {
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
		wantPath := filepath.ToSlash(filepath.Join(configDir, "local.json"))
		if !strings.HasPrefix(wantPath, "/") {
			wantPath = "/" + wantPath
		}
		if got := u.ResolveReference(ref).Path; got != wantPath {
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
	copilotACP, err := Parse([]string{"--agent", "copilot", "--protocol", "acp"}, root)
	if err != nil || copilotACP.Agent.Protocol != "acp" {
		t.Fatalf("explicit Copilot ACP: %+v, %v", copilotACP.Agent, err)
	}
	for _, front := range []string{"agent: copilot", "agent: {provider: copilot}"} {
		writeConfig(t, filepath.Join(root, "copilot.md"), "---\n"+front+"\n---\n")
		copilotDefault, err := Parse([]string{"--config", "copilot.md"}, root)
		if err != nil || copilotDefault.Agent.Protocol != "cli" {
			t.Fatalf("default Copilot protocol for %q: %+v, %v", front, copilotDefault.Agent, err)
		}
	}
}

func TestParseSchemaForms(t *testing.T) {
	root := t.TempDir()
	for _, tt := range []struct {
		name, yamlValue, cliValue, path, json string
	}{
		{name: "false YAML", yamlValue: "false", json: "false"},
		{name: "true YAML", yamlValue: "true", json: "true"},
		{name: "string true is a path", yamlValue: `"true"`, path: "true"},
		{name: "string object is a path", yamlValue: `'{"type":"string"}'`, path: `{"type":"string"}`},
		{name: "inline mapping", yamlValue: "{type: object}", json: `{"type":"object"}`},
		{name: "nested numbers and strings", yamlValue: `{properties: {n: {minimum: 0.123456789012345678901, maximum: 1.234567890123456789e20}}, title: "0.1"}`, json: `{"properties":{"n":{"minimum":0.123456789012345678901,"maximum":1.234567890123456789e20}},"title":"0.1"}`},
		{name: "JSON false", cliValue: " false ", json: "false"},
		{name: "JSON true", cliValue: "true", json: "true"},
		{name: "JSON object", cliValue: `{"type":"object"}`, json: `{"type":"object"}`},
		{name: "boolean filename", cliValue: "./false", path: "./false"},
		{name: "array filename", cliValue: "[schema]", path: "[schema]"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var args []string
			if tt.yamlValue != "" {
				writeConfig(t, filepath.Join(root, "schema.md"), "---\noutput_schema: "+tt.yamlValue+"\n---\n")
				args = []string{"--config", "schema.md"}
			} else {
				args = []string{"--input-schema", tt.cliValue}
			}
			c, err := Parse(args, root)
			if err != nil {
				t.Fatal(err)
			}
			s := c.InputSchema
			if tt.yamlValue != "" {
				s = c.OutputSchema
			}
			want := SchemaSpec{Present: true}
			if tt.path != "" {
				want.Path = filepath.Join(root, tt.path)
			} else {
				want.JSON = []byte(tt.json)
				want.BaseURI = directoryURI(root)
			}
			if !reflect.DeepEqual(s, want) {
				t.Fatalf("schema = %+v, want %+v", s, want)
			}
		})
	}
	t.Run("large CLI schema numbers are not parsed as float64", func(t *testing.T) {
		raw := `{"minimum":1e1000}`
		c, err := Parse([]string{"--input-schema", raw}, root)
		if err != nil || string(c.InputSchema.JSON) != raw {
			t.Fatalf("lost number precision: %q, %v", c.InputSchema.JSON, err)
		}
	})
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
		{"negative concurrency file", "concurrency: -1", nil, "concurrency must be positive"},
		{"zero concurrency file", "concurrency: 0", nil, "concurrency must be positive"},
		{"fractional concurrency file", "concurrency: 1.5", nil, "concurrency: expected an integer"},
		{"string concurrency file", `concurrency: "2"`, nil, "concurrency: expected an integer"},
		{"concurrency overflow file", "concurrency: 9223372036854775808", nil, "concurrency: expected an integer"},
		{"invalid concurrency file despite override", "concurrency: wrong", []string{"--concurrency", "2"}, "concurrency: expected an integer"},
		{"zero concurrency flag", "", []string{"--concurrency=0"}, "concurrency must be positive"},
		{"negative concurrency flag", "", []string{"--concurrency=-1"}, "concurrency must be positive"},
		{"invalid concurrency flag", "", []string{"--concurrency=abc"}, "invalid value"},
		{"reduce concurrency file", "mode: reduce\nconcurrency: 2", nil, "concurrency must be 1 in reduce mode"},
		{"reduce concurrency flag", "mode: reduce", []string{"--concurrency=2"}, "concurrency must be 1 in reduce mode"},
		{"reduce concurrency overridden", "mode: reduce\nconcurrency: 2", []string{"--concurrency=1"}, ""},
		{"auto concurrency", "mode: auto\nconcurrency: 2", nil, ""},
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
		{"builtin args", "agent: {provider: claude, args: [--verbose]}", nil, ""},
		{"builtin end of options", "agent: {provider: copilot, args: [--]}", nil, `"--" is not supported`},
		{"custom end of options", "agent: {protocol: acp, command: my-agent, args: [--]}", nil, ""},
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
		{"missing cwd", "", []string{"--cwd", "missing"}, "--cwd"},
		{"empty cwd", "", []string{"--cwd="}, "--cwd"},
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

func TestConcurrencyPrecedence(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, filepath.Join(root, "config.md"), "---\nmode: map\nconcurrency: 4\n---\n")
	for _, tt := range []struct {
		args []string
		want int
	}{
		{nil, 1},
		{[]string{"--config", "config.md"}, 4},
		{[]string{"--config", "config.md", "--concurrency", "2"}, 2},
		{[]string{"--concurrency", "3"}, 3},
	} {
		c, err := Parse(tt.args, root)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tt.args, err)
		}
		if c.Concurrency != tt.want {
			t.Errorf("Parse(%q): concurrency = %d, want %d", tt.args, c.Concurrency, tt.want)
		}
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
	for _, option := range []string{"--prompt", "--output-schema"} {
		t.Run("invalid UTF-8 in "+option, func(t *testing.T) {
			if _, err := Parse([]string{option, string([]byte{0xff})}, root); err == nil {
				t.Fatalf("invalid UTF-8 in %s was accepted", option)
			}
		})
	}
	if _, err := Parse([]string{"-h"}, root); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help should preserve flag.ErrHelp: %v", err)
	}
}

func TestParseHelpDoesNotWriteGlobally(t *testing.T) {
	const child = "SAGEPIPE_CONFIG_HELP_TEST_CHILD"
	if os.Getenv(child) == "1" {
		for _, arg := range []string{"-h", "--help"} {
			if _, err := Parse([]string{arg}, os.TempDir()); err != flag.ErrHelp {
				t.Fatalf("Parse(%q) = %v, want flag.ErrHelp", arg, err)
			}
		}
		_ = Usage()
		os.Exit(0) // Suppress the test runner's own PASS and coverage output.
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestParseHelpDoesNotWriteGlobally$")
	cmd.Env = append(os.Environ(), child+"=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("help subprocess: %v\n%s", err, output)
	}
	if len(output) != 0 {
		t.Fatalf("help wrote to process stdout or stderr: %q", output)
	}
}

func TestUsage(t *testing.T) {
	usage := Usage()
	if !strings.HasPrefix(usage, "Usage: sagepipe [options] < stdin > stdout\n\nOptions:\n") {
		t.Fatalf("unexpected usage header: %q", usage)
	}
	for _, name := range []string{"-C", "--cwd", "-h", "--help", "-i", "--ignore-failures", "--config", "--agent", "--input-schema", "--output-schema"} {
		if !strings.Contains(usage, "  "+name+" ") {
			t.Errorf("usage omits %s: %s", name, usage)
		}
	}
	for _, name := range []string{"--version"} {
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
		for line := range strings.SplitSeq(usage, "\n") {
			if strings.HasPrefix(line, "  "+prefix+f.Name+" ") && strings.HasSuffix(line, f.Usage) {
				count++
			}
		}
		if count != 1 {
			t.Errorf("usage lists %s (%q) %d times, want once", prefix+f.Name, f.Usage, count)
		}
	})
}
