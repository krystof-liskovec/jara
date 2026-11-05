package jara

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/rivo/uniseg"
)

type menuPalette struct{ canvas, surface, selection, text, muted, accent, green, brand string }

var menuTerminalTheme struct {
	once    sync.Once
	palette menuPalette
}

func menuColors() menuPalette {
	switch os.Getenv("JARA_THEME") {
	case "light":
		return menuPalette{"251;241;199", "235;219;178", "213;196;161", "60;56;54", "124;111;100", "175;58;3", "121;116;14", "175;58;3"}
	case "dark":
		return menuPalette{"40;40;40", "60;56;54", "80;73;69", "235;219;178", "168;153;132", "254;128;25", "184;187;38", "254;128;25"}
	}
	background, err := terminalBackground()
	if err != nil {
		background = fallbackTerminalBackground
	}
	return menuColorsForBackground(background)
}

func menuColorsForBackground(background terminalRGB) menuPalette {
	dark := background.foreground().r > 128
	mix := func(source, target byte, portion float64) byte {
		return byte(float64(source)*(1-portion) + float64(target)*portion + 0.5)
	}
	blend := func(source, target terminalRGB, portion float64) terminalRGB {
		return terminalRGB{mix(source.r, target.r, portion), mix(source.g, target.g, portion), mix(source.b, target.b, portion)}
	}
	rgb := func(c terminalRGB) string { return fmt.Sprintf("%d;%d;%d", c.r, c.g, c.b) }
	edge := terminalRGB{0, 0, 0}
	strength := 0.10
	accent := terminalRGB{175, 58, 3}
	green := terminalRGB{121, 116, 14}
	text := terminalRGB{60, 56, 54}
	muted := terminalRGB{124, 111, 100}
	if dark {
		edge = terminalRGB{255, 255, 255}
		strength = 0.13
		accent = terminalRGB{254, 128, 25}
		green = terminalRGB{184, 187, 38}
		text = terminalRGB{235, 219, 178}
		muted = terminalRGB{168, 153, 132}
	}
	surface := blend(background, edge, strength)
	selection := blend(surface, accent, 0.38)
	surfaceBrightness := (299*int(surface.r) + 587*int(surface.g) + 114*int(surface.b)) / 1000
	if surfaceBrightness > 115 {
		text = terminalRGB{60, 56, 54}
		muted = terminalRGB{124, 111, 100}
		green = terminalRGB{121, 116, 14}
	}
	return menuPalette{rgb(background), rgb(surface), rgb(selection), rgb(text), rgb(muted), rgb(accent), rgb(green), rgb(accent)}
}

// Read the terminal once before entering raw menu mode. Nested menus share its palette.
func menuColorsForTerminal() menuPalette {
	if os.Getenv("JARA_THEME") == "light" || os.Getenv("JARA_THEME") == "dark" {
		return menuColors()
	}
	menuTerminalTheme.once.Do(func() {
		background, err := menuTerminalBackground()
		if err != nil {
			background = fallbackTerminalBackground
		}
		menuTerminalTheme.palette = menuColorsForBackground(background)
	})
	return menuTerminalTheme.palette
}
func menuBrightness(color string) int {
	var r, g, b int
	if _, err := fmt.Sscanf(color, "%d;%d;%d", &r, &g, &b); err != nil {
		return 0
	}
	return (299*r + 587*g + 114*b) / 1000
}
func menuSelectionText(color string) string {
	if menuBrightness(color) > 115 {
		return "21;24;33"
	}
	return "244;246;255"
}

func menuFG(color string) string { return "\x1b[38;2;" + color + "m" }
func menuBG(color string) string { return "\x1b[48;2;" + color + "m" }

// Paint complete surfaces, including empty space, so the terminal's default
// background cannot create stripes or change the selected card's contrast.
func styleMenuLines(lines []string, width int) []string {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return lines
	}
	return styleMenuLinesWithPalette(lines, width, menuColors())
}

func styleMenuLinesWithPalette(lines []string, width int, palette menuPalette) []string {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return lines
	}
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
			fg = menuSelectionText(palette.selection)
			bold = "\x1b[1m"
		case selectedDetail:
			bg = palette.selection
			fg = menuSelectionText(palette.selection)
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
					status := palette.green
					if selectedDetail {
						if menuBrightness(palette.selection) > 115 {
							status = "10;92;60"
						} else {
							status = "184;248;205"
						}
					}
					parts[j] = menuFG(status) + part + menuFG(fg)
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
