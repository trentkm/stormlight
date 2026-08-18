package ui

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

type fakeClipboard struct {
	data []byte
	err  error
}

func (f fakeClipboard) ReadImage() ([]byte, error) {
	return f.data, f.err
}

func withClipboard(t *testing.T, clipboard imageClipboard) {
	t.Helper()
	previous := systemClipboard
	systemClipboard = clipboard
	t.Cleanup(func() { systemClipboard = previous })
}

func TestPasteClipboardImageWritesTempFile(t *testing.T) {
	payload := []byte("\x89PNG fake image bytes")
	withClipboard(t, fakeClipboard{data: payload})

	path, err := pasteClipboardImage()
	if err != nil {
		t.Fatalf("pasteClipboardImage: %v", err)
	}
	t.Cleanup(func() { os.Remove(path) })

	base := filepath.Base(path)
	if !strings.HasPrefix(base, "stormlight-paste-") ||
		!strings.HasSuffix(base, ".png") {
		t.Fatalf("unexpected temp file name %q", base)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read temp file: %v", err)
	}
	if !bytes.Equal(written, payload) {
		t.Fatalf("temp file holds %q, want %q", written, payload)
	}
}

func TestPasteClipboardImagePropagatesClipboardError(t *testing.T) {
	withClipboard(t, fakeClipboard{err: errNoClipboardImage})

	if _, err := pasteClipboardImage(); err != errNoClipboardImage {
		t.Fatalf("got %v, want errNoClipboardImage", err)
	}
}

func TestComposeCtrlVInsertsPastedImagePath(t *testing.T) {
	withClipboard(t, fakeClipboard{data: []byte("\x89PNG")})

	model := NewModel(stubBackend{})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = updated.(Model)
	model.mode = modeCompose
	model.sendInput.SetValue("look at this:")

	updated, _ = model.Update(tea.KeyPressMsg{Code: 'v', Mod: tea.ModCtrl})
	model = updated.(Model)

	value := model.sendInput.Value()
	if !strings.HasPrefix(value, "look at this: ") {
		t.Fatalf("path fused with existing text: %q", value)
	}
	fields := strings.Fields(value)
	path := fields[len(fields)-1]
	if !strings.HasSuffix(path, ".png") ||
		!strings.Contains(path, "stormlight-paste-") {
		t.Fatalf("no pasted path in composer: %q", value)
	}
	if !strings.HasSuffix(value, " ") {
		t.Fatalf("no trailing space after pasted path: %q", value)
	}
	if model.alert.err != nil {
		t.Fatalf("unexpected error: %v", model.alert.err)
	}
	os.Remove(path)
}

func TestComposeCtrlVReportsMissingImage(t *testing.T) {
	withClipboard(t, fakeClipboard{err: errNoClipboardImage})

	model := NewModel(stubBackend{})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = updated.(Model)
	model.mode = modeCompose
	model.sendInput.SetValue("before")

	updated, _ = model.Update(tea.KeyPressMsg{Code: 'v', Mod: tea.ModCtrl})
	model = updated.(Model)

	if model.alert.err == nil || model.alert.message() != "clipboard has no image" {
		t.Fatalf("got err %v, want clipboard has no image", model.alert.err)
	}
	if got := model.sendInput.Value(); got != "before" {
		t.Fatalf("composer changed on failed paste: %q", got)
	}
}

func TestPadToken(t *testing.T) {
	cases := []struct {
		name          string
		before, after rune
		want          string
	}{
		{"empty composer", 0, 0, "p "},
		{"after word", ':', 0, " p "},
		{"after space", ' ', 0, "p "},
		{"between words", 'a', 'b', " p "},
		{"before space", 'a', ' ', " p"},
		{"line start before word", 0, 'b', "p "},
	}
	for _, c := range cases {
		if got := padToken("p", c.before, c.after); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestDecodeAppleScriptPNG(t *testing.T) {
	got, err := decodeAppleScriptPNG("«data PNGf89504e47»\n")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !bytes.Equal(got, []byte{0x89, 0x50, 0x4e, 0x47}) {
		t.Fatalf("got % x", got)
	}
	if _, err := decodeAppleScriptPNG("missing value"); err != errNoClipboardImage {
		t.Fatalf("got %v, want errNoClipboardImage", err)
	}
}
