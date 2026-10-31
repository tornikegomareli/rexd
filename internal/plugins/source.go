package plugins

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/tornikegomareli/rexd/internal/state"
)

var (
	// owner/repo, the shorthand for a GitHub repository.
	githubShorthand = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9_.-]+$`)
	// git@host:path, the short form of an SSH URL.
	scpStyleURL = regexp.MustCompile(`^[A-Za-z0-9_.-]+@[A-Za-z0-9_.-]+:`)
)

// Source says where a plugin comes from.
type Source struct {
	Kind state.Kind
	// Location is a clone URL, or the absolute path of a local folder.
	Location string
	// Ref is a branch, tag or commit to check out. Only git sources have one.
	Ref string
}

// ParseSource reads what the user typed: owner/repo, owner/repo@ref, a git URL,
// or a local path. A local path must start with /, ./, ../ or ~/, so a name never
// changes meaning because a folder of that name happens to exist.
func ParseSource(arg, ref string, link bool) (Source, error) {
	if isLocalPath(arg) {
		return parseLocal(arg, ref, link)
	}
	if link {
		return Source{}, fmt.Errorf("--link needs a local folder, such as ./%s", arg)
	}
	if strings.Contains(arg, "://") || scpStyleURL.MatchString(arg) {
		return Source{Kind: state.KindGit, Location: arg, Ref: ref}, nil
	}

	repo, shorthandRef, hasRef := strings.Cut(arg, "@")
	if !githubShorthand.MatchString(repo) {
		return Source{}, fmt.Errorf("%q is not owner/repo, a git URL, or a local path (local paths start with ./ or /)", arg)
	}
	if hasRef {
		if shorthandRef == "" {
			return Source{}, fmt.Errorf("%q has no ref after the @", arg)
		}
		if ref != "" {
			return Source{}, errors.New("give the ref once: either owner/repo@ref or --ref")
		}
		ref = shorthandRef
	}
	return Source{Kind: state.KindGit, Location: "https://github.com/" + repo + ".git", Ref: ref}, nil
}

func isLocalPath(arg string) bool {
	return arg == "." || arg == ".." || arg == "~" ||
		strings.HasPrefix(arg, "/") || strings.HasPrefix(arg, "./") ||
		strings.HasPrefix(arg, "../") || strings.HasPrefix(arg, "~/")
}

func parseLocal(arg, ref string, link bool) (Source, error) {
	if ref != "" {
		return Source{}, errors.New("--ref only works with a git source")
	}
	path := arg
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return Source{}, err
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~"))
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return Source{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return Source{}, fmt.Errorf("%s does not exist", arg)
	}
	if !info.IsDir() {
		return Source{}, fmt.Errorf("%s is not a folder", arg)
	}
	kind := state.KindCopy
	if link {
		kind = state.KindLink
	}
	return Source{Kind: kind, Location: path}, nil
}
