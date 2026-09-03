package main

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// stationTheme is a deliberately cool, low-chroma palette. It stays clear of
// Signal's own brand blue: this is third-party software driving Signal, and it
// should not be mistaken for something Signal shipped.
//
// Colour carries state rather than decoration. Moss means an account is live,
// amber means it needs a step from you, brick means something failed.
type stationTheme struct{ base fyne.Theme }

func newStationTheme() fyne.Theme { return &stationTheme{base: theme.DefaultTheme()} }

var (
	colInk       = color.NRGBA{R: 0x19, G: 0x1C, B: 0x1E, A: 0xFF}
	colSurface   = color.NRGBA{R: 0x23, G: 0x27, B: 0x2A, A: 0xFF}
	colSurfaceHi = color.NRGBA{R: 0x2C, G: 0x31, B: 0x34, A: 0xFF}
	colLine      = color.NRGBA{R: 0x36, G: 0x3D, B: 0x40, A: 0xFF}
	colText      = color.NRGBA{R: 0xE4, G: 0xE7, B: 0xE6, A: 0xFF}
	colMuted     = color.NRGBA{R: 0x8A, G: 0x94, B: 0x90, A: 0xFF}
	colAccent    = color.NRGBA{R: 0x7F, G: 0xA9, B: 0x8C, A: 0xFF}
	colWarn      = color.NRGBA{R: 0xC9, G: 0xA2, B: 0x27, A: 0xFF}
	colDanger    = color.NRGBA{R: 0xC4, G: 0x57, B: 0x4B, A: 0xFF}

	// Light-variant equivalents, so the app is legible if the OS is set to
	// light mode and the user has not forced dark.
	colInkL     = color.NRGBA{R: 0xF2, G: 0xF3, B: 0xF2, A: 0xFF}
	colSurfaceL = color.NRGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xFF}
	colLineL    = color.NRGBA{R: 0xD5, G: 0xDA, B: 0xD7, A: 0xFF}
	colTextL    = color.NRGBA{R: 0x1B, G: 0x1F, B: 0x1D, A: 0xFF}
	colMutedL   = color.NRGBA{R: 0x5E, G: 0x67, B: 0x63, A: 0xFF}
	colAccentL  = color.NRGBA{R: 0x3F, G: 0x71, B: 0x55, A: 0xFF}
)

func (t *stationTheme) Color(name fyne.ThemeColorName, v fyne.ThemeVariant) color.Color {
	if v == theme.VariantLight {
		switch name {
		case theme.ColorNameBackground:
			return colInkL
		case theme.ColorNameForeground:
			return colTextL
		case theme.ColorNameInputBackground, theme.ColorNameButton, theme.ColorNameMenuBackground,
			theme.ColorNameOverlayBackground, theme.ColorNameHeaderBackground:
			return colSurfaceL
		case theme.ColorNameSeparator, theme.ColorNameInputBorder:
			return colLineL
		case theme.ColorNamePlaceHolder, theme.ColorNameDisabled:
			return colMutedL
		case theme.ColorNamePrimary, theme.ColorNameFocus, theme.ColorNameSuccess:
			return colAccentL
		case theme.ColorNameWarning:
			return colWarn
		case theme.ColorNameError:
			return colDanger
		}
		return t.base.Color(name, v)
	}

	switch name {
	case theme.ColorNameBackground:
		return colInk
	case theme.ColorNameForeground:
		return colText
	case theme.ColorNameInputBackground, theme.ColorNameButton:
		return colSurface
	case theme.ColorNameMenuBackground, theme.ColorNameOverlayBackground, theme.ColorNameHeaderBackground:
		return colSurfaceHi
	case theme.ColorNameSeparator, theme.ColorNameInputBorder:
		return colLine
	case theme.ColorNamePlaceHolder, theme.ColorNameDisabled:
		return colMuted
	case theme.ColorNamePrimary, theme.ColorNameFocus, theme.ColorNameSuccess:
		return colAccent
	case theme.ColorNameHover, theme.ColorNamePressed:
		return colSurfaceHi
	case theme.ColorNameSelection:
		return color.NRGBA{R: 0x7F, G: 0xA9, B: 0x8C, A: 0x40}
	case theme.ColorNameWarning:
		return colWarn
	case theme.ColorNameError:
		return colDanger
	case theme.ColorNameForegroundOnPrimary:
		return colInk
	}
	return t.base.Color(name, v)
}

// Size sets a type scale with a bit more air than Fyne's default, because this
// window is read at a glance rather than worked in continuously.
func (t *stationTheme) Size(name fyne.ThemeSizeName) float32 {
	switch name {
	case theme.SizeNameText:
		return 13.5
	case theme.SizeNameHeadingText:
		return 20
	case theme.SizeNameSubHeadingText:
		return 15.5
	case theme.SizeNameCaptionText:
		return 11.5
	case theme.SizeNamePadding:
		return 5
	case theme.SizeNameInnerPadding:
		return 9
	case theme.SizeNameInputRadius, theme.SizeNameSelectionRadius:
		return 5
	case theme.SizeNameSeparatorThickness:
		return 1
	}
	return t.base.Size(name)
}

func (t *stationTheme) Font(s fyne.TextStyle) fyne.Resource { return t.base.Font(s) }
func (t *stationTheme) Icon(n fyne.ThemeIconName) fyne.Resource {
	return t.base.Icon(n)
}
