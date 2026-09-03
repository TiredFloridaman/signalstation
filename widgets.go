package main

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"
)

// tappableBox makes an arbitrary layout clickable.
//
// The obvious alternative — stacking a borderless button behind the row — draws
// the button's own background over the row and looks wrong. Fyne delivers a tap
// to the deepest tappable object under the pointer, so buttons placed inside
// this box still receive their own clicks.
type tappableBox struct {
	widget.BaseWidget
	content fyne.CanvasObject
	onTap   func()
}

func newTappableBox(content fyne.CanvasObject, onTap func()) *tappableBox {
	b := &tappableBox{content: content, onTap: onTap}
	b.ExtendBaseWidget(b)
	return b
}

func (b *tappableBox) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(b.content)
}

func (b *tappableBox) Tapped(_ *fyne.PointEvent) {
	if b.onTap != nil {
		b.onTap()
	}
}

// Cursor switches to the pointing hand so the row reads as clickable.
func (b *tappableBox) Cursor() desktop.Cursor { return desktop.PointerCursor }

var (
	_ fyne.Tappable      = (*tappableBox)(nil)
	_ desktop.Cursorable = (*tappableBox)(nil)
)
