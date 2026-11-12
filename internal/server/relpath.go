package server

import (
	"path/filepath"
	"strings"
)

// RelativeDocumentPath converts an absolute document path to the root-relative,
// slash-separated form used by /api/files and the browser's current-file path.
// It returns false when the path is not inside root. If the lexical prefixes
// differ (for example /tmp vs /private/tmp), both are compared after resolving
// symlinks.
func RelativeDocumentPath(root, path string) (string, bool) {
	if rel, ok := relativeInside(root, path); ok {
		return rel, true
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", false
	}
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		// The file may have been removed; resolve its directory instead.
		dir, evalErr := filepath.EvalSymlinks(filepath.Dir(path))
		if evalErr != nil {
			return "", false
		}
		resolvedPath = filepath.Join(dir, filepath.Base(path))
	}
	return relativeInside(resolvedRoot, resolvedPath)
}

func relativeInside(root, path string) (string, bool) {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}
