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
