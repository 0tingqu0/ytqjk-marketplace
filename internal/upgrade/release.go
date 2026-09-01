package upgrade

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	repository       = "0tingqu0/ytqjk-marketplace"
	latestReleaseURL = "https://api.github.com/repos/" + repository + "/releases/latest"
	maxMetadataBytes = 1024 * 1024
	maxArchiveBytes  = 64 * 1024 * 1024
	maxBinaryBytes   = 96 * 1024 * 1024
)

var (
	semverPattern    = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	assetNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

type Error struct {
	Code  string
	cause error
}

func (e *Error) Error() string { return e.Code }
func (e *Error) Unwrap() error { return e.cause }

func failure(code string, cause error) error { return &Error{Code: code, cause: cause} }

type Asset struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Size int64  `json:"size"`
}

type Release struct {
	Version string           `json:"version"`
	Tag     string           `json:"tag"`
	PageURL string           `json:"page_url"`
	Assets  map[string]Asset `json:"-"`
}

type CheckResult struct {
	CurrentVersion  string `json:"current_version"`
	LatestVersion   string `json:"latest_version"`
	UpdateAvailable bool   `json:"update_available"`
	ReleaseURL      string `json:"release_url"`
}

type Client struct {
	httpClient *http.Client
	latestURL  string
}

func NewClient() *Client {
	client := &http.Client{Timeout: 60 * time.Second}
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) > 5 {
			return errors.New("too many redirects")
		}
		if !trustedDownloadURL(request.URL) {
			return errors.New("untrusted redirect")
		}
		request.Header.Del("Authorization")
		request.Header.Del("Cookie")
		return nil
	}
	return &Client{httpClient: client, latestURL: latestReleaseURL}
}

func (c *Client) Latest(ctx context.Context) (Release, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.latestURL, nil)
	if err != nil {
		return Release{}, failure("RELEASE_REQUEST_INVALID", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "ytqjk-go-updater")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return Release{}, failure("RELEASE_METADATA_UNAVAILABLE", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Release{}, failure("RELEASE_METADATA_UNAVAILABLE", fmt.Errorf("status %d", response.StatusCode))
	}
	body, err := readLimited(response.Body, response.ContentLength, maxMetadataBytes)
	if err != nil {
		return Release{}, failure("RELEASE_METADATA_INVALID", err)
	}
	return parseRelease(body)
}

func (c *Client) Check(ctx context.Context, current string) (CheckResult, Release, error) {
	if _, err := parseVersion(current); err != nil {
		return CheckResult{}, Release{}, failure("CURRENT_VERSION_INVALID", err)
	}
	release, err := c.Latest(ctx)
	if err != nil {
		return CheckResult{}, Release{}, err
	}
	newer, err := IsNewer(release.Version, current)
	if err != nil {
		return CheckResult{}, Release{}, failure("RELEASE_VERSION_INVALID", err)
	}
	return CheckResult{
		CurrentVersion: current, LatestVersion: release.Version,
		UpdateAvailable: newer, ReleaseURL: release.PageURL,
	}, release, nil
}

func (c *Client) Download(ctx context.Context, source, destination string, limit int64) (string, error) {
	parsed, err := url.Parse(source)
	if err != nil || !trustedDownloadURL(parsed) {
		return "", failure("RELEASE_DOWNLOAD_URL_INVALID", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return "", failure("RELEASE_DOWNLOAD_FAILED", err)
	}
	request.Header.Set("Accept", "application/octet-stream")
	request.Header.Set("User-Agent", "ytqjk-go-updater")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return "", failure("RELEASE_DOWNLOAD_FAILED", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", failure("RELEASE_DOWNLOAD_FAILED", fmt.Errorf("status %d", response.StatusCode))
	}
	if response.ContentLength > limit {
		return "", failure("RELEASE_DOWNLOAD_TOO_LARGE", nil)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return "", failure("RELEASE_STAGE_FAILED", err)
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", failure("RELEASE_STAGE_FAILED", err)
	}
	digest := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, digest), io.LimitReader(response.Body, limit+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || written > limit {
		_ = os.Remove(destination)
		if written > limit {
			return "", failure("RELEASE_DOWNLOAD_TOO_LARGE", nil)
		}
		return "", failure("RELEASE_DOWNLOAD_FAILED", errors.Join(copyErr, closeErr))
	}
	return fmt.Sprintf("%x", digest.Sum(nil)), nil
}

func parseRelease(body []byte) (Release, error) {
	var payload struct {
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Tag        string `json:"tag_name"`
		PageURL    string `json:"html_url"`
		Assets     []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
			Size int64  `json:"size"`
		} `json:"assets"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	if err := decoder.Decode(&payload); err != nil {
		return Release{}, failure("RELEASE_METADATA_INVALID", err)
	}
	if payload.Draft || payload.Prerelease {
		return Release{}, failure("RELEASE_NOT_STABLE", nil)
	}
	if !strings.HasPrefix(payload.Tag, "v") || len(payload.Tag) < 2 {
		return Release{}, failure("RELEASE_VERSION_INVALID", nil)
	}
	version := strings.TrimPrefix(payload.Tag, "v")
	if _, err := parseVersion(version); err != nil {
		return Release{}, failure("RELEASE_VERSION_INVALID", err)
	}
	expectedPage := "https://github.com/" + repository + "/releases/tag/" + payload.Tag
	if payload.PageURL != expectedPage {
		return Release{}, failure("RELEASE_PAGE_URL_INVALID", nil)
	}
	release := Release{Version: version, Tag: payload.Tag, PageURL: payload.PageURL, Assets: map[string]Asset{}}
	expectedPrefix := expectedPage[:strings.LastIndex(expectedPage, "/tag/")] + "/download/" + payload.Tag + "/"
	for _, candidate := range payload.Assets {
		if !assetNamePattern.MatchString(candidate.Name) || candidate.Size < 1 || candidate.Size > maxBinaryBytes ||
			release.Assets[candidate.Name].Name != "" {
			return Release{}, failure("RELEASE_ASSET_INVALID", nil)
		}
		if !strings.HasPrefix(candidate.URL, expectedPrefix) {
			return Release{}, failure("RELEASE_ASSET_URL_INVALID", nil)
		}
		release.Assets[candidate.Name] = Asset{Name: candidate.Name, URL: candidate.URL, Size: candidate.Size}
	}
	return release, nil
}

func BinaryAssetName() (string, error) {
	return binaryAssetName(runtime.GOOS, runtime.GOARCH)
}

func binaryAssetName(goos, goarch string) (string, error) {
	if goarch != "amd64" || goos != "linux" && goos != "windows" {
		return "", failure("PLATFORM_NOT_SUPPORTED", nil)
	}
	suffix := ""
	if goos == "windows" {
		suffix = ".exe"
	}
	return "ytqjk-" + goos + "-" + goarch + suffix, nil
}

func ArchiveAssetName() (string, error) {
	return archiveAssetName(runtime.GOOS, runtime.GOARCH)
}

func archiveAssetName(goos, goarch string) (string, error) {
	if goarch != "amd64" || goos != "linux" && goos != "windows" {
		return "", failure("PLATFORM_NOT_SUPPORTED", nil)
	}
	if goos == "windows" {
		return "ytqjk-windows-amd64.zip", nil
	}
	return "ytqjk-linux-amd64.tar.gz", nil
}

func IsNewer(candidate, current string) (bool, error) {
	left, err := parseVersion(candidate)
	if err != nil {
		return false, err
	}
	right, err := parseVersion(current)
	if err != nil {
		return false, err
	}
	for index := range left {
		if left[index] != right[index] {
			return left[index] > right[index], nil
		}
	}
	return false, nil
}

func parseVersion(value string) ([3]int, error) {
	if !semverPattern.MatchString(value) {
		return [3]int{}, errors.New("version must be pure SemVer")
	}
	parts := strings.Split(value, ".")
	var result [3]int
	for index, part := range parts {
		value, err := strconv.ParseInt(part, 10, 31)
		if err != nil {
			return [3]int{}, err
		}
		result[index] = int(value)
	}
	return result, nil
}

func trustedDownloadURL(value *url.URL) bool {
	if value == nil || value.Scheme != "https" || value.User != nil {
		return false
	}
	switch strings.ToLower(value.Hostname()) {
	case "api.github.com", "github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com":
		return true
	default:
		return false
	}
}

func readLimited(reader io.Reader, declared, limit int64) ([]byte, error) {
	if declared > limit {
		return nil, errors.New("response is too large")
	}
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("response is too large")
	}
	return data, nil
}
