package core

import (
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/MunifTanjim/stremthru/internal/util"
)

func UnmarshalJSON(statusCode int, body []byte, v any) error {
	if statusCode == 204 && len(strings.TrimSpace(string(body))) == 0 {
		return nil
	}

	err := json.Unmarshal(body, v)
	if err == nil {
		return nil
	}

	bodySample := string(body)
	if len(bodySample) > 1000 {
		bodySample = bodySample[0:1000] + " ..."
	}

	bodySample = strings.Replace(bodySample, "\n", "\\n", -1)

	return fmt.Errorf(
		"Couldn't deserialize JSON (response status: %v, body sample: '%s'): %v",
		statusCode, bodySample, err,
	)
}

type MagnetLink struct {
	Hash     string // xt - exact topic
	Link     string
	Name     string   // dn - display name
	Trackers []string // tr - address tracker
	RawLink  string
}

func NormalizeMagnetHash(hash string) string {
	switch len(hash) {
	case 40:
		decoded, err := hex.DecodeString(hash)
		if err != nil || len(decoded) != 20 {
			return ""
		}
		return strings.ToLower(hash)
	case 32:
		if decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(hash)); err != nil {
			return ""
		} else {
			return strings.ToLower(hex.EncodeToString(decoded))
		}
	default:
		return ""
	}
}

// IsMagnetHashV2 reports whether hash is a hexadecimal BitTorrent v2 infohash.
// V2 hashes can be exposed by providers but are not supported by v1-only
// persistence, matching, or playback paths.
func IsMagnetHashV2(hash string) bool {
	if len(hash) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(hash)
	return err == nil && len(decoded) == 32
}

func ParseMagnetLink(value string) (MagnetLink, error) {
	magnet := MagnetLink{}
	if !strings.HasPrefix(value, "magnet:") {
		magnet.Hash = NormalizeMagnetHash(value)
		if magnet.Hash == "" {
			return magnet, errors.New("invalid magnet")
		}
		magnet.Link = "magnet:?xt=urn:btih:" + magnet.Hash
		magnet.RawLink = magnet.Link
		return magnet, nil
	}

	u, err := url.Parse(value)
	if err != nil {
		return magnet, err
	}
	params := u.Query()
	for _, xt := range params["xt"] {
		prefix, hash, found := strings.Cut(xt, ":")
		if !found || !strings.EqualFold(prefix, "urn") {
			continue
		}
		namespace, hash, found := strings.Cut(hash, ":")
		if !found || !strings.EqualFold(namespace, "btih") {
			continue
		}
		if normalized := NormalizeMagnetHash(hash); normalized != "" {
			magnet.Hash = normalized
			break
		}
	}
	if magnet.Hash == "" {
		return magnet, errors.New("invalid magnet")
	}
	magnet.Name = params.Get("dn")
	if params.Has("tr") {
		magnet.Trackers = params["tr"]
		params.Del("tr")
	}
	magnet.Link = "magnet:?xt=" + "urn:btih:" + magnet.Hash
	if magnet.Name != "" {
		magnet.Link = magnet.Link + "&dn=" + magnet.Name
	}
	magnet.RawLink = value
	return magnet, nil
}

func HasVideoExtension(filename string) bool {
	return util.FileExtVideo.Has(finalExtension(filename))
}

func HasDisallowedExtension(filename string) bool {
	return hasDisallowedExtension(filename, util.FileExtDisallowed)
}

func hasDisallowedExtension(filename string, extensions *util.Set[string]) bool {
	return extensions.Has(finalExtension(filename))
}

func finalExtension(filename string) string {
	filename = strings.ReplaceAll(filename, "\\", "/")
	filename = strings.TrimRight(filename, " .")
	return strings.ToLower(filepath.Ext(filename))
}

type FileClassification struct {
	Disallowed   bool
	AllowedVideo bool
}

// ClassifyFile checks both identifiers for blocked extensions, then classifies
// the name as a video (using the path only when the name is empty).
func ClassifyFile(name, path string) FileClassification {
	if HasDisallowedExtension(name) || HasDisallowedExtension(path) {
		return FileClassification{Disallowed: true}
	}
	if name == "" {
		name = path
	}
	return FileClassification{AllowedVideo: HasVideoExtension(name)}
}

func IsAllowedVideoFile(name, path string) bool {
	return ClassifyFile(name, path).AllowedVideo
}

// NormalizeMagnetHashes accepts infohashes or magnet links, drops values without
// a supported hash, and returns unique canonical hashes in their input order.
func NormalizeMagnetHashes(values []string) []string {
	hashes := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		magnet, err := ParseMagnetLink(value)
		if err != nil || magnet.Hash == "" {
			continue
		}
		if _, exists := seen[magnet.Hash]; !exists {
			hashes = append(hashes, magnet.Hash)
			seen[magnet.Hash] = struct{}{}
		}
	}
	return hashes
}
