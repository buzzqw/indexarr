package engine

import "errors"

// ErrLinkGone marks a download link the site no longer serves: the release page
// answered 404 or 410. It is not a temporary failure, so a caller counts it on
// the source and quarantines it instead of opening the same dead page at every
// pass. Release pages are removed from public trackers long before the rows
// that point at them leave the cache.
var ErrLinkGone = errors.New("engine: the release link is gone")
