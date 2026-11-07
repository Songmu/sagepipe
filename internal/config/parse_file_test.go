package config

import "testing"

func TestParseFileErrorOrder(t *testing.T) {
	for _, tt := range []struct {
		name, front, want string
	}{
		{
			"execution before schemas",
			"mode: invalid\ninput_schema: []\nmax_input_bytes: 0",
			`mode: unsupported value "invalid"`,
		},
		{
			"input schema before output schema",
			"input_schema: []\noutput_schema: []\nmax_input_bytes: 0",
			"input_schema: expected a schema object, boolean, or file path",
		},
		{
			"schemas before limits",
			"output_schema: []\nmax_input_bytes: 0",
			"output_schema: expected a schema object, boolean, or file path",
		},
		{
			"limits before timeout",
			"max_input_bytes: 0\ntimeout: 0s",
			"max_input_bytes must be positive",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var c Config
			err := parseFile(&c, []byte("---\n"+tt.front+"\n---\n"), ".")
			if err == nil || err.Error() != tt.want {
				t.Fatalf("parseFile error = %v, want %q", err, tt.want)
			}
		})
	}
}
