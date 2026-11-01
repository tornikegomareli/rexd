package plugins

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	rangeRef = regexp.MustCompile(`^[\^~]\d+\.\d+\.\d+$`)
	// A prerelease such as 1.2.0-rc1 does not match, so a range never installs one.
	versionTag = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)$`)
)

// isRange says if a ref is a version range: ^1.2.3 or ~1.2.3.
func isRange(ref string) bool {
	return rangeRef.MatchString(ref)
}

func parseVersion(text string) (version [3]int, ok bool) {
	match := versionTag.FindStringSubmatch(text)
	if match == nil {
		return version, false
	}
	for i := range version {
		number, err := strconv.Atoi(match[i+1])
		if err != nil {
			return version, false
		}
		version[i] = number
	}
	return version, true
}

// inRange follows npm: ^1.2.3 keeps the major version, and below 1.0 the minor
// version too, and ~1.2.3 keeps the minor version.
func inRange(v, low [3]int, kind byte) bool {
	for i := range v {
		if v[i] != low[i] {
			if v[i] < low[i] {
				return false
			}
			break
		}
	}
	if kind == '~' || low[0] == 0 {
		return v[0] == low[0] && v[1] == low[1]
	}
	return v[0] == low[0]
}

// bestTag returns the highest tag in the range.
func bestTag(tags []string, ref string) (string, error) {
	low, _ := parseVersion(ref[1:])
	best, found := "", [3]int{}
	for _, tag := range tags {
		v, ok := parseVersion(tag)
		if !ok || !inRange(v, low, ref[0]) {
			continue
		}
		if best == "" || compareVersions(v, found) > 0 {
			best, found = tag, v
		}
	}
	if best == "" {
		return "", fmt.Errorf("no tag matches %s", ref)
	}
	return best, nil
}

func compareVersions(a, b [3]int) int {
	for i := range a {
		if a[i] != b[i] {
			return a[i] - b[i]
		}
	}
	return 0
}

// resolveRange asks the repository for its tags, and returns the best one.
func resolveRange(ctx context.Context, location, ref string) (string, error) {
	out, err := git(ctx, "", "ls-remote", "--tags", "--refs", location)
	if err != nil {
		return "", err
	}
	var tags []string
	for _, line := range strings.Split(out, "\n") {
		if _, tag, found := strings.Cut(line, "refs/tags/"); found {
			tags = append(tags, tag)
		}
	}
	return bestTag(tags, ref)
}
