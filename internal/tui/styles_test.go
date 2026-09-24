package tui

import (
	"fmt"
	"math"
	"strconv"
	"testing"
)

func TestNormalizeTerminalTheme(t *testing.T) {
	for input, want := range map[string]string{
		"": "auto", " AUTO ": "auto", "dark": "dark", "LIGHT": "light",
	} {
		got, ok := NormalizeTerminalTheme(input)
		if !ok || got != want {
			t.Fatalf("NormalizeTerminalTheme(%q)=%q,%v want %q,true", input, got, ok, want)
		}
	}
	if got, ok := NormalizeTerminalTheme("sepia"); ok || got != "" {
		t.Fatalf("invalid theme accepted: %q,%v", got, ok)
	}
}

func TestLightAndDarkPalettesKeepHighContrastRolesDistinct(t *testing.T) {
	t.Cleanup(func() { applyTerminalPalette(darkTerminalPalette()) })
	applyTerminalPalette(lightTerminalPalette())
	if colorBg == colorText || colorSurface == colorText || colorAccent == colorBg {
		t.Fatalf("light palette collapsed contrast: bg=%s surface=%s text=%s accent=%s", colorBg, colorSurface, colorText, colorAccent)
	}
	if colorBg != lightTerminalPalette().bg || colorText != lightTerminalPalette().text {
		t.Fatalf("light palette was not applied: bg=%s text=%s", colorBg, colorText)
	}
	applyTerminalPalette(darkTerminalPalette())
	if colorBg != darkTerminalPalette().bg || colorText != darkTerminalPalette().text {
		t.Fatalf("dark palette was not restored: bg=%s text=%s", colorBg, colorText)
	}
}

func TestTerminalPalettesMeetReadableTextContrast(t *testing.T) {
	for name, palette := range map[string]terminalPalette{
		"dark": darkTerminalPalette(), "light": lightTerminalPalette(),
	} {
		for role, foreground := range map[string]string{
			"text": string(palette.text), "muted": string(palette.muted),
			"thought": string(palette.thought), "help": string(palette.help),
			"accent": string(palette.accent), "green": string(palette.green),
			"yellow": string(palette.yellow), "red": string(palette.red),
		} {
			for surface, background := range map[string]string{
				"app": string(palette.bg), "panel": string(palette.surface),
			} {
				if ratio := contrastRatio(foreground, background); ratio < 4.5 {
					t.Fatalf("%s %s on %s contrast %.2f < 4.5 (%s on %s)", name, role, surface, ratio, foreground, background)
				}
			}
		}
	}
}

func contrastRatio(foreground, background string) float64 {
	fg := relativeLuminance(foreground)
	bg := relativeLuminance(background)
	if bg > fg {
		fg, bg = bg, fg
	}
	return (fg + 0.05) / (bg + 0.05)
}

func relativeLuminance(hex string) float64 {
	if len(hex) != 7 || hex[0] != '#' {
		panic(fmt.Sprintf("invalid test color %q", hex))
	}
	values := make([]float64, 3)
	for i := range values {
		value, err := strconv.ParseUint(hex[1+i*2:3+i*2], 16, 8)
		if err != nil {
			panic(err)
		}
		channel := float64(value) / 255
		if channel <= 0.04045 {
			values[i] = channel / 12.92
		} else {
			values[i] = math.Pow((channel+0.055)/1.055, 2.4)
		}
	}
	return 0.2126*values[0] + 0.7152*values[1] + 0.0722*values[2]
}
