package main

import (
	"testing"

	"github.com/thebanri/limoni/core/cell"
	"github.com/thebanri/limoni/core/driver"
	"github.com/thebanri/limoni/core/terminal"
)

// Since Limoni v0.8.0 a click inside a modal reaches only click regions
// tagged with that modal's layer. The buttons of every dialog must still fire
// through the real router, and a dialog drawn later must sit on top.
func TestModalButtonsReceiveClicks(t *testing.T) {
	b := driver.NewPortableBackend(driver.NewMemoryTerminalIO(nil, 100, 40))
	term, err := terminal.New(b)
	if err != nil {
		t.Fatal(err)
	}

	var inFirst, inSecond, outside int
	_ = term.Draw(func(f *terminal.Frame) {
		f.RegisterClickHandler(cell.NewRect(0, 0, 100, 40), func(driver.MouseEvent) { outside++ })
		func() {
			openModal(f, "first", cell.NewRect(10, 10, 40, 10), nil)
			defer f.EndLayer()
			f.RegisterClickHandler(cell.NewRect(12, 12, 5, 1), func(driver.MouseEvent) { inFirst++ })
		}()
		func() {
			openModal(f, "second", cell.NewRect(30, 15, 20, 5), nil)
			defer f.EndLayer()
			f.RegisterClickHandler(cell.NewRect(32, 16, 5, 1), func(driver.MouseEvent) { inSecond++ })
		}()
	})

	term.RouteMouseEvent(driver.MouseEvent{X: 33, Y: 16, Button: driver.MouseLeft})
	if inSecond != 1 {
		t.Fatalf("button of the top dialog: %d clicks, want 1", inSecond)
	}
	term.RouteMouseEvent(driver.MouseEvent{X: 5, Y: 5, Button: driver.MouseLeft})
	if outside != 0 {
		t.Fatalf("a click outside a dialog leaked to the screen beneath")
	}

	// Only the first dialog open.
	_ = term.Draw(func(f *terminal.Frame) {
		openModal(f, "first", cell.NewRect(10, 10, 40, 10), nil)
		defer f.EndLayer()
		f.RegisterClickHandler(cell.NewRect(12, 12, 5, 1), func(driver.MouseEvent) { inFirst++ })
	})
	term.RouteMouseEvent(driver.MouseEvent{X: 13, Y: 12, Button: driver.MouseLeft})
	if inFirst != 1 {
		t.Fatalf("button of the dialog: %d clicks, want 1", inFirst)
	}
}

