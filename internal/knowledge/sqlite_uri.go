package knowledge

import (
	"errors"
	"net/url"
	"path/filepath"
	"strings"
)

func sqliteFileURI(path string, query url.Values) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	normalized := filepath.ToSlash(absolute)
	uri := url.URL{Scheme: "file", RawQuery: query.Encode()}
	volume := filepath.VolumeName(absolute)
	if strings.HasPrefix(volume, `\\`) {
		withoutPrefix := strings.TrimPrefix(normalized, "//")
		host, remainder, found := strings.Cut(withoutPrefix, "/")
		if !found || host == "" || remainder == "" {
			return "", errors.New("invalid UNC SQLite path")
		}
		uri.Host = host
		uri.Path = "/" + remainder
	} else {
		if volume != "" && !strings.HasPrefix(normalized, "/") {
			normalized = "/" + normalized
		}
		uri.Path = normalized
	}
	return uri.String(), nil
}
