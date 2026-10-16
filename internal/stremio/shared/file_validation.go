package stremio_shared

import (
	"errors"

	"github.com/MunifTanjim/stremthru/core"
	"github.com/MunifTanjim/stremthru/internal/torrent_stream"
	"github.com/MunifTanjim/stremthru/store"
)

type PlaybackFileRequest struct {
	Name       string
	Index      int
	StoreCode  store.StoreCode
	AllowMixed bool
}

// ValidatePlaybackFiles leaves the supplied list unchanged and returns only allowed
// videos for matching. Explicit blocked-file requests reject instead of falling
// back to another video. The caller handles evidence tracking and presentation.
func ValidatePlaybackFiles(files []store.MagnetFile, request PlaybackFileRequest) ([]store.File, error) {
	if core.HasDisallowedExtension(request.Name) {
		return nil, errors.New("requested file has a disallowed extension")
	}

	var requestedFiles []store.File
	if request.Name != "" || (request.Index != -1 && request.StoreCode == store.StoreCodeRealDebrid) {
		requestedFiles = make([]store.File, 0, len(files))
	}
	videoFiles := make([]store.File, 0, len(files))
	hasDisallowed := false
	for i := range files {
		file := &files[i]
		if requestedFiles != nil {
			requestedFiles = append(requestedFiles, file)
		}
		classification := core.ClassifyFile(file.Name, file.Path)
		if classification.Disallowed {
			hasDisallowed = true
			continue
		}
		if classification.AllowedVideo {
			videoFiles = append(videoFiles, file)
		}
	}

	requested := MatchFileByName(requestedFiles, request.Name)
	if request.Name == "" {
		requested = MatchFileByIdx(requestedFiles, request.Index, request.StoreCode)
	}
	if requested != nil && core.ClassifyFile(requested.GetName(), requested.GetPath()).Disallowed {
		return nil, errors.New("requested file has a disallowed extension")
	}
	if hasDisallowed && !request.AllowMixed {
		return nil, errors.New("torrent contains disallowed files")
	}
	if len(videoFiles) == 0 {
		return nil, errors.New("torrent contains no allowed video files")
	}
	return videoFiles, nil
}

// StoredFilesAreViable allows a checked cached video list to clear stored
// no-video evidence. Strict blocked-file evidence cannot be cleared this way.
func StoredFilesAreViable(files torrent_stream.Files, hasCheckedVideo, allowMixed bool) bool {
	if len(files) == 0 {
		return true
	}
	hasVideo := hasCheckedVideo
	for _, file := range files {
		classification := core.ClassifyFile(file.Name, file.Path)
		if classification.Disallowed && !allowMixed {
			return false
		}
		hasVideo = hasVideo || classification.AllowedVideo
	}
	return hasVideo
}

// StoredPlaybackIsBlocked checks retained evidence before cached or fresh playback.
// Missing or non-video-only evidence does not block here; fresh playback still
// validates the provider's returned list. Blocked companions reject in strict
// mode, and blocked requests always reject.
func StoredPlaybackIsBlocked(hash string, request PlaybackFileRequest) (bool, error) {
	if core.HasDisallowedExtension(request.Name) {
		return true, nil
	}
	filesByHash, err := torrent_stream.GetFilesByHashes([]string{hash})
	if err != nil {
		return false, err
	}
	for _, file := range filesByHash[hash] {
		if !core.ClassifyFile(file.Name, file.Path).Disallowed {
			continue
		}
		// Torrent offsets and other providers' IDs cannot identify a RealDebrid file.
		if !request.AllowMixed || (request.Name != "" && file.Name == request.Name) ||
			(request.Name == "" && request.StoreCode == store.StoreCodeRealDebrid && file.Source == string(request.StoreCode) && request.Index != -1 && file.Idx == request.Index) {
			return true, nil
		}
	}
	return false, nil
}
