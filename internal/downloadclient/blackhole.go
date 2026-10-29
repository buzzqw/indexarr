package downloadclient

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// blackholeClient writes the metafile into a folder watched by another
// program. It needs no address: SavePath is the folder and URL stays empty.
type blackholeClient struct {
	cfg    Config
	kind   string
	folder string
}

func newBlackhole(cfg Config, kind string) (*blackholeClient, error) {
	folder := strings.TrimSpace(cfg.SavePath)
	if folder == "" {
		return nil, fmt.Errorf("Blackhole needs a save folder")
	}
	return &blackholeClient{cfg: cfg, kind: kind, folder: folder}, nil
}

func (c *blackholeClient) label() string {
	if c.kind == KindUsenetBlackhole {
		return "Usenet Blackhole"
	}
	return "Torrent Blackhole"
}

func (c *blackholeClient) Send(ctx context.Context, item Item) error {
	if err := checkItemSize(item); err != nil {
		return err
	}
	switch c.kind {
	case KindUsenetBlackhole:
		if len(item.NZB) == 0 {
			return fmt.Errorf("Usenet Blackhole needs an .nzb file")
		}
		return c.write(safeTransferFilename(item.Title, "nzb"), item.NZB)
	default:
		switch {
		case len(item.Torrent) > 0:
			return c.write(safeTransferFilename(item.Title, "torrent"), item.Torrent)
		case strings.TrimSpace(item.Magnet) != "":
			return c.write(safeTransferFilename(item.Title, "magnet"), []byte(item.Magnet))
		default:
			return fmt.Errorf("Torrent Blackhole needs a .torrent file or a magnet link")
		}
	}
}

func (c *blackholeClient) write(name string, data []byte) error {
	if err := os.MkdirAll(c.folder, 0o750); err != nil {
		return fmt.Errorf("create the Blackhole folder: %w", err)
	}
	target := filepath.Join(c.folder, name)
	temp, err := os.CreateTemp(c.folder, ".indexarr-*")
	if err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	tempName := temp.Name()
	cleanup := func() {
		temp.Close()
		os.Remove(tempName)
	}
	if _, err := temp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("write %s: %w", name, err)
	}
	if err := temp.Chmod(0o640); err != nil {
		cleanup()
		return fmt.Errorf("write %s: %w", name, err)
	}
	if err := temp.Close(); err != nil {
		os.Remove(tempName)
		return fmt.Errorf("write %s: %w", name, err)
	}
	if err := os.Rename(tempName, target); err != nil {
		// Windows refuses to replace an existing file; remove it and retry.
		_ = os.Remove(target)
		if retry := os.Rename(tempName, target); retry != nil {
			os.Remove(tempName)
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	return nil
}

func (c *blackholeClient) Test(ctx context.Context) (string, error) {
	if err := os.MkdirAll(c.folder, 0o750); err != nil {
		return "", fmt.Errorf("create the Blackhole folder: %w", err)
	}
	probe, err := os.CreateTemp(c.folder, ".indexarr-write-test-*")
	if err != nil {
		return "", fmt.Errorf("the Blackhole folder is not writable: %w", err)
	}
	name := probe.Name()
	probe.Close()
	os.Remove(name)
	return c.label() + " folder ready", nil
}

// truncateUTF8 keeps the string within max bytes without splitting a rune.
func truncateUTF8(value string, max int) string {
	if len(value) <= max {
		return value
	}
	for len(value) > max {
		_, size := utf8.DecodeLastRuneInString(value)
		value = value[:len(value)-size]
	}
	return value
}
