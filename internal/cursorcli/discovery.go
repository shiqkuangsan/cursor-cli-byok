package cursorcli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const installCommand = "curl https://cursor.com/install -fsS | bash"

var officialVersionPattern = regexp.MustCompile(`^(\d{4})\.(\d{2})\.(\d{2})-([0-9A-Za-z][0-9A-Za-z.-]*)$`)

var verifiedVersions = map[string]struct{}{
	"2026.07.08-0c04a8a": {},
	"2026.07.09-a3815c0": {},
}

type Version struct {
	Raw      string
	Year     int
	Month    int
	Day      int
	Revision string
}

func (v Version) String() string {
	return v.Raw
}

func IsVerifiedVersion(version Version) bool {
	_, verified := verifiedVersions[version.Raw]
	return verified
}

func Find(getenv func(string) string) (string, error) {
	if getenv == nil {
		return "", errors.New("find cursor-agent: environment lookup is required")
	}
	for _, directory := range filepath.SplitList(getenv("PATH")) {
		if directory == "" || !filepath.IsAbs(directory) {
			continue
		}
		candidate := filepath.Join(directory, "cursor-agent")
		if isExecutableFile(candidate) {
			return candidate, nil
		}
	}

	home := getenv("HOME")
	if home != "" {
		if !filepath.IsAbs(home) {
			return "", errors.New("find cursor-agent: HOME must be absolute")
		}
		candidate := filepath.Join(home, ".local", "bin", "cursor-agent")
		if isExecutableFile(candidate) {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("find cursor-agent: executable not found; install the official Cursor CLI with: %s", installCommand)
}

func ReadVersion(ctx context.Context, path string) (Version, error) {
	if ctx == nil {
		return Version{}, errors.New("read cursor-agent version: context is required")
	}
	if !filepath.IsAbs(path) || !isExecutableFile(path) {
		return Version{}, errors.New("read cursor-agent version: path must be an absolute executable file")
	}
	output, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return Version{}, fmt.Errorf("read cursor-agent version: execute %q: %w", path, err)
	}
	return ParseVersion(strings.TrimSpace(string(output)))
}

func ParseVersion(raw string) (Version, error) {
	matches := officialVersionPattern.FindStringSubmatch(raw)
	if matches == nil {
		return Version{}, errors.New("parse cursor-agent version: unsupported version format")
	}
	year, _ := strconv.Atoi(matches[1])
	month, _ := strconv.Atoi(matches[2])
	day, _ := strconv.Atoi(matches[3])
	date := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	if date.Year() != year || int(date.Month()) != month || date.Day() != day {
		return Version{}, errors.New("parse cursor-agent version: invalid release date")
	}
	return Version{
		Raw:      raw,
		Year:     year,
		Month:    month,
		Day:      day,
		Revision: matches[4],
	}, nil
}

func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	return info.Mode().Perm()&0o111 != 0
}
