package jara

import (
	"os"
	"strings"

	"github.com/rivo/uniseg"
)

type menuPalette struct{ canvas, surface, selection, text, muted, accent, green, brand string }

func menuColors() menuPalette {
	if os.Getenv("JARA_THEME") == "light" {
		return menuPalette{"242;244;250", "230;234;244", "214;224;250", "30;39;60", "83;96;120", "61;75;180", "20;110;77", "75;48;155"}
	}
	return menuPalette{"15;19;30", "23;29;44", "42;53;83", "229;235;248", "143;157;184", "161;179;255", "108;222;172", "185;164;255"}
}
func menuFG(rgb string) string { return "\x1b[38;2;" + rgb + "m" }
func menuBG(rgb string) string { return "\x1b[48;2;" + rgb + "m" }

// Paint complete surfaces, including empty space, so the terminal's default
// background cannot create stripes or change the selected card's contrast.
func styleMenuLines(lines []string, width int) []string {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return lines
	}
	palette := menuColors()
	styled := make([]string, len(lines))
	selectedDetail := false
	previousRow := false
	for i, line := range lines {
		content := strings.TrimLeft(line, " ")
		bg, fg := palette.canvas, palette.muted
		bold := ""
		selected := strings.HasPrefix(content, "▸ ")
		parts := strings.SplitN(content, " ", 3)
		row := len(parts) == 3 && len(parts[0]) == 1 && parts[0][0] >= '1' && parts[0][0] <= '9' && strings.HasPrefix(parts[2], " ")
		detail := previousRow
		switch {
		case i == 0:
			fg = palette.brand
			bold = "\x1b[1m"
		case i == 2:
			bg = palette.surface
			fg = palette.accent
		case selected:
			bg = palette.selection
			fg = palette.text
			bold = "\x1b[1m"
		case selectedDetail:
			bg = palette.selection
			fg = palette.accent
		case row:
			bg = palette.surface
			fg = palette.text
		case detail:
			bg = palette.surface
		}
		panel := min(100, max(1, width-1))
		gutter := max(0, (width-1-panel)/2)
		if len(line) >= gutter {
			line = line[gutter:]
		}
		padding := strings.Repeat(" ", max(0, panel-uniseg.StringWidth(line)))
		painted := line
		if detail || (bg == palette.surface && !row && i > 2) {
			parts := strings.Split(line, " · ")
			for j, part := range parts {
				if strings.Contains(part, "live") || part == "working" {
					parts[j] = menuFG(palette.green) + part + menuFG(fg)
				}
			}
			painted = strings.Join(parts, " · ")
		}
		if strings.Contains(line, "Enter open") || strings.Contains(line, "Ctrl-C quit") || strings.Contains(line, "1-9 open") {
			for _, key := range []string{"1-9", "Enter", "↑↓", "n/p", "/", "←", "→", "Ctrl-C"} {
				painted = strings.ReplaceAll(painted, key, menuFG(palette.accent)+key+menuFG(fg))
			}
		}
		if i == 0 {
			painted = strings.Replace(painted, "JARA", menuBG(palette.accent)+menuFG(palette.canvas)+"JARA"+menuBG(bg)+menuFG(fg), 1)
		}
		styled[i] = menuBG(palette.canvas) + strings.Repeat(" ", gutter) + menuBG(bg) + menuFG(fg) + bold + painted + padding + menuBG(palette.canvas) + strings.Repeat(" ", max(0, width-1-panel-gutter)) + "\x1b[0m"
		selectedDetail = selected
		previousRow = selected || row
	}
	return styled
}
