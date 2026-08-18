package remote

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// releaseHost is where published builds live. A host being prepared from
// this machine gets the archive for its own platform, fetched here and
// checked here, rather than a URL handed to it to trust on its own.
const releaseHost = "https://github.com/trentkm/stormlight/releases/download"

// downloadTimeout bounds fetching one archive. They are tens of
// megabytes; a minute is generous and a stall is not a thing to wait out
// inside a popup.
const downloadTimeout = 60 * time.Second

// Target is a platform in the naming published builds use.
type Target struct {
	OS   string
	Arch string
}

func (t Target) String() string { return t.OS + "/" + t.Arch }

// TargetFor reads `uname -sm` the way Go names the same platform.
//
// The two vocabularies disagree in exactly the places that matter:
// `aarch64` and `arm64` are one machine with two names, as are `x86_64`
// and `amd64`, and picking the wrong one downloads a binary that will
// not run.
func TargetFor(platform string) (Target, bool) {
	fields := strings.Fields(platform)
	if len(fields) != 2 {
		return Target{}, false
	}
	var target Target
	switch strings.ToLower(fields[0]) {
	case "darwin":
		target.OS = "darwin"
	case "linux":
		target.OS = "linux"
	default:
		return Target{}, false
	}
	switch strings.ToLower(fields[1]) {
	case "arm64", "aarch64":
		target.Arch = "arm64"
	case "x86_64", "amd64":
		target.Arch = "amd64"
	default:
		return Target{}, false
	}
	return target, true
}

// releaseBinary fetches the published build for a platform and returns
// the executable inside it, having checked the archive against the
// release's own checksums.
//
// Checked here rather than there: the machine being prepared is the one
// that cannot yet be trusted to check anything, since the whole reason
// for this is that it has no Stormlight on it.
func releaseBinary(ctx context.Context, version string, target Target) ([]byte, error) {
	number := strings.TrimPrefix(version, "v")
	if number == "" || number == "dev" {
		return nil, fmt.Errorf(
			"this is a development build (%s), so there is no published archive to "+
				"install on a %s host — build one for that platform and copy it there, "+
				"or install a release on it and set bin = in its [hosts.*] entry",
			version, target)
	}
	archive := fmt.Sprintf("stormlight_%s_%s_%s.tar.gz", number, target.OS, target.Arch)
	base := fmt.Sprintf("%s/v%s", releaseHost, number)

	sums, err := fetch(ctx, base+"/checksums.txt")
	if err != nil {
		return nil, fmt.Errorf("read the release checksums: %w", err)
	}
	want, ok := checksumFor(sums, archive)
	if !ok {
		return nil, fmt.Errorf(
			"release v%s publishes nothing for %s", number, target)
	}

	payload, err := fetch(ctx, base+"/"+archive)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", archive, err)
	}
	sum := sha256.Sum256(payload)
	if got := hex.EncodeToString(sum[:]); got != want {
		return nil, fmt.Errorf(
			"%s does not match the checksum the release published "+
				"(got %s, want %s) — refusing to install it", archive, got, want)
	}
	return binaryFromArchive(payload)
}

func fetch(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, response.Status)
	}
	return io.ReadAll(response.Body)
}

// checksumFor reads goreleaser's checksums.txt: one "<sum>  <file>" per
// line.
func checksumFor(sums []byte, archive string) (string, bool) {
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == archive {
			return fields[0], true
		}
	}
	return "", false
}

// binaryFromArchive pulls the executable out of a release tarball, which
// also carries the licence and the readme.
func binaryFromArchive(payload []byte) ([]byte, error) {
	uncompressed, err := gzip.NewReader(strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	defer uncompressed.Close()
	archive := tar.NewReader(uncompressed)
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if header.Typeflag != tar.TypeReg || header.Name != "stormlight" {
			continue
		}
		return io.ReadAll(archive)
	}
	return nil, fmt.Errorf("the release archive holds no stormlight binary")
}

// localBinary is this machine's own executable, for a host that runs the
// same platform.
func localBinary(path string) ([]byte, error) {
	return os.ReadFile(path)
}
