package stremio_torz

import (
	"strconv"

	"github.com/MunifTanjim/stremthru/core"
	stremio_shared "github.com/MunifTanjim/stremthru/internal/stremio/shared"
	"github.com/MunifTanjim/stremthru/internal/torrent_stream"
)

// Unknown/non-video-only lists retain their existing behavior. A list whose
// only videos are samples cannot provide a playable stream, even for movies.
func hasOnlySampleVideos(fileLists ...torrent_stream.Files) bool {
	hasSample := false
	for _, files := range fileLists {
		for _, file := range files {
			file.Normalize()
			if !file.IsVideo() {
				continue
			}
			if !core.IsSampleFile(file.Name, file.Path) {
				return false
			}
			hasSample = true
		}
	}
	return hasSample
}

// Stored explicit mappings take precedence. Otherwise inspect live files before
// old inferred metadata, and leave the index unknown if no episode can be proved.
func selectNonSampleTorrentFile(live, stored torrent_stream.Files, sid string, nsid *torrent_stream.NormalizedStremId) *torrent_stream.File {
	idToMatch := sid
	if nsid.IsAnime {
		idToMatch = ""
		if nsid.Id != "" {
			idToMatch = nsid.Id + ":" + nsid.Episode
		}
	}
	for _, files := range []torrent_stream.Files{stored, live} {
		for _, file := range files {
			file.Normalize()
			if file.IsVideo() && !core.IsSampleFile(file.Name, file.Path) && idToMatch != "" && (file.SId == idToMatch || file.ASId == idToMatch) {
				result := file
				return &result
			}
		}
	}
	if nsid.IsAnime {
		return nil
	}
	for _, files := range []torrent_stream.Files{live, stored} {
		var largest *torrent_stream.File
		for _, file := range files {
			file.Normalize()
			if !file.IsVideo() || core.IsSampleFile(file.Name, file.Path) {
				continue
			}
			if !nsid.IsSeries() {
				if largest == nil || file.Size > largest.Size {
					result := file
					largest = &result
				}
				continue
			}
			if nsid.Episode == "" {
				continue
			}
			data := stremio_shared.ParseSeasonEpisodeFromName(file.Name, false)
			if data.Episode != -1 && strconv.Itoa(data.Episode) == nsid.Episode && (data.Season == -1 || strconv.Itoa(data.Season) == nsid.Season) {
				result := file
				return &result
			}
		}
		if largest != nil {
			return largest
		}
	}
	return nil
}
