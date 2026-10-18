package stremio_torz

import (
	"strconv"

	stremio_shared "github.com/MunifTanjim/stremthru/internal/stremio/shared"
	"github.com/MunifTanjim/stremthru/internal/torrent_stream"
	"github.com/MunifTanjim/stremthru/internal/util"
)

// selectMappedTorrentFile checks explicit SID/ASID evidence without inferring
// an episode from filenames. Normalize local values to preserve cached inputs.
func selectMappedTorrentFile(files torrent_stream.Files, stremID string, nsid *torrent_stream.NormalizedStremId) *torrent_stream.File {
	idToMatch := stremID
	if nsid.IsAnime {
		idToMatch = ""
		if nsid.Id != "" {
			idToMatch = nsid.Id + ":" + nsid.Episode
		}
	}
	if idToMatch == "" {
		return nil
	}
	for _, f := range files {
		if f.SId != idToMatch && f.ASId != idToMatch {
			continue
		}
		f.Normalize()
		if f.IsVideo() {
			file := f
			return &file
		}
	}
	return nil
}

// Live indexer filenames must not override a stored explicit file mapping,
// including mappings recorded before torrent metadata was available.
func selectIndexerTorrentFile(files, storedFiles torrent_stream.Files, stremID string, nsid *torrent_stream.NormalizedStremId, contexts ...stremio_shared.FileSeasonContext) (*torrent_stream.File, bool) {
	if file := selectMappedTorrentFile(storedFiles, stremID, nsid); file != nil {
		return file, true
	}
	return selectTorrentFile(files, stremID, nsid, contexts...)
}

// selectTorrentFile is shared by stored hashes and live indexer results. An
// series selection requires season evidence even before a file list is known.
func selectTorrentFile(files torrent_stream.Files, stremID string, nsid *torrent_stream.NormalizedStremId, contexts ...stremio_shared.FileSeasonContext) (*torrent_stream.File, bool) {
	if file := selectMappedTorrentFile(files, stremID, nsid); file != nil {
		return file, true
	}
	if nsid.IsAnime || !nsid.IsSeries() || nsid.Episode == "" {
		return nil, true
	}
	context := stremio_shared.FileSeasonContext{}
	if len(contexts) > 0 {
		context = contexts[0]
	}
	if len(files) == 0 {
		return nil, stremio_shared.FilePathMatchesSeason("/file", util.SafeParseInt(nsid.Season, -1), context)
	}
	for i := range files {
		f := files[i]
		// Normalize only the local value; indexer responses may be cached.
		f.Normalize()
		if !f.IsVideo() {
			continue
		}
		data := context.ParseName(f.Name, false)
		if data.Season != -1 && strconv.Itoa(data.Season) != nsid.Season {
			continue
		}
		if data.Episode == -1 || strconv.Itoa(data.Episode) != nsid.Episode {
			continue
		}
		if data.Season == -1 && !stremio_shared.FilePathMatchesSeason(f.Path, util.SafeParseInt(nsid.Season, -1), context) {
			continue
		}
		file := f
		return &file, true
	}
	return nil, false
}
