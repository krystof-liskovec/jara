package jara

import (
	"bytes"
)

const maxCodexOSC = 4096

// codexColorQueries consumes OSC 10/11 queries and leaves other terminal output intact.
type codexColorQueries struct {
	pending []byte
	answer  func(byte) error
}

func (f *codexColorQueries) feed(input []byte) ([]byte, error) {
	f.pending = append(f.pending, input...)
	var output []byte
	for len(f.pending) > 0 {
		start := bytes.IndexByte(f.pending, 0x1b)
		if start < 0 {
			output = append(output, f.pending...)
			f.pending = nil
			break
		}
		if start > 0 {
			output = append(output, f.pending[:start]...)
			f.pending = f.pending[start:]
		}
		if len(f.pending) < 2 {
			break
		}
		if f.pending[1] != ']' {
			output = append(output, f.pending[:2]...)
			f.pending = f.pending[2:]
			continue
		}
		end, terminator := oscEnd(f.pending)
		if end < 0 {
			if len(f.pending) > maxCodexOSC {
				output = append(output, f.pending...)
				f.pending = nil
			}
			break
		}
		sequence := f.pending[:end+terminator]
		slot, query := colorQuery(f.pending[2:end])
		if query {
			if err := f.answer(slot); err != nil {
				return output, err
			}
		} else {
			output = append(output, sequence...)
		}
		f.pending = f.pending[len(sequence):]
	}
	return output, nil
}

func (f *codexColorQueries) finish() []byte {
	output := f.pending
	f.pending = nil
	return output
}

func oscEnd(data []byte) (int, int) {
	for i := 2; i < len(data); i++ {
		if data[i] == 0x07 {
			return i, 1
		}
		if data[i] == 0x1b && i+1 < len(data) && data[i+1] == '\\' {
			return i, 2
		}
	}
	return -1, 0
}

func colorQuery(payload []byte) (byte, bool) {
	if bytes.Equal(payload, []byte("10;?")) {
		return 10, true
	}
	if bytes.Equal(payload, []byte("11;?")) {
		return 11, true
	}
	return 0, false
}
