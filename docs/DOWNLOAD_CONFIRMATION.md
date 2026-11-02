# Download confirmation

indexarr records a **grab** when it hands a `.torrent`, an NZB, a magnet or an
eD2k link to an app. That is a delivery, not proof that the file arrived: the
app may still be downloading it, may have refused it, or may never have started.

A download client can tell indexarr when the payload reaches 100%. The Download
tab then marks that grab as **downloaded**; every other grab stays **delivered
only**. This document is the contract between indexarr and a client.

## Endpoint

```text
POST /api/v2/downloads/confirm
```

Authentication is the same API key as the rest of the machine API: the key used
for Torznab searches also confirms downloads. Send it in one of the accepted
forms:

- `X-Api-Key: <key>`
- `Authorization: Bearer <key>`
- `?apikey=<key>`

The body is JSON and is limited to 64 KiB. One request carries one download.

## Request

```json
{
  "event": "download.completed",
  "protocol": "torrent",
  "hash": "0123456789abcdef0123456789abcdef01234567",
  "title": "Release title",
  "client": "gextto",
  "size": 123456789,
  "completed_at": "2026-11-02T14:00:00Z"
}
```

| Field | Required | Meaning |
| --- | --- | --- |
| `event` | no | `download.completed`; an empty value is accepted, any other value is refused |
| `protocol` | yes | `torrent` or `ed2k` |
| `hash` | yes | 40 hexadecimal characters for a torrent infohash, 32 for an eD2k MD4 |
| `title` | no | the release title; used only when indexarr did not know the hash |
| `client` | no | the client name shown in the Download tab (for example `gextto`) |
| `size` | no | the completed size in bytes; informational |
| `completed_at` | no | RFC 3339 timestamp; an empty value means "now" |

## Response

```json
{"event": "download.completed", "matched": true, "grab_id": 123}
```

`matched` is `false` when no grab corresponds to the hash or the title. The
client should treat that as final: the release was not served by this indexarr,
or its history has already been pruned. The call is **idempotent**; sending the
same confirmation again changes nothing.

A malformed body, an unknown protocol or a hash of the wrong length answers
`400` with `{"error": "..."}`. A missing or wrong key answers `401`. Internal
failures answer `500` with a generic message and never echo the body.

## How indexarr matches a confirmation

1. **Release by hash.** A torrent looks up `releases.infohash`; an eD2k download
   looks up `releases.ed2k_hash`. The comparison is case-insensitive.
2. **Release by title.** When the hash is not in the cache, an exact
   case-insensitive title among the releases already marked as downloaded is
   used, and the hash is stored on that release so the next report is direct.
3. **Grab.** The newest unconfirmed `kind = 'grab'` row for that release at or
   before `completed_at` is marked. A repeated grab for the same release
   therefore does not make an older delivery look confirmed. When only the
   title matched, the search is limited to the seven days before the
   completion.
4. **Idempotence.** Only rows with `confirmed_at = 0` are updated, so a client
   can retry a request without inflating the confirmed count.

## Who sends it today

**Gextto** reports a completion after `torrent_finished` or `storage_moved`, for
torrents and eD2k files alike. It uses every enabled indexarr manager in its
Torznab list: the base URL gives `/api/v2/downloads/confirm`, and the manager's
API key authenticates the call. The request runs asynchronously with a short
timeout, never blocks the download, and the result is best effort. Because the
endpoint is idempotent, a duplicate report is harmless.

Other clients are not wired in yet. qBittorrent, Transmission, Deluge,
rTorrent, aria2, uTorrent, Flood, SABnzbd and NZBGet all expose a status or
history API that carries the hash and a completed flag, so a small bridge can
post the same JSON. Polling those APIs from indexarr is a separate feature and
is not required by this contract.

## What this does not cover

- A magnet used directly from the feed never reaches indexarr, so there is
  nothing to confirm. Enable *Send magnet downloads through indexarr* to make
  those downloads visible.
- An eD2k client that opens the `ed2k` attribute directly, without asking
  indexarr's download link, stays invisible.
- A client that never reports completion leaves its grabs as **delivered
  only**, which is the honest state: indexarr knows it handed over the link and
  nothing more.
