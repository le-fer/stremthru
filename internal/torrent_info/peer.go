package torrent_info

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/MunifTanjim/stremthru/internal/config"
	"github.com/MunifTanjim/stremthru/internal/db"
	"github.com/MunifTanjim/stremthru/internal/imdb_torrent"
	ts "github.com/MunifTanjim/stremthru/internal/torrent_stream"
	"github.com/MunifTanjim/stremthru/internal/util"
	"golang.org/x/text/unicode/norm"
)

// UpsertFromPeer preserves verified whole-title relationships from an IMDb
// lookup. Episode relevance comes from stored files and parsed metadata, never
// from the episode that happened to trigger the pull.
func UpsertFromPeer(items []TorrentInfoInsertData, sid string) error {
	if err := upsert(items, "", false, true); err != nil {
		return err
	}
	sid = ts.CleanStremId(sid)
	// AniDB relationships need numbering conversions; retain the existing anime
	// import path rather than treating parsed TV/absolute episodes as AniDB ones.
	if !strings.HasPrefix(sid, "tt") {
		return nil
	}

	hashes := []string{}
	seen := util.NewSet[string]()
	for _, item := range items {
		if len(item.Hash) != 40 || item.Source.HasUntrustedData() || shouldDiscardTorrentTitle(item.Hash, item.TorrentTitle) || seen.Has(item.Hash) {
			continue
		}
		seen.Add(item.Hash)
		hashes = append(hashes, item.Hash)
	}
	if len(hashes) == 0 {
		return nil
	}
	// A peer lookup may contain a collection returned for just one mapped file.
	// The requested ID alone does not establish the identity of the whole torrent.
	var title, originalTitle string
	var year int
	err := db.GetPrimary().QueryRow("SELECT title, orig_title, year FROM imdb_title WHERE tid = ?", sid).Scan(&title, &originalTitle, &year)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	title, originalTitle = normalizePeerTitle(title), normalizePeerTitle(originalTitle)
	for chunk := range slices.Chunk(hashes, 150) {
		stored, err := parsePeerImports(chunk)
		if err != nil {
			return err
		}
		args := []any{sid}
		eligible := []string{}
		for _, hash := range chunk {
			info := stored[hash]
			parsedTitle := normalizePeerTitle(info.Title)
			if parsedTitle == "" || (parsedTitle != title && parsedTitle != originalTitle) || (year != 0 && info.Year != 0 && info.Year != year) {
				continue
			}
			eligible = append(eligible, hash)
			args = append(args, hash, info.TorrentTitle, info.Title, info.Year)
		}
		if skipped := len(chunk) - len(eligible); skipped > 0 {
			log.Debug("skipped peer fallback mappings without matching IMDb title identity", "sid", sid, "count", skipped)
		}
		if len(eligible) == 0 {
			continue
		}

		// A peer lookup is fallback evidence. Do not add a competing title to a
		// torrent with a nonempty mapping or files explicitly mapped elsewhere.
		query := `INSERT INTO imdb_torrent (hash, tid)
			SELECT ti.hash, ? FROM torrent_info ti
			WHERE (ti.hash, ti.t_title, ti.title, ti.year) IN (` + util.RepeatJoin("(?,?,?,?)", len(eligible), ",") + `)
			AND ti.parser_input = ti.t_title
			AND NOT EXISTS (SELECT 1 FROM imdb_torrent it WHERE it.hash = ti.hash AND it.tid != '')
			AND NOT EXISTS (SELECT 1 FROM torrent_stream ts WHERE ts.h = ti.hash
				AND ts.sid LIKE 'tt%' AND ts.sid != ? AND ts.sid NOT LIKE ?)
			ON CONFLICT (hash, tid) DO NOTHING`
		args = append(args, sid, sid+":%")
		if err := imdb_torrent.WithHashLocks(eligible, func(executor db.Executor) error {
			_, err := executor.Exec(query, args...)
			return err
		}); err != nil {
			return err
		}
	}
	if title == "" && originalTitle == "" && config.Feature.HasIMDBTitle() {
		// With IMDb sync enabled, a fresh instance can import torrents before
		// the dataset is ready.
		// Keep that pull retryable so the caller cannot cache an incomplete result
		// or consume its successful-pull cooldown while identity is unavailable.
		return fmt.Errorf("IMDb title metadata unavailable for peer import: %s", sid)
	}
	return nil
}

// Re-read after a guarded parsed write: another importer or parser may have
// changed the title between our read and write. Keep retries bounded so a hot
// torrent reports a retryable import failure instead of consuming the cooldown.
func parsePeerImports(hashes []string) (map[string]TorrentInfo, error) {
	const maxAttempts = 3
	for attempt := 0; attempt <= maxAttempts; attempt++ {
		stored, err := GetByHashesFromPrimary(hashes)
		if err != nil {
			return nil, err
		}
		if len(stored) != len(hashes) {
			return nil, fmt.Errorf("missing torrent metadata after peer import")
		}
		parsed := []*TorrentInfo{}
		for _, info := range stored {
			if info.IsParsed() {
				continue
			}
			if attempt == maxAttempts {
				return nil, fmt.Errorf("torrent metadata changed while parsing peer import: %s", info.Hash)
			}
			if err := info.Parse(); err != nil {
				return nil, err
			}
			parsed = append(parsed, &info)
		}
		if len(parsed) == 0 {
			return stored, nil
		}
		if err := UpsertParsed(parsed); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("torrent metadata changed while parsing peer import")
}

// Compare identity, not formatting: release names commonly replace punctuation
// and whitespace with dots. Keep letters and digits, including non-Latin titles.
// Compose equivalent Unicode spellings before filtering combining characters.
func normalizePeerTitle(title string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, norm.NFC.String(title))
}
