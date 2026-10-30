package jara

import "strings"

// Keep bracketed text pastes opaque, even when packets split escape sequences.
type clipboardKeys struct {
	paste    bool
	sequence []byte
	write    func([]byte) error
	image    func() error
}

func (k *clipboardKeys) feed(data []byte) error {
	for _, ch := range data {
		if len(k.sequence) > 0 {
			k.sequence = append(k.sequence, ch)
			if len(k.sequence) == 2 && ch == '[' {
				continue
			}
			if len(k.sequence) > 2 && k.sequence[1] == '[' && ch >= 0x20 && ch < 0x40 && len(k.sequence) < 64 {
				continue
			}
			seq := string(k.sequence)
			k.sequence = nil
			if !k.paste {
				switch seq {
				case "\x1b[118;5u", "\x1b[118;5:1u", "\x1b[118;7u", "\x1b[27;5;118~", "\x1b\x16":
					if err := k.image(); err != nil {
						return err
					}
					continue
				case "\x1b[118;5:2u", "\x1b[118;5:3u":
					continue // Kitty repeat/release must not duplicate attachments.
				}
			}
			if seq == "\x1b[200~" {
				k.paste = true
			}
			if seq == "\x1b[201~" {
				k.paste = false
			}
			if err := k.write([]byte(seq)); err != nil {
				return err
			}
			continue
		}
		if ch == 27 {
			k.sequence = []byte{ch}
			continue
		}
		if ch == 22 && !k.paste {
			if err := k.image(); err != nil {
				return err
			}
		} else if err := k.write([]byte{ch}); err != nil {
			return err
		}
	}
	return nil
}

func (k *clipboardKeys) flush() error {
	data := k.sequence
	k.sequence = nil
	return k.write(data)
}

func imagePaste(path string) string {
	if strings.ContainsAny(path, " \t'\"\\") {
		path = quote(path)
	}
	return "\x1b[200~" + path + "\x1b[201~"
}
