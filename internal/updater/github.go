package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	repository = "nbaertsch/afterburner"
	apiBase    = "https://api.github.com"
)

var ErrNoRelease = errors.New("no GitHub release exists")

type Release struct {
	TagName string  `json:"tag_name"`
	Name    string  `json:"name"`
	Assets  []Asset `json:"assets"`
}

type Asset struct {
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	BrowserDownloadURL string `json:"browser_download_url"`
	APIURL             string `json:"url"`
}

type Client struct {
	HTTP              *http.Client
	Token             string
	APIBase           string
	ManifestPublicKey []byte
}

func NewClient(ctx context.Context) Client {
	return Client{
		HTTP:    &http.Client{Timeout: 30 * time.Second},
		Token:   resolveToken(ctx),
		APIBase: apiBase,
	}
}

func (client Client) Latest(ctx context.Context) (Release, error) {
	return client.fetch(ctx, "/repos/"+repository+"/releases/latest")
}

func (client Client) Release(ctx context.Context, version string) (Release, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		return Release{}, fmt.Errorf("release version is required")
	}
	if !strings.HasPrefix(version, "v") {
		version = "v" + version
	}
	return client.fetch(ctx, "/repos/"+repository+"/releases/tags/"+url.PathEscape(version))
}

func (client Client) fetch(ctx context.Context, path string) (Release, error) {
	base := client.APIBase
	if base == "" {
		base = apiBase
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return Release{}, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", "afterburn-updater")
	if client.Token != "" {
		request.Header.Set("Authorization", "Bearer "+client.Token)
	}
	response, err := client.HTTP.Do(request)
	if err != nil {
		return Release{}, fmt.Errorf("query GitHub Releases: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return Release{}, ErrNoRelease
	}
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return Release{}, fmt.Errorf("GitHub Releases returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	var release Release
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&release); err != nil {
		return Release{}, fmt.Errorf("decode GitHub release: %w", err)
	}
	if release.TagName == "" {
		return Release{}, fmt.Errorf("GitHub release is missing a tag")
	}
	return release, nil
}

func IsNewer(current, candidate string) bool {
	if current == "" || current == "dev" {
		return true
	}
	return compareVersion(candidate, current) > 0
}

func resolveToken(ctx context.Context) string {
	for _, name := range []string{"GITHUB_TOKEN", "GH_TOKEN"} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	command := exec.CommandContext(ctx, "gh", "auth", "token")
	output, err := command.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func compareVersion(left, right string) int {
	normalize := func(value string) []int {
		value = strings.TrimPrefix(strings.TrimSpace(value), "v")
		var result []int
		for _, field := range strings.FieldsFunc(value, func(r rune) bool { return r < '0' || r > '9' }) {
			var number int
			fmt.Sscanf(field, "%d", &number)
			result = append(result, number)
		}
		return result
	}
	a, b := normalize(left), normalize(right)
	for i := 0; i < len(a) || i < len(b); i++ {
		var av, bv int
		if i < len(a) {
			av = a[i]
		}
		if i < len(b) {
			bv = b[i]
		}
		if av > bv {
			return 1
		}
		if av < bv {
			return -1
		}
	}
	return 0
}
