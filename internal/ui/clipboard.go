package ui

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// imageClipboard reads an image from the system clipboard. Terminal paste
// cannot deliver image bytes to a TUI, so Spanreed bridges the gap: Ctrl-v
// reads the clipboard through this seam, and tests (or future platforms)
// substitute their own implementation.
type imageClipboard interface {
	// ReadImage returns the clipboard image as PNG bytes, or
	// errNoClipboardImage when the clipboard holds no image.
	ReadImage() ([]byte, error)
}

var errNoClipboardImage = errors.New("clipboard has no image")

// systemClipboard is the process-wide clipboard; tests replace it.
var systemClipboard imageClipboard = platformClipboard{}

type platformClipboard struct{}

func (platformClipboard) ReadImage() ([]byte, error) {
	switch runtime.GOOS {
	case "darwin":
		return readDarwinClipboardImage()
	case "linux":
		return readLinuxClipboardImage()
	default:
		return nil, fmt.Errorf("image paste is not supported on %s", runtime.GOOS)
	}
}

func readDarwinClipboardImage() ([]byte, error) {
	if path, err := exec.LookPath("pngpaste"); err == nil {
		out, err := exec.Command(path, "-").Output()
		if err != nil || len(out) == 0 {
			// pngpaste exits nonzero when the clipboard has no image.
			return nil, errNoClipboardImage
		}
		return out, nil
	}
	// Fallback: osascript prints the clipboard PNG as a «data PNGf…» hex
	// literal. Slower than pngpaste but needs no extra install.
	out, err := exec.Command(
		"osascript", "-e", "get the clipboard as «class PNGf»",
	).Output()
	if err != nil {
		return nil, errNoClipboardImage
	}
	return decodeAppleScriptPNG(string(out))
}

// decodeAppleScriptPNG unwraps osascript's «data PNGf<hex>» literal.
func decodeAppleScriptPNG(out string) ([]byte, error) {
	const prefix, suffix = "«data PNGf", "»"
	trimmed := strings.TrimSpace(out)
	if !strings.HasPrefix(trimmed, prefix) || !strings.HasSuffix(trimmed, suffix) {
		return nil, errNoClipboardImage
	}
	raw, err := hex.DecodeString(trimmed[len(prefix) : len(trimmed)-len(suffix)])
	if err != nil || len(raw) == 0 {
		return nil, errNoClipboardImage
	}
	return raw, nil
}

func readLinuxClipboardImage() ([]byte, error) {
	commands := [][]string{
		{"wl-paste", "--type", "image/png"},
		{"xclip", "-selection", "clipboard", "-t", "image/png", "-o"},
	}
	found := false
	for _, command := range commands {
		path, err := exec.LookPath(command[0])
		if err != nil {
			continue
		}
		found = true
		out, err := exec.Command(path, command[1:]...).Output()
		if err == nil && len(out) > 0 {
			return out, nil
		}
	}
	if !found {
		return nil, errors.New("install wl-clipboard or xclip to paste images")
	}
	return nil, errNoClipboardImage
}

// pasteClipboardImage parks the clipboard image in a temp file and returns
// its path for insertion into the composer — coding agents read image files
// referenced by path. The file is deliberately not cleaned up here: the
// agent opens it only after the message is sent, so removal is left to the
// OS temp policy.
func pasteClipboardImage() (string, error) {
	data, err := systemClipboard.ReadImage()
	if err != nil {
		return "", err
	}
	file, err := os.CreateTemp("", "stormlight-paste-*.png")
	if err != nil {
		return "", fmt.Errorf("save clipboard image: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		os.Remove(file.Name())
		return "", fmt.Errorf("save clipboard image: %w", err)
	}
	if err := file.Close(); err != nil {
		os.Remove(file.Name())
		return "", fmt.Errorf("save clipboard image: %w", err)
	}
	return file.Name(), nil
}
