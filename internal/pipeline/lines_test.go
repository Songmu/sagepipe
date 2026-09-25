package pipeline

import (
	"bufio"
	"io"
	"strings"
	"testing"
)

func TestNextLine(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		max     int64
		want    string
		blank   bool
		tooLong bool
		raw     int64
	}{
		{"lf", "hello\n", 5, "hello", false, false, 6},
		{"crlf", "hello\r\n", 5, "hello", false, false, 7},
		{"no final newline", "hello", 5, "hello", false, false, 5},
		{"empty record", "\n", 1, "", true, false, 1},
		{"blank record", " \t \r\n", 3, " \t ", true, false, 5},
		{"embedded carriage return", " \r \n", 3, " \r ", false, false, 4},
		{"oversized", "hello!\n", 5, "hello!", false, true, 7},
		{"oversized blank", strings.Repeat(" ", 10000) + "\n", 5, "", true, true, 10001},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := bufio.NewReader(strings.NewReader(tt.input))
			line, err := nextLine(r, tt.max)
			if err != nil {
				t.Fatal(err)
			}
			if !line.tooLong && string(line.data) != tt.want {
				t.Errorf("data = %q, want %q", line.data, tt.want)
			}
			if line.blank != tt.blank || line.tooLong != tt.tooLong || line.rawBytes != tt.raw {
				t.Errorf("line = %+v, want blank=%t tooLong=%t raw=%d", line, tt.blank, tt.tooLong, tt.raw)
			}
			if _, err := nextLine(r, tt.max); err != io.EOF {
				t.Errorf("next call = %v, want EOF", err)
			}
		})
	}
}
