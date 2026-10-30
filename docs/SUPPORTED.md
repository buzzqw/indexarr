# Supported sources

<!-- Generated from the catalog by go generate ./internal/catalog. Do not edit by hand. -->

Every source listed here is shipped with indexarr: no proxy, no external
definition file to keep in sync. The catalog has no index of its own — it
describes how to read the sites you configure.

**102 site definitions** plus 8 generic connectors: 86 verified, 2 degraded and 14 draft. A draft is usable but not production-ready.

When a definition is added, removed or changes state, this file is
regenerated in the same commit (`make supported-list`); the catalog test fails
while it is stale, so the list cannot describe a catalog that no longer exists.

## Reading the tables

| Column | Meaning |
| --- | --- |
| **State** | `verified` = written and checked against the real pages; `degraded` = works with a known limit; `draft` = usable but not production-ready |
| **Access** | `None` = public, `Account` = username and password, `Cookie` = pasted session cookie, `API key` = key or passkey, `Uploader` = the site's uploader name, `Endpoint` = the address of a Torznab/Newznab endpoint or a feed |
| **Language** | Primary language of the site |

## Torrent (101)

| Site | Access | Language | State | Notes |
| --- | --- | --- | --- | --- |
| [0Magnet](https://16mag.net/) | None | zh | verified | — |
| [1337x](https://1337x.tube/) | None | en | verified | unofficial mirror |
| [Academic Torrents](https://academictorrents.com/) | Endpoint | en | verified | — |
| [ACG.RIP](https://acg.rip/.xml) | Endpoint | ja | draft | — |
| [ACGNX](https://share.acgnx.se/rss.xml) | None | zh | verified | — |
| [AniBT](https://anibt.net/rss/magnets.xml) | Endpoint | zh | verified | — |
| [AniLiberty](https://aniliberty.top/) | None | ru | verified | — |
| [AnimeTosho](https://animetosho.org/) | None | en | verified | — |
| [AniRena](https://www.anirena.com/) | None | en | verified | — |
| [AudioBook Bay](https://audiobookbay.lu/) | None | en | verified | — |
| [Bangumi Moe](https://bangumi.moe/) | None | zh | verified | — |
| [BigFANGroup](https://bigfangroup.org/) | None | ru | verified | — |
| [Bitsearch](https://bitsearch.eu/) | None | en | verified | — |
| [BlueRoms](https://www.blueroms.ws/) | None | en | verified | — |
| [BT.etree](https://bt.etree.org/) | None | en | verified | — |
| [BT4G](https://bt4gprx.com/) | None | en | verified | — |
| [BTDigg](https://btdig.com/) | None | en | verified | — |
| [BTdirectory](https://btmulu.live/) | None | zh | verified | — |
| [Catorrent](https://catorrent.net/) | None | ru | verified | — |
| [CorsaroBlu](https://ilcorsaroblu.org/) | None | it | verified | xbtit family |
| [CorsaroBlu (account)](https://ilcorsaroblu.org/) | Cookie | it | draft | — |
| [CorsaroBlu (uploader)](https://ilcorsaroblu.org/) | Uploader | it | draft | xbtit family |
| [CrackingPatching](https://crackingpatching.com/feed) | Endpoint | en | draft | — |
| [DaMagNet](https://damag.net/) | None | en | verified | — |
| [DivxTotal](https://divxtotal.foo/) | None | es | verified | — |
| [dmhy](https://share.dmhy.org/topics/rss/rss.xml) | Endpoint | zh | draft | — |
| [E-Hentai](https://e-hentai.org/) | None | en | verified | — |
| [EBookBay](http://ebb.la/) | None | en | verified | — |
| [ExtraTorrent.st](https://extratorrent.st/) | None | en | verified | — |
| [ExtTo](https://extto.org/) | None | en | verified | — |
| [EZTV](https://eztvx.to/) | Endpoint | en | verified | — |
| [FOSS Torrents](https://fosstorrents.com/) | None | en | verified | — |
| [Free JAV Torrent](https://www.freejavtorrent.com/) | None | en | verified | — |
| [ilDraGoNeRo](https://www.ildragonero2.info/forum/) | Account | it | verified | — |
| [Internet Archive](https://archive.org/) | None | en | verified | — |
| [IPTorrents](https://iptorrents.com/) | Cookie | en | verified | — |
| [Kickass Torrents (KAT mirror)](https://thekat.info/) | None | en | verified | unofficial mirror |
| [Kickass Torrents (kickasstorrents.to)](https://kickass.torrentbay.st/) | None | en | verified | — |
| [Knaben (ITA feed)](https://rss.knaben.org/ita) | Endpoint | it | verified | — |
| [Knaben (search)](https://knaben.org/) | None | en | verified | — |
| [LibriVox](https://librivox.org/) | None | en | verified | — |
| [LimeTorrents](https://www.limetorrents.fun/) | None | en | verified | — |
| [LinuxTracker](https://linuxtracker.org/) | None | en | verified | — |
| [Mac Torrents](https://www.torrentmac.net/) | None | en | verified | — |
| [Mac Torrents Download](https://www.torrentmac.net/feed/) | Endpoint | en | draft | — |
| [Magnet Cat](https://magnetcatcat.com/) | None | zh | verified | — |
| [MagnetDownload](https://www.magnetdownload.com/) | None | en | verified | — |
| [MegaPeer](https://megapeer.vip/) | None | ru | verified | — |
| [Mikan Project](https://mikanani.me/) | None | zh | verified | — |
| [MirCrew](https://mircrew-releases.org/) | Account | it | verified | — |
| [MirCrew (Smartfeed)](https://mircrew-releases.org/) | Endpoint | it | verified | — |
| [MixtapeTorrent](http://www.mixtapetorrent.com/) | None | en | verified | — |
| [MyPornClub](https://myporn.club/) | None | en | verified | — |
| [nekoBT](https://nekobt.to/) | None | en | verified | — |
| [Nipponsei](https://nipponsei.minglong.org/tracker/rss.php) | Endpoint | ja | draft | — |
| [Nyaa](https://nyaa.si/) | None | en | verified | — |
| [PandaCD](https://pandacd.io/) | None | en | verified | — |
| [PC-torrent](https://pc-torrents.games/) | None | ru | verified | — |
| [plugintorrent](https://plugintorrent.com/feed/) | Endpoint | en | draft | — |
| [PornoTorrent](https://pornotorrent.com.br/) | None | pt | verified | — |
| [PornRips](https://pornrips.to/feed/) | Endpoint | en | draft | — |
| [PornXLab](https://www.pornxlab.com/) | None | en | verified | — |
| [RinTor.NeT](https://www.rintor.net/) | None | ru | verified | — |
| [RuTor](https://rutor.info/) | None | ru | verified | — |
| [RuTracker.RU](http://rutracker.ru/) | None | ru | verified | — |
| [Sexy-Pics](https://www.sexy-pics.us/) | None | en | verified | — |
| [Shana Project](https://www.shanaproject.com/) | None | en | verified | — |
| [showRSS](https://showrss.info/) | Endpoint | en | verified | — |
| [SkidrowRepack](https://skidrowrepack.com/) | None | en | verified | — |
| [SolidTorrents](https://solidtorrents.eu/) | None | en | verified | — |
| [sosulki](https://sosulki.bosk.cc/) | None | ru | verified | — |
| [SubsPlease](https://subsplease.org/) | Endpoint | en | verified | — |
| [Sukebei](https://sukebei.nyaa.si/?page=rss) | Endpoint | en | verified | — |
| [sukebei.nyaa.si](https://sukebei.nyaa.si/rss) | Endpoint | ja | draft | — |
| [The Pirate Bay](https://apibay.org/) | None | en | verified | — |
| [TheRARBG](https://therarbg.to/) | None | en | verified | — |
| [TokyoTosho](https://www.tokyotosho.info/) | Endpoint | en | verified | — |
| [Torlock](https://www.torlock.com/) | Endpoint | en | verified | — |
| [Torrent Kitty](https://www.torrentkitty.cam/) | None | zh | verified | — |
| [torrent.by](https://torrent.by/) | None | ru | verified | — |
| [Torrent9](https://www7.torrent9.to/) | None | fr | verified | — |
| [TorrentByte](https://torrentbyte.cc/) | None | en | degraded | — |
| [TorrentDownload](https://www.torrentdownload.info/) | None | en | verified | — |
| [TorrentDownloads](https://www.torrentdownloads.pro/) | None | en | verified | — |
| [TorrentFunk](https://www.torrentfunk.com/) | Endpoint | en | verified | — |
| [TorrentGalaxy](https://torrentgalaxy.info/) | Uploader | en | verified | — |
| [TorrentGalaxyClone](https://torrentgalaxy.info/) | None | en | verified | — |
| [TorrentLeech](https://www.torrentleech.org/) | Account | en | draft | — |
| [TorrentLeech (RSS)](https://rss.torrentleech.org/) | Endpoint | en | draft | — |
| [TorrentProject2](https://torrentproject.cc/) | None | en | verified | — |
| [TorrentsCSV](https://torrents-csv.com/) | None | en | verified | — |
| [Torrentsome](https://torrentsome269.com/) | None | ko | degraded | — |
| [Torrenttip](https://torrenttip249.top/) | None | ko | verified | — |
| [U3C3](https://u3c3.com/) | None | zh | verified | — |
| [UIndex](https://uindex.org/) | None | en | verified | — |
| [VST Torrentz](https://vsttorrentz.net/feed/) | Endpoint | en | draft | — |
| [VSTorrent](https://vstorrent.org/feed/) | Endpoint | en | draft | — |
| [XXXClub](https://xxxclub.to/) | None | en | verified | — |
| [XXXTor](https://xxxtor.com/) | None | ru | verified | — |
| [YTS](https://yts.gg/) | None | en | verified | unofficial mirror |
| [Zamunda LIFE](https://zamunda.life/) | None | bg | verified | — |

## eD2k (1)

| Site | Access | Language | State | Notes |
| --- | --- | --- | --- | --- |
| [DDUnlimited](https://ddunlimited.net/) | Account | it | verified | — |

## Family connectors and generic endpoints

These are not single sites: one definition covers every site that speaks the
same protocol or runs the same tracker software.

| Connector | Covers |
| --- | --- |
| Gazelle (generic) | Any Gazelle tracker: site address and credentials |
| Newznab (generic) | Any Newznab/Usenet indexer: full endpoint and API key |
| NexusPHP (generic) | Any NexusPHP tracker: site address and credentials |
| RSS / Atom feed | Any RSS/Atom feed with a .torrent, .nzb or magnet enclosure |
| TorrentTrader (generic) | Any TorrentTrader site: site address and credentials |
| Torznab (generic) | Any Torznab indexer: full endpoint and API key |
| UNIT3D (generic) | Any UNIT3D tracker: site address, then username and password or a session cookie |
| Xbtit (generic) | Any Xbtit site: site address and credentials |

eD2k sources are configured with `protocol: ed2k`: indexarr stores the
`ed2k://` links and sends them to aMule.

Usenet has no per-site definition yet: any Newznab indexer works through
the generic connector, and `.nzb` files are sent to SABnzbd or NZBGet.

## Not in the catalog

A site without a native definition stays out of the user list: there is no
Cardigann execution and no proxy towards Jackett or Prowlarr. An endpoint that
already speaks Torznab or Newznab is always usable through the generic
connector, with no definition of its own.
