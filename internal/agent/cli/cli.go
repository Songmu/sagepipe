// Package cli provides bounded subprocess execution for the built-in CLI agents.
package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	defaultOutputBytes = 16 << 20
	maxStderrBytes     = 64 << 10
)

var errStderrLimit = errors.New("stderr limit exceeded")

// Options configures a CLI runner. Dir defaults to the current working directory
// at construction time; child processes inherit the parent's environment.
// MaxOutputBytes caps the entire stdout event stream, independently of the
// final-answer limit. Zero selects 16 MiB; positive values must be less than
// the platform's maximum int so that the scanner can represent the limit + 1.
type Options struct {
	Program        string
	Dir            string
	Model          string
	AllowedTools   []string
	MaxOutputBytes int64
}

// Prepare supplies product defaults and pins an explicit absolute working directory.
func (o Options) Prepare(defaultProgram string) (Options, error) {
	if o.Program == "" {
		o.Program = defaultProgram
	}
	if o.Dir == "" {
		var err error
		o.Dir, err = os.Getwd()
		if err != nil {
			return Options{}, errors.New("CLI: cannot determine working directory")
		}
	}
	dir, err := filepath.Abs(o.Dir)
	if err != nil {
		return Options{}, errors.New("CLI: invalid working directory")
	}
	o.Dir = dir
	if o.MaxOutputBytes == 0 {
		o.MaxOutputBytes = defaultOutputBytes
	}
	if !validOutputLimit(o.MaxOutputBytes) {
		return Options{}, errors.New("CLI: output limit cannot fit platform scanner")
	}
	o.AllowedTools = append([]string(nil), o.AllowedTools...)
	return o, nil
}

func validOutputLimit(limit int64) bool {
	return limit > 0 && limit < int64(^uint(0)>>1)
}

// Execute starts a fresh process, passes stdin without a shell, and calls onLine
// for each bounded stdout JSON line. Neither stdout nor stderr is included in errors.
func Execute(ctx context.Context, product string, opts Options, args []string, stdin io.Reader, onLine func([]byte) error) error {
	if !validOutputLimit(opts.MaxOutputBytes) || onLine == nil {
		return errors.New("CLI: invalid subprocess options")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, opts.Program, args...)
	cmd.Dir = opts.Dir
	cmd.Stdin = stdin
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("%s CLI: cannot open stdout pipe", product)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("%s CLI: cannot open stderr pipe", product)
	}
	if err := cmd.Start(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%s CLI: cannot start subprocess", product)
	}

	stderrDone := make(chan error, 1)
	go func() {
		n, err := io.Copy(io.Discard, io.LimitReader(stderr, maxStderrBytes+1))
		if n > maxStderrBytes {
			_ = cmd.Process.Kill()
			stderrDone <- errStderrLimit
		} else {
			stderrDone <- err
		}
	}()

	stream := &io.LimitedReader{R: stdout, N: opts.MaxOutputBytes + 1}
	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 4096), int(opts.MaxOutputBytes+1))
	var readErr error
	for scanner.Scan() {
		if stream.N == 0 {
			readErr = errors.New("stdout limit exceeded")
			break
		}
		if err := onLine(scanner.Bytes()); err != nil {
			readErr = err
			break
		}
	}
	if readErr == nil {
		if stream.N == 0 {
			readErr = errors.New("stdout limit exceeded")
		} else if err := scanner.Err(); err != nil {
			readErr = errors.New("stdout limit exceeded or unreadable")
		}
	}
	if readErr != nil {
		_ = cmd.Process.Kill()
	}
	stderrErr := <-stderrDone
	waitErr := cmd.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	if errors.Is(stderrErr, errStderrLimit) {
		return fmt.Errorf("%s CLI: %w", product, stderrErr)
	}
	if readErr != nil {
		return fmt.Errorf("%s CLI: %w", product, readErr)
	}
	if stderrErr != nil {
		return fmt.Errorf("%s CLI: unreadable stderr", product)
	}
	if waitErr != nil {
		var exit *exec.ExitError
		if errors.As(waitErr, &exit) {
			return fmt.Errorf("%s CLI: exited with status %d", product, exit.ExitCode())
		}
		return fmt.Errorf("%s CLI: subprocess failed", product)
	}
	return nil
}

// CheckResponseLimit enforces the caller's limit on the extracted final answer.
func CheckResponseLimit(text string, maxBytes int64) error {
	if maxBytes < 0 {
		return errors.New("agent response byte limit must not be negative")
	}
	if maxBytes > 0 && int64(len(text)) > maxBytes {
		return errors.New("agent response exceeds byte limit")
	}
	return nil
}

// SafeNativeSchema accepts a conservative common subset that can be passed
// unchanged to Claude or Codex. Unknown keywords require prompt fallback.
func SafeNativeSchema(schema []byte, requireAllProperties bool) (bool, error) {
	if len(schema) == 0 {
		return false, nil
	}
	if !utf8.Valid(schema) {
		return false, errors.New("invalid native JSON schema")
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(schema))
	if err := decoder.Decode(&value); err != nil {
		return false, errors.New("invalid native JSON schema")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return false, errors.New("invalid native JSON schema")
	}
	return safeObject(value, requireAllProperties), nil
}

func safeObject(value any, requireAllProperties bool) bool {
	obj, ok := value.(map[string]any)
	if !ok || obj["type"] != "object" || obj["additionalProperties"] != false {
		return false
	}
	props, ok := obj["properties"].(map[string]any)
	if !ok {
		return false
	}
	required, ok := obj["required"].([]any)
	if !ok {
		return false
	}
	seen := make(map[string]bool, len(required))
	for _, name := range required {
		key, ok := name.(string)
		if !ok || seen[key] {
			return false
		}
		if _, exists := props[key]; !exists {
			return false
		}
		seen[key] = true
	}
	if requireAllProperties && len(seen) != len(props) {
		return false
	}
	for key, value := range obj {
		switch key {
		case "type", "additionalProperties", "properties", "required":
		case "description", "title":
			if _, ok := value.(string); !ok {
				return false
			}
		default:
			return false
		}
	}
	for _, value := range props {
		if !safeField(value, requireAllProperties) {
			return false
		}
	}
	return true
}

func safeField(value any, requireAllProperties bool) bool {
	obj, ok := value.(map[string]any)
	if !ok {
		return false
	}
	switch obj["type"] {
	case "object":
		return safeObject(value, requireAllProperties)
	case "array":
		if len(obj) != 2 || !safeField(obj["items"], requireAllProperties) {
			return false
		}
	case "string", "integer", "number", "boolean":
		for key, value := range obj {
			switch key {
			case "type":
			case "description", "title":
				if _, ok := value.(string); !ok {
					return false
				}
			default:
				return false
			}
		}
	default:
		return false
	}
	return true
}

// JoinAllowedTools preserves spaces in individual tool rules (not shell words).
func JoinAllowedTools(tools []string) (string, error) {
	for _, tool := range tools {
		if strings.TrimSpace(tool) == "" || strings.Contains(tool, ",") {
			return "", errors.New("CLI: invalid allowed tool rule")
		}
	}
	return strings.Join(tools, ","), nil
}
