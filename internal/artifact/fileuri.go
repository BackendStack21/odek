package artifact

import "net/url"

// FileURI returns the file:// URI for an absolute local path, with the path
// percent-encoded so characters that are meaningful in a URI ('#', '?', '%',
// spaces) stay part of the path. Validate parses ref URIs with url.Parse, so
// every ref odek builds itself must use this instead of "file://" + path.
func FileURI(path string) string {
	return (&url.URL{Scheme: "file", Path: path}).String()
}
