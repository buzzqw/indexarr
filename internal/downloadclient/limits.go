package downloadclient

import (
	"fmt"
	"strings"
)

// maxItemPayload bounds the .torrent or .nzb body sent to a client. Release
// payloads are stored in the database and can be large, but no sane metafile
// needs more than this; the limit keeps request bodies bounded.
const maxItemPayload = 32 << 20

// maxItemURI bounds a magnet or download URL copied into a request body.
const maxItemURI = 8 << 10

// checkItemSize rejects an oversized item before any network call. The title is
// not part of the check because every client derives a bounded filename from
// it.
func checkItemSize(item Item) error {
	if len(item.Torrent) > maxItemPayload {
		return fmt.Errorf("the .torrent file is larger than %d MiB", maxItemPayload>>20)
	}
	if len(item.NZB) > maxItemPayload {
		return fmt.Errorf("the .nzb file is larger than %d MiB", maxItemPayload>>20)
	}
	if len(item.Magnet) > maxItemURI {
		return fmt.Errorf("the magnet link is too long")
	}
	if len(item.URL) > maxItemURI {
		return fmt.Errorf("the download URL is too long")
	}
	if len(item.Ed2k) > maxItemURI {
		return fmt.Errorf("the eD2k link is too long")
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// safeTransferFilename derives a bounded, filename-safe name with one
// extension. A title such as "A/B: Test" becomes "A_B_ Test.torrent".
func safeTransferFilename(title, extension string) string {
	base := safeFilename(title, "release")
	suffix := "." + strings.TrimLeft(extension, ".")
	if strings.HasSuffix(strings.ToLower(base), strings.ToLower(suffix)) {
		base = base[:len(base)-len(suffix)]
	}
	base = truncateUTF8(base, 190-len(suffix))
	if strings.TrimSpace(base) == "" {
		base = "release"
	}
	return base + suffix
}
