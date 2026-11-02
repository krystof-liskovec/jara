package jara

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// A newly joined checkout has no shared sync history. Use the local committed
// tree as a conservative base on both sides: local edits can replace matching
// remote content, while divergent remote content remains a conflict.
func seedExistingBaseline(binding Binding, root string) error {
	state, err := syncStatePath(binding, root)
	if err != nil {
		return err
	}
	if _, err = os.Stat(state); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	head := gitHead(root)
	if head == "" {
		return fmt.Errorf("local repository needs a commit before linking an existing checkout")
	}
	base, err := committedManifest(root, head)
	if err != nil {
		return err
	}
	return writeJSON(state, Baseline{Local: base, Remote: base, LocalHead: head})
}

func committedManifest(root, commit string) (Snapshot, error) {
	rules, err := readIgnore(root)
	if err != nil {
		return nil, err
	}
	listing, err := exec.Command("git", "-C", root, "ls-tree", "-rz", "--full-tree", commit).Output()
	if err != nil {
		return nil, fmt.Errorf("read committed file list: %w", err)
	}
	type entry struct{ name, mode, object string }
	var entries []entry
	var objects strings.Builder
	for _, record := range bytes.Split(listing, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		header, name, ok := strings.Cut(string(record), "\t")
		fields := strings.Fields(header)
		if !ok || len(fields) != 3 {
			return nil, fmt.Errorf("invalid committed file entry")
		}
		if ignored(name, rules) {
			continue
		}
		if err = validateWorkspacePath(name); err != nil {
			return nil, err
		}
		if fields[1] != "blob" {
			return nil, fmt.Errorf("%s is a submodule; submodule transfer is not supported", name)
		}
		entries = append(entries, entry{name, fields[0], fields[2]})
		objects.WriteString(fields[2] + "\n")
	}
	cmd := exec.Command("git", "-C", root, "cat-file", "--batch")
	cmd.Stdin = strings.NewReader(objects.String())
	output, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	finished := false
	defer func() {
		if !finished {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	reader := bufio.NewReader(output)
	files := Snapshot{}
	for _, e := range entries {
		header, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		fields := strings.Fields(header)
		if len(fields) != 3 || fields[0] != e.object || fields[1] != "blob" {
			return nil, fmt.Errorf("invalid Git object response for %s", e.name)
		}
		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || size < 0 {
			return nil, fmt.Errorf("invalid Git object size for %s", e.name)
		}
		f := File{Mode: 0644}
		switch e.mode {
		case "120000":
			if size > maxTransfer {
				return nil, fmt.Errorf("symlink target too large: %s", e.name)
			}
			target := make([]byte, size)
			if _, err = io.ReadFull(reader, target); err != nil {
				return nil, err
			}
			f.Mode, f.Link = 0, string(target)
			if !validLink(e.name, f.Link) {
				return nil, fmt.Errorf("committed symlink points outside workspace: %s", e.name)
			}
			hashData(&f)
		case "100644", "100755":
			if e.mode == "100755" {
				f.Mode = 0755
			}
			hash := sha256.New()
			if _, err = io.CopyN(hash, reader, size); err != nil {
				return nil, err
			}
			f.Hash = hex.EncodeToString(hash.Sum(nil))
		default:
			return nil, fmt.Errorf("unsupported committed file mode %s", e.mode)
		}
		if delimiter, err := reader.ReadByte(); err != nil || delimiter != '\n' {
			return nil, fmt.Errorf("invalid Git object delimiter for %s", e.name)
		}
		files[e.name] = f
	}
	err = cmd.Wait()
	finished = true
	return files, err
}
