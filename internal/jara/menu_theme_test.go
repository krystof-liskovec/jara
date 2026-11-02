package jara

import (
	"github.com/rivo/uniseg"
	"strings"
	"testing"
)

func TestMenuThemeKeepsGeometryAndColorsWholeSelection(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	items := []menuItem{{Title: "Review login flow", Detail: "Codex · recent · 2 live", Kind: "conversation"}, {Title: "Fix deployment", Detail: "2 terminals", Kind: "conversation"}}
	for _, theme := range []string{"dark", "light"} {
		t.Setenv("JARA_THEME", theme)
		for _, width := range []int{20, 32, 80, 120} {
			lines := menuLines("jara", items, 0, width, 24)
			styled := styleMenuLines(lines, width)
			selection := menuBG(menuColors().selection)
			found := false
			for i, line := range styled {
				if strings.TrimSpace(lines[i]) == "" && strings.Contains(line, menuBG(menuColors().surface)) {
					t.Fatal("numeric metadata merged cards across their spacer")
				}
				if uniseg.StringWidth(menuText(line)) > width-1 {
					t.Fatalf("%s %d: overflow", theme, width)
				}
				if strings.Contains(line, selection) && strings.Contains(menuText(line), "▸ ") {
					found = true
					if i+1 >= len(styled) || !strings.Contains(styled[i+1], selection) {
						t.Fatal("selection metadata has a different surface")
					}
				}
			}
			if !found {
				t.Fatal("selected card missing")
			}
		}
	}
}
func TestMenuHonorsNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	plain := []string{" JARA / Projects", "> 1 First"}
	got := styleMenuLines(plain, 32)
	for i := range plain {
		if got[i] != plain[i] {
			t.Fatal("NO_COLOR changed output")
		}
	}
}

func TestTerminalThemeInheritsColorsAndReversesWholeSelection(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	items := []menuItem{{Title: "Project", Detail: "2 live terminals", Kind: "project"}}
	for _, theme := range []string{"", "terminal"} {
		t.Setenv("JARA_THEME", theme)
		lines := styleMenuLines(menuLines("Projects", items, 0, 80, 24), 80)
		for i, line := range lines {
			if strings.Contains(line, ";2;") {
				t.Fatalf("terminal theme overrides RGB colors on row %d", i)
			}
			if strings.Contains(menuText(line), "▸ ") {
				for _, selected := range lines[i : i+2] {
					if !strings.Contains(selected, "\x1b[7m\x1b[39m") {
						t.Fatal("selection does not invert the terminal's default colors")
					}
					if !strings.HasSuffix(selected, "\x1b[0m") {
						t.Fatal("selection leaks styling into the next row")
					}
				}
			}
		}
	}
}
