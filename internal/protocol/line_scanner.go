package protocol

import (
	"bufio"
	"bytes"
	"errors"
	"io"
)

// JSON-lines streams between Jara, Codex, and Claude Code carry whole
// conversations and attached images, so single lines can be large. A
// bufio.Scanner stops at its first overlong line and the stream silently
// ends; lineScanner instead skips that one line and keeps reading.

const JSONLineLimit = 64 << 20

type LineScanner struct {
	reader  *bufio.Reader
	limit   int
	line    []byte
	err     error
	skipped int // overlong lines dropped so far
}

func NewLineScanner(input io.Reader) *LineScanner {
	return &LineScanner{reader: bufio.NewReaderSize(input, 64*1024), limit: JSONLineLimit}
}

// Scan advances to the next line within the limit, like bufio.Scanner.Scan.
func (s *LineScanner) Scan() bool {
	for s.err == nil {
		line, overlong, err := s.readLine()
		if err != nil && !errors.Is(err, io.EOF) {
			s.err = err
		}
		if overlong {
			s.skipped++
		} else if len(line) > 0 || err == nil {
			s.line = line
			if errors.Is(err, io.EOF) {
				s.err = io.EOF
			}
			return true
		}
		if err != nil {
			s.err = err
		}
	}
	return false
}

func (s *LineScanner) readLine() (line []byte, overlong bool, err error) {
	var buffer []byte
	for {
		chunk, readErr := s.reader.ReadSlice('\n')
		if !overlong {
			if len(buffer)+len(chunk) > s.limit+2 {
				overlong, buffer = true, nil
			} else {
				buffer = append(buffer, chunk...)
			}
		}
		if errors.Is(readErr, bufio.ErrBufferFull) {
			continue
		}
		return bytes.TrimRight(buffer, "\r\n"), overlong, readErr
	}
}

// Bytes returns the current line; it stays valid until the next Scan.
func (s *LineScanner) Bytes() []byte { return s.line }

// Err returns the first read error other than the end of the stream.
func (s *LineScanner) Err() error {
	if errors.Is(s.err, io.EOF) {
		return nil
	}
	return s.err
}

// Text returns the current line as a string.
func (s *LineScanner) Text() string { return string(s.line) }
