package protocol

import (
	"strings"
	"testing"
)

func TestLineScannerSkipsOverlongLines(t *testing.T) {
	input := "one\r\n" + strings.Repeat("x", 300) + "\ntwo\n\nthree"
	scanner := NewLineScanner(strings.NewReader(input))
	scanner.limit = 100
	var lines []string
	for scanner.Scan() {
		lines = append(lines, string(scanner.Bytes()))
	}
	if strings.Join(lines, "|") != "one|two||three" || scanner.skipped != 1 || scanner.Err() != nil {
		t.Fatalf("lines %q skipped %d err %v", lines, scanner.skipped, scanner.Err())
	}
	big := strings.Repeat("y", 200*1024) // longer than the read buffer
	scanner = NewLineScanner(strings.NewReader(big + "\n"))
	if !scanner.Scan() || len(scanner.Bytes()) != len(big) || scanner.Scan() {
		t.Fatal("lines longer than the read buffer are returned whole")
	}
}
