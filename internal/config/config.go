// Package config parses sagepipe's Markdown configuration and command-line options.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/goccy/go-yaml"
)

// SchemaSpec identifies either a schema file or an inline JSON schema.
// BaseURI is the containing directory's file URI for an inline schema.
type SchemaSpec struct {
	Present bool
	Path    string
	JSON    []byte
	BaseURI string
}

type AgentConfig struct {
	Provider, Protocol, Model, Command, CWD string
	Args                                    []string
}

type Config struct {
	Agent                                         AgentConfig
	CWD, Prompt, Mode, AllowedTools               string
	InputSchema, OutputSchema                     SchemaSpec
	MaxInputBytes, MaxLineBytes, MaxResponseBytes int64
	Timeout                                       time.Duration
	Concurrency, Verbosity                        int
	IgnoreFailures                                bool
}

type flagOptions struct {
	configPath, directory, agent, protocol, model, agentCWD string
	mode, prompt, allowedTools, inputSchema, outputSchema   string
	timeout                                                 string
	maxInputBytes, maxLineBytes, maxResponseBytes           int64
	concurrency, verbosity                                  int
	help, ignoreFailures                                    bool
}

type verbosityFlag struct {
	count *int
	step  int
}

func (v verbosityFlag) String() string {
	if v.count == nil {
		return "0"
	}
	return strconv.Itoa(*v.count)
}

func (v verbosityFlag) Set(value string) error {
	enabled, err := strconv.ParseBool(value)
	if err != nil {
		return err
	}
	if enabled {
		*v.count += v.step
	} else {
		*v.count = 0
	}
	return nil
}

func (verbosityFlag) IsBoolFlag() bool { return true }

func newFlagSet(o *flagOptions) *flag.FlagSet {
	fs := flag.NewFlagSet("sagepipe", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.configPath, "config", "", "Markdown configuration file")
	fs.StringVar(&o.directory, "C", "", "working directory")
	fs.StringVar(&o.directory, "cwd", "", "working directory")
	fs.StringVar(&o.agent, "agent", "", "agent provider")
	fs.StringVar(&o.protocol, "protocol", "", "agent protocol")
	fs.StringVar(&o.model, "model", "", "agent model")
	fs.StringVar(&o.agentCWD, "agent-cwd", "", "agent working directory")
	fs.StringVar(&o.mode, "mode", "", "map, reduce, or auto")
	fs.IntVar(&o.concurrency, "concurrency", 0, "maximum concurrent map requests")
	fs.StringVar(&o.prompt, "prompt", "", "transformation prompt")
	fs.StringVar(&o.allowedTools, "allowed-tools", "", "agent tools")
	fs.StringVar(&o.inputSchema, "input-schema", "", "input schema")
	fs.StringVar(&o.outputSchema, "output-schema", "", "output schema")
	fs.Int64Var(&o.maxInputBytes, "max-input-bytes", 0, "maximum reduce input bytes")
	fs.Int64Var(&o.maxLineBytes, "max-line-bytes", 0, "maximum map line bytes")
	fs.Int64Var(&o.maxResponseBytes, "max-response-bytes", 0, "maximum response bytes")
	fs.StringVar(&o.timeout, "timeout", "", "agent call timeout")
	const ignoreFailuresDescription = "exit 0 after writing any valid output records, even on failure"
	fs.BoolVar(&o.ignoreFailures, "i", false, ignoreFailuresDescription)
	fs.BoolVar(&o.ignoreFailures, "ignore-failures", false, ignoreFailuresDescription)
	const verboseDescription = "include INFO diagnostics; repeat for DEBUG"
	fs.Var(verbosityFlag{&o.verbosity, 1}, "v", verboseDescription)
	fs.Var(verbosityFlag{&o.verbosity, 1}, "verbose", verboseDescription)
	const helpDescription = "display usage"
	fs.BoolVar(&o.help, "h", false, helpDescription)
	fs.BoolVar(&o.help, "help", false, helpDescription)
	return fs
}

// Usage returns CLI help text without writing to stdout or stderr.
func Usage() string {
	var b strings.Builder
	b.WriteString("Usage: sagepipe [options] < stdin > stdout\n\nOptions:\n")
	newFlagSet(new(flagOptions)).VisitAll(func(f *flag.Flag) {
		prefix := "--"
		if len(f.Name) == 1 {
			prefix = "-"
		}
		fmt.Fprintf(&b, "  %-24s %s\n", prefix+f.Name, f.Usage)
	})
	return b.String()
}

// Parse resolves config-file paths relative to the config file and CLI paths
// relative to the effective working directory. It does not change process cwd.
func Parse(argv []string, startupCWD string) (Config, error) {
	var empty Config
	if !utf8.ValidString(startupCWD) || startupCWD == "" {
		return empty, errors.New("startup cwd must be a valid UTF-8 directory")
	}
	for _, arg := range argv {
		if !utf8.ValidString(arg) {
			return empty, errors.New("arguments: invalid UTF-8")
		}
	}
	startupCWD, err := filepath.Abs(startupCWD)
	if err != nil {
		return empty, fmt.Errorf("startup cwd: %w", err)
	}

	var opts flagOptions
	fs := newFlagSet(&opts)
	for _, arg := range argv {
		name, _, _ := strings.Cut(strings.TrimPrefix(arg, "-"), "=")
		if len(name) > 1 && strings.Trim(name, "v") == "" && fs.Lookup(name) == nil {
			fs.Var(verbosityFlag{&opts.verbosity, len(name)}, name, "")
		}
	}
	if err := fs.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return empty, flag.ErrHelp
		}
		return empty, fmt.Errorf("arguments: %w", err)
	}
	if opts.help {
		return empty, flag.ErrHelp
	}
	if fs.NArg() != 0 {
		return empty, fmt.Errorf("unexpected positional argument %q", fs.Arg(0))
	}
	set := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	cliCWD, err := cliWorkingDirectory(startupCWD, opts.directory, set)
	if err != nil {
		return empty, err
	}
	c, err := loadConfig(cliCWD, opts.configPath, set["config"], opts.verbosity)
	if err != nil {
		return empty, err
	}
	if err := applyOverrides(&c, cliCWD, opts, set); err != nil {
		return empty, err
	}
	if err := validate(&c); err != nil {
		return empty, err
	}
	return c, nil
}

func cliWorkingDirectory(startupCWD, directory string, set map[string]bool) (string, error) {
	flagName := ""
	if set["C"] {
		flagName = "-C"
	}
	if set["cwd"] {
		flagName = "--cwd"
	}
	if flagName == "" {
		return startupCWD, nil
	}
	if err := validPath(directory, flagName); err != nil {
		return "", err
	}
	cwd := resolve(startupCWD, directory)
	if err := requireDirectory(cwd, flagName); err != nil {
		return "", err
	}
	return cwd, nil
}

func loadConfig(cliCWD, configPath string, hasConfig bool, verbosity int) (Config, error) {
	c := Config{
		Agent:            AgentConfig{Provider: "copilot", Protocol: "cli"},
		CWD:              cliCWD,
		Mode:             "auto",
		Concurrency:      1,
		MaxInputBytes:    65536,
		MaxLineBytes:     1048576,
		MaxResponseBytes: 8388608,
		Verbosity:        verbosity,
	}
	if hasConfig {
		if err := validPath(configPath, "--config"); err != nil {
			return Config{}, err
		}
		path := resolve(cliCWD, configPath)
		data, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("config %s: %w", path, err)
		}
		if !utf8.Valid(data) {
			return Config{}, fmt.Errorf("config %s: invalid UTF-8", path)
		}
		if err := parseFile(&c, data, filepath.Dir(path)); err != nil {
			return Config{}, fmt.Errorf("config %s: %w", path, err)
		}
	}
	return c, nil
}

func applyOverrides(c *Config, cliCWD string, opts flagOptions, set map[string]bool) error {
	c.IgnoreFailures = opts.ignoreFailures
	if set["C"] || set["cwd"] {
		c.CWD = cliCWD
	}
	if set["agent"] {
		if !builtin(opts.agent) {
			return fmt.Errorf("--agent: unsupported provider %q", opts.agent)
		}
		c.Agent = AgentConfig{Provider: opts.agent, Protocol: defaultProtocol(opts.agent)}
	}
	if set["protocol"] {
		if c.Agent.Provider == "" {
			return errors.New("--protocol cannot override a custom agent")
		}
		c.Agent.Protocol = opts.protocol
	}
	if set["model"] {
		if opts.model == "" {
			return errors.New("--model must not be empty")
		}
		c.Agent.Model = opts.model
	}
	if set["agent-cwd"] {
		if err := validPath(opts.agentCWD, "--agent-cwd"); err != nil {
			return err
		}
		c.Agent.CWD = opts.agentCWD
	}
	if set["mode"] {
		c.Mode = opts.mode
	}
	if set["concurrency"] {
		c.Concurrency = opts.concurrency
	}
	if set["prompt"] {
		c.Prompt = opts.prompt
	}
	if set["allowed-tools"] {
		c.AllowedTools = opts.allowedTools
	}
	if set["input-schema"] {
		spec, err := schemaFromCLI(opts.inputSchema, c.CWD)
		if err != nil {
			return fmt.Errorf("--input-schema: %w", err)
		}
		c.InputSchema = spec
	}
	if set["output-schema"] {
		spec, err := schemaFromCLI(opts.outputSchema, c.CWD)
		if err != nil {
			return fmt.Errorf("--output-schema: %w", err)
		}
		c.OutputSchema = spec
	}
	if set["max-input-bytes"] {
		c.MaxInputBytes = opts.maxInputBytes
	}
	if set["max-line-bytes"] {
		c.MaxLineBytes = opts.maxLineBytes
	}
	if set["max-response-bytes"] {
		c.MaxResponseBytes = opts.maxResponseBytes
	}
	if set["timeout"] {
		timeout, err := parseTimeout(opts.timeout)
		if err != nil {
			return fmt.Errorf("--timeout: %w", err)
		}
		c.Timeout = timeout
	}
	return nil
}

func parseFile(c *Config, data []byte, dir string) error {
	front, body, err := frontmatter(data)
	if err != nil {
		return err
	}
	c.Prompt = string(body)
	if front == nil || onlyComments(front) {
		return nil
	}
	var fields map[string]yaml.RawMessage
	if err := yaml.Unmarshal(front, &fields); err != nil {
		return fmt.Errorf("frontmatter: %w", err)
	}
	if fields == nil {
		return errors.New("frontmatter must be a mapping")
	}

	if raw, ok := fields["agent"]; ok {
		c.Agent, err = parseAgent(raw, dir)
		if err != nil {
			return fmt.Errorf("agent: %w", err)
		}
	}
	if v, ok, err := stringField(fields, "cwd"); err != nil {
		return err
	} else if ok {
		if err := validPath(v, "cwd"); err != nil {
			return err
		}
		c.CWD = resolve(dir, v)
	}
	if v, ok, err := stringField(fields, "mode"); err != nil {
		return err
	} else if ok {
		if !validMode(v) {
			return fmt.Errorf("mode: unsupported value %q", v)
		}
		c.Mode = v
	}
	if raw, ok := fields["concurrency"]; ok {
		if err := decodeYAML(raw, &c.Concurrency); err != nil {
			return fmt.Errorf("concurrency: expected an integer: %w", err)
		}
		if c.Concurrency <= 0 {
			return errors.New("concurrency must be positive")
		}
	}
	if v, ok, err := stringField(fields, "allowed-tools"); err != nil {
		return err
	} else if ok {
		c.AllowedTools = v
	}
	if raw, ok := fields["input_schema"]; ok {
		spec, err := schemaFromYAML(raw, dir)
		if err != nil {
			return fmt.Errorf("input_schema: %w", err)
		}
		c.InputSchema = spec
	}
	if raw, ok := fields["output_schema"]; ok {
		spec, err := schemaFromYAML(raw, dir)
		if err != nil {
			return fmt.Errorf("output_schema: %w", err)
		}
		c.OutputSchema = spec
	}
	for _, entry := range []struct {
		name   string
		target *int64
	}{
		{"max_input_bytes", &c.MaxInputBytes},
		{"max_line_bytes", &c.MaxLineBytes},
		{"max_response_bytes", &c.MaxResponseBytes},
	} {
		if raw, ok := fields[entry.name]; ok {
			if err := decodeYAML(raw, entry.target); err != nil {
				return fmt.Errorf("%s: expected an integer: %w", entry.name, err)
			}
			if *entry.target <= 0 {
				return fmt.Errorf("%s must be positive", entry.name)
			}
		}
	}
	if v, ok, err := stringField(fields, "timeout"); err != nil {
		return err
	} else if ok {
		c.Timeout, err = parseTimeout(v)
		if err != nil {
			return fmt.Errorf("timeout: %w", err)
		}
	}
	return nil
}

func onlyComments(data []byte) bool {
	for line := range bytes.SplitSeq(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) != 0 && line[0] != '#' {
			return false
		}
	}
	return true
}

func frontmatter(data []byte) (front, body []byte, err error) {
	first, rest, hasNewline := bytes.Cut(data, []byte("\n"))
	if string(bytes.TrimSuffix(first, []byte("\r"))) != "---" {
		return nil, data, nil
	}
	if !hasNewline {
		return nil, nil, errors.New("frontmatter is missing its closing ---")
	}
	var lines [][]byte
	for {
		line, remaining, more := bytes.Cut(rest, []byte("\n"))
		if string(bytes.TrimSuffix(line, []byte("\r"))) == "---" {
			return bytes.Join(lines, []byte("\n")), remaining, nil
		}
		lines = append(lines, line)
		if !more {
			return nil, nil, errors.New("frontmatter is missing its closing ---")
		}
		rest = remaining
	}
}

func parseAgent(raw yaml.RawMessage, dir string) (AgentConfig, error) {
	var name string
	if err := decodeYAML(raw, &name); err == nil {
		if !builtin(name) {
			return AgentConfig{}, fmt.Errorf("unsupported provider %q", name)
		}
		return AgentConfig{Provider: name, Protocol: defaultProtocol(name)}, nil
	}
	var fields map[string]yaml.RawMessage
	if err := yaml.Unmarshal(raw, &fields); err != nil || fields == nil {
		return AgentConfig{}, errors.New("expected a provider name or agent object")
	}
	a, err := parseAgentFields(fields)
	if err != nil {
		return a, err
	}
	if a.Protocol != "" && a.Protocol != "acp" && a.Protocol != "cli" {
		return a, fmt.Errorf("protocol: unsupported value %q", a.Protocol)
	}
	if a.Provider != "" {
		if !builtin(a.Provider) {
			return a, fmt.Errorf("unsupported provider %q", a.Provider)
		}
		if _, ok := fields["command"]; ok {
			return a, errors.New("command is only supported for custom agents")
		}
		if a.Protocol == "" {
			a.Protocol = defaultProtocol(a.Provider)
		}
	} else {
		if _, ok := fields["protocol"]; !ok {
			return a, errors.New("custom agents require protocol: acp")
		}
		if a.Command == "" {
			return a, errors.New("custom agents require command")
		}
		if strings.ContainsRune(a.Command, '/') || strings.ContainsRune(a.Command, filepath.Separator) {
			a.Command = resolve(dir, a.Command)
		}
	}
	if a.CWD != "" {
		a.CWD = resolve(dir, a.CWD)
	}
	return a, nil
}

func parseAgentFields(fields map[string]yaml.RawMessage) (AgentConfig, error) {
	var a AgentConfig
	for name := range fields {
		switch name {
		case "provider", "protocol", "model", "command", "args", "cwd":
		default:
			return a, fmt.Errorf("unknown field %q", name)
		}
	}
	for _, entry := range []struct {
		name   string
		target *string
	}{
		{"provider", &a.Provider},
		{"protocol", &a.Protocol},
		{"model", &a.Model},
		{"command", &a.Command},
		{"cwd", &a.CWD},
	} {
		if v, ok, err := stringField(fields, entry.name); err != nil {
			return a, err
		} else if ok {
			if v == "" {
				return a, fmt.Errorf("%s must not be empty", entry.name)
			}
			*entry.target = v
		}
	}
	if rawArgs, ok := fields["args"]; ok {
		if err := decodeYAML(rawArgs, &a.Args); err != nil {
			return a, fmt.Errorf("args: expected an array of strings: %w", err)
		}
		for _, arg := range a.Args {
			if !utf8.ValidString(arg) {
				return a, errors.New("args: invalid UTF-8")
			}
		}
	}
	return a, nil
}

func builtin(provider string) bool {
	return provider == "copilot" || provider == "claude" || provider == "codex"
}

func defaultProtocol(provider string) string {
	return "cli"
}

func validate(c *Config) error {
	if err := requireDirectory(c.CWD, "cwd"); err != nil {
		return err
	}
	if c.Agent.CWD == "" {
		c.Agent.CWD = c.CWD
	}
	agentCWD := c.Agent.CWD
	if !filepath.IsAbs(agentCWD) {
		agentCWD = resolve(c.CWD, agentCWD)
	}
	if err := requireDirectory(agentCWD, "agent.cwd"); err != nil {
		return err
	}
	if !validMode(c.Mode) {
		return fmt.Errorf("mode: unsupported value %q", c.Mode)
	}
	if c.Concurrency <= 0 {
		return errors.New("concurrency must be positive")
	}
	if c.Mode == "reduce" && c.Concurrency != 1 {
		return errors.New("concurrency must be 1 in reduce mode")
	}
	for _, entry := range []struct {
		name  string
		value int64
	}{
		{"max_input_bytes", c.MaxInputBytes},
		{"max_line_bytes", c.MaxLineBytes},
		{"max_response_bytes", c.MaxResponseBytes},
	} {
		if entry.value <= 0 {
			return fmt.Errorf("%s must be positive", entry.name)
		}
	}
	if c.Agent.Provider == "" {
		if c.Agent.Protocol != "acp" || c.Agent.Command == "" {
			return errors.New("custom agents require protocol: acp and command")
		}
		if strings.TrimSpace(c.AllowedTools) != "" {
			return errors.New("allowed-tools is not supported for custom ACP agents")
		}
	} else {
		switch {
		case c.Agent.Provider == "copilot" && (c.Agent.Protocol == "acp" || c.Agent.Protocol == "cli"):
		case (c.Agent.Provider == "claude" || c.Agent.Provider == "codex") && c.Agent.Protocol == "cli":
		default:
			return fmt.Errorf("unsupported agent/protocol combination %q/%q", c.Agent.Provider, c.Agent.Protocol)
		}
		for _, arg := range c.Agent.Args {
			if arg == "--" {
				return errors.New(`agent.args: "--" is not supported for built-in agents`)
			}
		}
	}
	return nil
}

func validMode(mode string) bool {
	return mode == "map" || mode == "reduce" || mode == "auto"
}

func stringField(fields map[string]yaml.RawMessage, name string) (string, bool, error) {
	raw, ok := fields[name]
	if !ok {
		return "", false, nil
	}
	var value string
	if err := decodeYAML(raw, &value); err != nil {
		return "", true, fmt.Errorf("%s: expected a string: %w", name, err)
	}
	return value, true, nil
}

func decodeYAML(raw yaml.RawMessage, target any) error {
	data, err := yaml.YAMLToJSON(raw)
	if err != nil {
		return err
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return errors.New("null is not allowed")
	}
	return json.Unmarshal(data, target)
}

func schemaFromYAML(raw yaml.RawMessage, dir string) (SchemaSpec, error) {
	data, err := schemaYAMLToJSON(raw)
	if err != nil {
		return SchemaSpec{}, err
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return SchemaSpec{}, err
	}
	switch v := value.(type) {
	case string:
		if err := validPath(v, "schema path"); err != nil {
			return SchemaSpec{}, err
		}
		return SchemaSpec{Present: true, Path: resolve(dir, v)}, nil
	case map[string]any, bool:
		return SchemaSpec{Present: true, JSON: data, BaseURI: directoryURI(dir)}, nil
	default:
		return SchemaSpec{}, errors.New("expected a schema object, boolean, or file path")
	}
}

func schemaFromCLI(value, dir string) (SchemaSpec, error) {
	if !utf8.ValidString(value) {
		return SchemaSpec{}, errors.New("invalid UTF-8")
	}
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(trimmed, "{") || trimmed == "true" || trimmed == "false" {
		var schema any
		decoder := json.NewDecoder(strings.NewReader(trimmed))
		decoder.UseNumber()
		if err := decoder.Decode(&schema); err != nil {
			return SchemaSpec{}, fmt.Errorf("invalid JSON schema: %w", err)
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			if err == nil {
				return SchemaSpec{}, errors.New("invalid JSON schema: multiple JSON values")
			}
			return SchemaSpec{}, fmt.Errorf("invalid JSON schema: %w", err)
		}
		switch schema.(type) {
		case map[string]any, bool:
			return SchemaSpec{Present: true, JSON: []byte(trimmed), BaseURI: directoryURI(dir)}, nil
		default:
			return SchemaSpec{}, errors.New("expected a schema object or boolean")
		}
	}
	if err := validPath(value, "schema path"); err != nil {
		return SchemaSpec{}, err
	}
	return SchemaSpec{Present: true, Path: resolve(dir, value)}, nil
}

func parseTimeout(value string) (time.Duration, error) {
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, err
	}
	if duration <= 0 {
		return 0, errors.New("must be positive")
	}
	return duration, nil
}

func validPath(path, name string) error {
	if path == "" || !utf8.ValidString(path) {
		return fmt.Errorf("%s must be a nonempty UTF-8 path", name)
	}
	return nil
}

func requireDirectory(path, name string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%s %s: %w", name, path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s %s: not a directory", name, path)
	}
	return nil
}

func resolve(dir, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(dir, path)
}

func directoryURI(dir string) string {
	path := filepath.ToSlash(dir)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if !strings.HasSuffix(path, "/") {
		path += "/"
	}
	return (&url.URL{Scheme: "file", Path: path}).String()
}
