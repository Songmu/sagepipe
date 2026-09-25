package pipeline

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"math"
)

type inputLine struct {
	data     []byte
	rawBytes int64
	blank    bool
	tooLong  bool
}

// nextLine bounds retained input even when an input line has no terminator.
func nextLine(r *bufio.Reader, max int64) (inputLine, error) {
	line := inputLine{blank: true}
	limit := max
	if max <= math.MaxInt64-2 {
		limit += 2 // Leave room for CRLF before checking the actual line length.
	}
	pendingCR := false
	for {
		part, err := r.ReadSlice('\n')
		if len(part) == 0 && errors.Is(err, io.EOF) && line.rawBytes == 0 {
			return inputLine{}, io.EOF
		}
		line.rawBytes += int64(len(part))
		for _, b := range part {
			if pendingCR && b != '\n' {
				line.blank = false
			}
			pendingCR = b == '\r'
			if b != ' ' && b != '\t' && b != '\r' && b != '\n' {
				line.blank = false
			}
		}
		if !line.tooLong {
			if line.rawBytes > limit {
				line.tooLong = true
				line.data = nil
			} else {
				line.data = append(line.data, part...)
			}
		}
		switch {
		case err == nil, errors.Is(err, io.EOF):
			if pendingCR {
				line.blank = false
			}
			if !line.tooLong {
				line.data = bytes.TrimSuffix(line.data, []byte{'\n'})
				if len(part) > 0 && part[len(part)-1] == '\n' {
					line.data = bytes.TrimSuffix(line.data, []byte{'\r'})
				}
				line.tooLong = int64(len(line.data)) > max
			}
			return line, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		default:
			return inputLine{}, err
		}
	}
}
