# Configuration

StremThru is configured using environment variables.

## Server

### `STREMTHRU_BASE_URL`

Base URL for StremThru. Used for generating callback URLs and links.

- **Default:** `http://localhost:8080`

**Example:**

```sh
STREMTHRU_BASE_URL=http://localhost:8080
```

### `STREMTHRU_LISTEN_ADDR`

Address to listen on.

**Example:**

```sh
STREMTHRU_LISTEN_ADDR=127.0.0.1:8080
```

### `STREMTHRU_PORT`

Port to listen on.

- **Default:** `8080`

**Example:**

```sh
STREMTHRU_PORT=8080
```

### `STREMTHRU_LOG_LEVEL`

Log level for the application.

| Value   | Description                       |
| ------- | --------------------------------- |
| `TRACE` | Most verbose                      |
| `DEBUG` | Debug information                 |
| `INFO`  | General information **(default)** |
| `WARN`  | Warnings                          |
| `ERROR` | Errors only                       |
| `FATAL` | Fatal errors only                 |

**Example:**

```sh
STREMTHRU_LOG_LEVEL=INFO
```

### `STREMTHRU_LOG_FORMAT`

Log output format.

| Value  | Description               |
| ------ | ------------------------- |
| `json` | JSON format **(default)** |
| `text` | Plain text format         |

**Example:**

```sh
STREMTHRU_LOG_FORMAT=json
```

### `STREMTHRU_DATA_DIR`

Directory for StremThru data files (cache, database, temporary files etc.).

- **Default:** `./data`

**Example:**

```sh
STREMTHRU_DATA_DIR=./data
```

### `STREMTHRU_IP_CHECKER`

Comma-separated list of IP checker services used to determine the machine's public IP address. Multiple checkers provide fallback — if the first fails, the next is tried.

| Value            | Description                                            |
| ---------------- | ------------------------------------------------------ |
| `api.ipify.org`  | [api.ipify.org](https://api.ipify.org)                 |
| `akamai`         | [whatismyip.akamai.com](https://whatismyip.akamai.com) |
| `amazon` / `aws` | [checkip.amazonaws.com](https://checkip.amazonaws.com) |
| `icanhazip.com`  | [icanhazip.com](https://icanhazip.com)                 |
| `ifconfig.co`    | [ifconfig.co](https://ifconfig.co/ip)                  |
| `ifconfig.io`    | [ifconfig.io](https://ifconfig.io/ip)                  |
| `ifconfig.me`    | [ifconfig.me](https://ifconfig.me/ip)                  |

- **Default:** `aws,akamai,api.ipify.org`

**Example:**

```sh
STREMTHRU_IP_CHECKER=aws,akamai,api.ipify.org
```

## Authentication {#authentication}

### `STREMTHRU_AUTH`

Comma-separated list of credentials for proxy authorization. Supports two formats:

- Plain text: `username:password`
- Base64 encoded: `dXNlcm5hbWU6cGFzc3dvcmQ=`

**Example:**

```sh
STREMTHRU_AUTH=user1:pass1,user2:pass2
```

### `STREMTHRU_AUTH_ADMIN`

Comma-separated list of admin usernames or credentials.

**Example:**

```sh
STREMTHRU_AUTH_ADMIN=user1,user3:pass3
```

## Store {#store}

### `STREMTHRU_STORE_AUTH`

Comma-separated list of store credentials in `username:store_name:store_token` format.

For proxy-authorized requests, these credentials are used to authenticate with external stores.

If `username` is `*`, it is used as a fallback for users without explicit store credentials.

| Store       | `store_name` | `store_token`        |
| ----------- | ------------ | -------------------- |
| AllDebrid   | `alldebrid`  | `<api-key>`          |
| Debrider    | `debrider`   | `<api-key>`          |
| Debrid-Link | `debridlink` | `<api-key>`          |
| EasyDebrid  | `easydebrid` | `<api-key>`          |
| Offcloud    | `offcloud`   | `<api-key>`          |
| PikPak      | `pikpak`     | `<email>:<password>` |
| Premiumize  | `premiumize` | `<api-key>`          |
| RealDebrid  | `realdebrid` | `<api-token>`        |
| TorBox      | `torbox`     | `<api-key>`          |
| Torrin      | `torrin`     | `<api-key>`          |

**Example:**

```sh
STREMTHRU_STORE_AUTH=user1:realdebrid:rd-api-token,user2:torbox:tb-api-key
```

### `STREMTHRU_STORE_CONTENT_CACHED_STALE_TIME`

Comma-separated list of stale time for cached/uncached content in `store_name:cached_stale_time:uncached_stale_time` format.

If `store_name` is `*`, it is used as a fallback.

Minimum allowed values depend on whether StremThru is connected to upstream or not:

| Upstream     | Min Cached | Min Uncached |
| ------------ | ---------- | ------------ |
| Connected    | `18h`      | `6h`         |
| Disconnected | `30m`      | `5m`         |

- **Default:** `*:24h:8h`

**Example:**

```sh
STREMTHRU_STORE_CONTENT_CACHED_STALE_TIME=*:24h:8h
```

### `STREMTHRU_STORE_CONTENT_PROXY`

Comma-separated list of store content proxy configuration in `store_name:content_proxy_config` format.

| `content_proxy_config` | Description              |
| ---------------------- | ------------------------ |
| `true`                 | Enable content proxying  |
| `false`                | Disable content proxying |

If `store_name` is `*`, it is used as a fallback.

- **Default:** `*:true`

**Example:**

```sh
STREMTHRU_STORE_CONTENT_PROXY=*:true
```

### `STREMTHRU_CONTENT_PROXY_CONNECTION_LIMIT`

Comma-separated list of content proxy connection limits per user in `username:connection_limit` format.

If `username` is `*`, it is used as a fallback.

If `connection_limit` is `0`, no limit is applied.

- **Default:** `*:0`

**Example:**

```sh
STREMTHRU_CONTENT_PROXY_CONNECTION_LIMIT=*:0
```

## Tunnel

### `STREMTHRU_HTTP_PROXY`

HTTP proxy URL. Used for tunneling traffic when configured.

**Example:**

```sh
STREMTHRU_HTTP_PROXY=http://proxy:8080
```

### `STREMTHRU_TUNNEL`

Comma-separated list of tunnel configuration in `hostname:tunnel_config` format.

| `tunnel_config` | Description                        |
| --------------- | ---------------------------------- |
| `true`          | Enable with `STREMTHRU_HTTP_PROXY` |
| `false`         | Disable                            |
| `<url>`         | Enable with specified `url`        |

If `hostname` is `*` and `tunnel_config` is `false`, only explicitly enabled hostnames will be tunneled.

If `hostname` is `[newz_nzb_grab]`, the configured tunnel is used as a fallback for Newznab nzb file grab requests when no per-hostname tunnel matches.

**Example:**

```sh
STREMTHRU_TUNNEL=*:false,example.com:true
```

::: warning
Cannot override `STREMTHRU_STORE_TUNNEL`.
:::

### `STREMTHRU_STORE_TUNNEL`

Comma-separated list of tunnel configuration for stores in `store_name:tunnel_config` format.

| `tunnel_config` | Description         |
| --------------- | ------------------- |
| `true`          | Enable tunneling    |
| `false`         | Disable tunneling   |
| `api`           | Enable for API only |

If `store_name` is `*`, it is used as a fallback.

When enabled, `STREMTHRU_HTTP_PROXY` is used to tunnel traffic for the store.

- **Default:** `*:true`

**Example:**

```sh
STREMTHRU_STORE_TUNNEL=realdebrid:true,*:false
```

::: warning
Only used when using StremThru to interact with the Store. Not affected by `STREMTHRU_TUNNEL`.
StremThru will _try_ to automatically adjust `STREMTHRU_TUNNEL` to reflect `STREMTHRU_STORE_TUNNEL`.
:::

## Peer

### `STREMTHRU_PEER_FLAG`

Comma-separated list of flags to control peer behavior.

| Value  | Description                                                           |
| ------ | --------------------------------------------------------------------- |
| `lazy` | Fetch data from peer asynchronously in background instead of blocking |

**Example:**

```sh
STREMTHRU_PEER_FLAG=lazy
```

## Vault

Vault is used for storing sensitive data, e.g. password, api key etc.

### `STREMTHRU_VAULT_SECRET`

Secret for encrypting sensitive data.

**Example:**

```sh
STREMTHRU_VAULT_SECRET=my-super-secret-vault-key
```

::: info
This config is required to use Vault in dashboard.
:::

## Database & Cache

See [Database & Cache](./database-and-cache) for details.

## Features

See [Features](./features) for details.

## Integrations

See [Integrations](./integrations) for all `STREMTHRU_INTEGRATION_*` environment variables.

## Stremio Addons

See [Stremio Addons](./stremio-addons) for all `STREMTHRU_STREMIO_*` environment variables.

## Newz

See [Newz Configuration](./newz) for all `STREMTHRU_NEWZ_*` environment variables.

## Torrent File Validation

Torz and Wrap validate supplied torrent file lists before exposing streams or
selecting files for playback.

By default, `STREMTHRU_ALLOW_VIDEO_TORRENTS_WITH_DISALLOWED_FILES=false`, which
enables **strict mode**:

- A supplied nonempty file list must contain at least one allowed video.
- Any supplied file with a disallowed extension rejects the entire torrent, even
  when the torrent also contains an allowed video.
- A cached result without a file list remains compatible; a missing file list
  alone does not cause rejection.
- Disallowed files are never selected for playback.

Set `STREMTHRU_ALLOW_VIDEO_TORRENTS_WITH_DISALLOWED_FILES=true` to permit mixed
torrents containing both allowed videos and disallowed files. Disallowed files
remain excluded from playback.

### How file lists are evaluated

Only the final extension of a supplied file name or path is checked. Matching is
case-insensitive, and trailing spaces and dots in the final component are ignored.
A parent directory extension does not count: `/Example.app/Contents/movie.mkv`
is allowed unless `.mkv` itself is blocked.

| Supplied files                           | Strict mode (default)    | Mixed torrents permitted      |
| ---------------------------------------- | ------------------------ | ----------------------------- |
| `movie.mkv`                              | Accept                   | Accept                        |
| `movie.mkv` + `subtitle.srt`             | Accept; select the video | Accept; select the video      |
| `movie.mkv` + `setup.exe`                | Reject the torrent       | Accept; select only the video |
| `setup.exe` only                         | Reject                   | Reject                        |
| `readme.txt` only                        | Reject: no allowed video | Reject: no allowed video      |
| Only explicitly blocked video extensions | Reject                   | Reject                        |

Unknown extensions are not automatically blocked. Removing an extension from the
blocked list also does not make it a video extension.

Supplied lists are validated for both cached and noncached provider statuses. A
valid list does not promote a noncached result to cached status.

### Evidence and playback behavior

Validation applies to current indexer results, retained torrent metadata, and file
lists returned by every configured store. Torz keeps its existing episode matching.
A sole checked video with no identifiable episode can preserve an otherwise
compatible candidate with an unknown file index; explicit episode mismatches and
ambiguous multi-video lists still fail matching. Playback performs its existing
file matching and single-video fallback. After those files are stored, later
requests defer the same opaque candidate until the store check rather than dropping
it during initial lookup. Wrap keeps its existing final filtering, sorting, and
duplicate priority.

Deferred reconstruction uses the checked list for episode matching and file
metadata, even when an older stored list already contains an allowed video.
Retained disallowed-file evidence is still checked first and remains definitive
in strict mode.

Evidence from different sources is combined as follows:

- A validated cached provider list containing an allowed video can supersede
  stored or current-indexer evidence that only lacks an allowed video.
- A missing list cannot clear a rejection.
- In strict mode, current or retained disallowed-file evidence is definitive and
  cannot be cleared by another store's cleaner list.
- Duplicate results for the same canonical hash are merged before validation, so
  a later duplicate cannot overwrite an earlier blocked file list.

Torz and Wrap check retained disallowed-file evidence before reusing a cached
playback link or requesting a new one. This check does not make another provider
request. Fresh playback responses are also validated before an allowed video is
matched.

This validation is separate from the WebDAV filter, which controls which
individual Usenet files appear in WebDAV listings.

### Evidence storage limitations

The database stores a file path, but not a separate provider-supplied file name.
Names read from stored metadata are therefore derived from the path. Current
responses still validate both fields.

If they conflict—for example, `Name="setup.exe"` with `Path="/movie.mkv"`—the file
is treated as blocked in the current response and is not recorded as apparently
valid video evidence. In strict mode, the list is rejected. In mixed mode, that
file is excluded and another allowed video is still required. Paths whose own
extension is blocked are retained so later checks can see that evidence.

Skipping a conflicting file write does not create a permanent rejection or remove
older metadata. A later cached answer without files can still pass under the
missing-list policy. No schema change or rewrite of existing rows is performed.

RealDebrid retains supplied disallowed-file evidence before permissive selection
or torrent replacement can omit it. Its playback response continues to list only
the selected files. A rejected torrent already reported as downloaded follows the
existing tracking path, including publication to configured Buddy or peer services.
Evidence seen before download completes is retained locally without asserting
cached status.

### `STREMTHRU_ALLOW_VIDEO_TORRENTS_WITH_DISALLOWED_FILES`

- **Default:** `false`

The default `false` value enables strict mode. Set this to `true` to keep a torrent
that contains both an allowed video and disallowed companion files.

This setting never permits a disallowed file to be selected. That includes video
extensions explicitly added to the blocked list. Wrap excludes streams that name a
disallowed file, and an explicit request for one is rejected instead of being
redirected to another video. Lists without any allowed video are rejected in both
modes.

The preference is local to the instance. It changes validation, not evidence
storage: retained paths remain available to later checks and Buddy responses,
subject to existing source-specific storage rules.

```sh
STREMTHRU_ALLOW_VIDEO_TORRENTS_WITH_DISALLOWED_FILES=true
```

### `STREMTHRU_STORE_RECHECK_MISSING_FILE_LISTS`

- **Default:** `true`

When a cached result has no file list, TorBox, Debrider, EasyDebrid and Premiumize
attempt a direct provider recheck before reusing that result. A recheck does not
guarantee that the provider will return files. Setting this to `false` allows
cached fileless answers to be reused without this direct lookup and disables the
additional cross-store recheck described below. File validation remains enabled.

This is not an exact restoration of upstream behavior for requests containing
`sid`, including normal Torz/Wrap checks. Upstream's local cache filter omitted
fileless cached entries for those requests, so the four adapters could already
reach their existing provider lookup as a cache miss. This implementation retains
those entries and lets the recheck setting control the subsequent direct lookup.

Extra provider traffic therefore depends on the request and lookup path; not every
fileless recheck adds a request compared with upstream. Nonempty cached file
lists remain subject to the existing SID filter.

#### Provider coverage

| Provider                                  | Availability lookup                                                      |
| ----------------------------------------- | ------------------------------------------------------------------------ |
| TorBox, Debrider, EasyDebrid, Premiumize  | Optional additional direct recheck for cached answers without files      |
| RealDebrid, AllDebrid, DebridLink, PikPak | Existing cache/Buddy/peer routing; no additional direct provider recheck |
| Offcloud                                  | Existing direct lookup already requests files                            |
| Torrin                                    | Remote store API; forwards `LocalOnly` as `local_only=1`                 |

Cross-store fallback can still check any remaining configured store through that
adapter's existing path. Depending on the adapter and configuration, that path may
use its cache, Buddy, peer, or a direct provider lookup.

#### `LocalOnly`, failures, and repeated rechecks

With `LocalOnly`, the four direct-recheck adapters retain the cache/Buddy-stage
result and skip the subsequent direct provider lookup. `LocalOnly` is the existing
per-request store API flag, set when the check endpoint receives a nonempty
`local_only` query value. It does not make preceding cache/Buddy/peer stages
strictly local; their existing routing remains unchanged.

If a provider request containing an additional missing-file-list recheck fails,
reusable cached answers are retained and the error is logged separately. Any
hashes without a reusable answer remain unknown and can still trigger checks of
other configured stores. The failed store is not exposed as an unconfirmed
uncached option. Across the store API, only a boolean failure marker is forwarded;
provider error details remain on the instance that made the request. Known file
evidence can still reject a retained answer.
If a peer reports a recheck failure, its returned answers are retained without
another same-provider recheck. Omitted hashes still receive the adapter's normal
direct lookup unless `LocalOnly` is set; a failure in one peer chunk does not
suppress lookup for hashes missing from another chunk.
Ordinary lookup failures retain their existing error handling when the request did
not include an additional fileless-result recheck or a retained peer recheck failure.

EasyDebrid requires one positional result per requested magnet. Ambiguous batch
lengths follow the failure handling above rather than assigning files to guessed
hashes. Debrider uses returned hashes when supplied; safely identified sparse
results remain usable, while omitted hashes retain reusable answers or remain
unknown. Hashless Debrider results use positions only for complete responses whose
supplied identities agree with that ordering. Neither adapter retries ambiguous
batches automatically.

The existing cache stale timeout does not throttle this recheck. It controls the
preceding cache/Buddy lookup, while the recheck predicate is evaluated afterward.
Consequently, a cached result that remains fileless can be rechecked again by
each later eligible non-`LocalOnly` request.

#### Recheck statistics

The dashboard's **Store Statistics** includes missing-file-list check counters in
the existing rolling one-hour window. They are subsets of **Total Calls**, not
additional events added to that total:

- **Recheck-only calls:** direct provider API calls containing only missing-file-list
  work, including file-fetch follow-ups.
- **Mixed calls:** API calls batching recheck hashes together with ordinary lookups.
- **Recheck call errors:** failed recheck-only or mixed calls, including rejected
  response shapes. File-policy rejection itself is not an API error.
- **Recheck hash attempts:** hash submissions to cache-check calls. Repeated requests
  count again; file-fetch follow-ups do not count the same hash again.

Counters describe direct calls made by this instance, not Buddy/peer requests or
provider calls on another instance. Cross-store calls triggered by a first store's
cached answer without files are attributed when the adapter actually calls the
provider; ordinary cache misses and other fallback causes are not labelled as
missing-file-list rechecks. A peer warning does not turn lookup of an unrelated
omitted hash into a recheck.

These figures do not measure net extra traffic compared with disabling the feature:
mixed batches may share a call that ordinary lookups already required. Counters are
in memory and reset on restart. Recheck failures do not persist a retry prohibition;
a still-fileless cached answer can be retried by the next eligible request.

Validation applies to the returned list; it does not establish that the provider
returned every file in the torrent.

```sh
STREMTHRU_STORE_RECHECK_MISSING_FILE_LISTS=false
```

### `STREMTHRU_STORE_RECHECK_MISSING_FILE_LISTS_CROSS_STORE`

- **Default:** `true`

When the first configured store reports a cached torrent without files, check the
hash through all remaining configured stores, potentially obtaining file
information. This applies even when the first store is RealDebrid, AllDebrid,
DebridLink or PikPak, despite those adapters having no additional direct
same-provider recheck. Both recheck settings must be `true`. Setting only this
setting to `false` keeps same-provider rechecks enabled where supported.

The remaining stores are checked concurrently. Existing fallback after a first-store
error or noncached result remains active regardless of these settings. Fallback for
a supplied list without an allowed video or a requested hash omitted from a
successful response also remains active.

Strict disallowed-file evidence is definitive and does not trigger fallback. A
valid video list from another store can clear a rejection caused only by a list
without an allowed video. A valid first-store video list does not trigger the
additional checks.

```sh
STREMTHRU_STORE_RECHECK_MISSING_FILE_LISTS=true
STREMTHRU_STORE_RECHECK_MISSING_FILE_LISTS_CROSS_STORE=false
```

### Hash identity and duplicate evidence

Supported v1 hashes are normalized to lowercase hexadecimal for current lookups,
writes, and validation. Supported base32 hashes resolve to the same canonical key.
This prevents a different spelling in a provider, Buddy, or peer response from
bypassing known evidence. Existing database rows are not rewritten, so operators
must inspect historical noncanonical rows separately.

Titles, file names, paths, and provider IDs are not lowercased. Buddy and peer
evidence keeps different supplied names for the same path until validation, while
identical path/name entries are deduplicated when recorded.

Duplicate store results for one canonical hash are merged before reuse and
validation. All supplied file evidence is retained. If any duplicate is cached,
the merged result is cached; otherwise it keeps the first result's status.

Torz excludes hashes already rejected by strict current-indexer or retained
evidence from provider availability checks. Wrap preserves Torz's existing
duplicate priority when **Include Torz** is enabled. A retained direct URL with a
matching torrent candidate still honors the checked hash's rejection; direct-only
results do not cause another provider lookup.

### Boolean values

All three boolean settings in this section accept values supported by Go's
`strconv.ParseBool`: `1`, `t`, `T`, `TRUE`, `true`, `True`, `0`, `f`, `F`,
`FALSE`, `false`, and `False`.

An unset or whitespace-only value uses the default. Invalid values stop startup.

### `STREMTHRU_DISALLOWED_EXTENSIONS`

Adjust the default blocked extensions:

```text
.exe .com .scr .msi .bat .cmd .ps1 .vbs .js .jar .lnk
.sh .bash .zsh .fish .run .appimage .deb .rpm
.app .dmg .pkg .command .scpt .scptd
.aab .apk .apks .apkm .xapk
```

- **Default:** the list above; an unset or empty value keeps it.
- `+ext` adds an extension to the blocked list.
- `-ext` removes an extension from the blocked list.
- Separate adjustments with commas or whitespace.
- A leading dot is optional; extensions are case-insensitive.
- Adjustments apply from left to right; the last adjustment for an extension wins.
- Use single extensions containing only letters and digits. Paths, wildcards,
  compound extensions such as `tar.gz`, and unsigned extensions are invalid and
  stop startup.

```sh
# Also reject PDFs; allow JavaScript files.
STREMTHRU_DISALLOWED_EXTENSIONS="+pdf,-js"

# The final adjustment keeps .exe blocked.
STREMTHRU_DISALLOWED_EXTENSIONS="-exe +exe"
```

Removing an extension from this list does not make it a video extension. The
requirement for a video in a supplied nonempty file list still applies. Startup
logs and the dashboard show the effective recheck flags and blocked extensions.

## Torz

See [Torz Configuration](./torz) for all `STREMTHRU_TORZ_*` environment variables.

## WebDAV

### `STREMTHRU_WEBDAV_FILE_EXT_FILTER`

File extension filter for WebDAV endpoints. Controls which file types are exposed.

**Format:** Comma-separated list of extensions or presets.

| Value        | Description                             |
| ------------ | --------------------------------------- |
| `:video:`    | Preset for all video file extensions    |
| `:subtitle:` | Preset for all subtitle file extensions |
| `ext`        | Include specific extension              |
| `-ext`       | Exclude specific extension from preset  |

- **Default:** `:video:,:subtitle:`

**Example:**

```sh
# Default: all video and subtitle files
STREMTHRU_WEBDAV_FILE_EXT_FILTER=:video:,:subtitle:

# Only video files, excluding .avi
STREMTHRU_WEBDAV_FILE_EXT_FILTER=:video:,-avi

# Only specific extensions
STREMTHRU_WEBDAV_FILE_EXT_FILTER=mkv,mp4,srt
```
