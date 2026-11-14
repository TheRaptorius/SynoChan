// Package fourchan knows the URLs, JSON API and HTML of 4chan.
package fourchan

import (
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// SiteName is used in save paths and keys.
const SiteName = "4chan"

// Endpoints are the hosts used for requests. Tests point them at a local server.
type Endpoints struct {
	API    string // JSON API, e.g. https://a.4cdn.org
	Images string // Full size uploads, e.g. https://i.4cdn.org
	Thumbs string // Thumbnails, e.g. https://t.4cdn.org
	Boards string // HTML pages, e.g. https://boards.4chan.org
}

// DefaultEndpoints are the production hosts.
var DefaultEndpoints = Endpoints{
	API:    "https://a.4cdn.org",
	Images: "https://i.4cdn.org",
	Thumbs: "https://t.4cdn.org",
	Boards: "https://boards.4chan.org",
}

func (e Endpoints) ThreadJSONURL(board string, id int64) string {
	return fmt.Sprintf("%s/%s/thread/%d.json", e.API, board, id)
}

func (e Endpoints) CatalogURL(board string) string {
	return fmt.Sprintf("%s/%s/catalog.json", e.API, board)
}

func (e Endpoints) ThreadHTMLURL(board string, id int64) string {
	return fmt.Sprintf("%s/%s/thread/%d", e.Boards, board, id)
}

// ThreadURL is the public URL of a thread.
func ThreadURL(board string, id int64) string {
	return fmt.Sprintf("https://boards.4chan.org/%s/thread/%d", board, id)
}

// BoardURL is the public URL of a board's catalog.
func BoardURL(board string) string {
	return fmt.Sprintf("https://boards.4chan.org/%s/catalog", board)
}

var (
	threadRe = regexp.MustCompile(`boards\.(?:4chan|4channel)\.org/([a-zA-Z0-9]+)/thread/(\d+)`)
	boardRe  = regexp.MustCompile(`boards\.(?:4chan|4channel)\.org/([a-zA-Z0-9]+)/*(?:catalog/*)?$`)
)

// ThreadRef identifies a thread parsed from a URL.
type ThreadRef struct {
	Board   string
	ID      int64
	Subject string // Optional subject given in the URL query (".../thread/123?subject=My_Subject").
}

// ParseThreadURL recognises thread URLs. Reply anchors and slugs are ignored.
func ParseThreadURL(raw string) (ThreadRef, bool) {
	raw = PrepareURL(raw)
	m := threadRe.FindStringSubmatch(raw)
	if m == nil {
		return ThreadRef{}, false
	}
	id, err := strconv.ParseInt(m[2], 10, 64)
	if err != nil || id <= 0 {
		return ThreadRef{}, false
	}
	ref := ThreadRef{Board: m[1], ID: id}
	// A custom subject can be passed as the value of the last query parameter, underscores meaning spaces.
	if i := strings.Index(raw, "?"); i >= 0 {
		if j := strings.LastIndex(raw, "="); j > i {
			value := raw[j+1:]
			if unescaped, err := url.QueryUnescape(value); err == nil {
				value = unescaped
			}
			ref.Subject = SanitiseSubject(strings.ReplaceAll(value, "_", " "))
		}
	}
	return ref, true
}

// ParseBoardURL recognises board (and board catalog) URLs and returns the board code.
func ParseBoardURL(raw string) (string, bool) {
	raw = PrepareURL(raw)
	if i := strings.Index(raw, "?"); i >= 0 {
		raw = raw[:i]
	}
	m := boardRe.FindStringSubmatch(raw)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// PrepareURL trims whitespace and drops the reply anchor, e.g. "…/thread/16245377#p16245377" becomes "…/thread/16245377".
func PrepareURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if i := strings.IndexByte(raw, '#'); i > 0 {
		raw = raw[:i]
	}
	return raw
}

// SplitURLs splits user input into URLs. Commas, whitespace and new lines all separate entries.
func SplitURLs(text string) []string {
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n' || r == '\r' || r == '\t'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = PrepareURL(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// Post is one post of a thread, or one thread of a catalog page. Only the used fields are decoded.
type Post struct {
	No       int64           `json:"no"`
	Sub      *string         `json:"sub"`
	Name     *string         `json:"name"`
	Com      *string         `json:"com"`
	Tim      json.RawMessage `json:"tim"`
	Filename *string         `json:"filename"`
	Ext      *string         `json:"ext"`
	Images   int             `json:"images"`
}

// HasFile reports whether the post carries an upload.
func (p *Post) HasFile() bool {
	return p.Ext != nil && *p.Ext != ""
}

// TimString returns the server side file name stem (upload timestamp).
func (p *Post) TimString() string {
	s := strings.Trim(string(p.Tim), `"`)
	return s
}

// ThreadResponse is the body of /{board}/thread/{id}.json.
type ThreadResponse struct {
	Posts []Post `json:"posts"`
}

// CatalogPage is one page of /{board}/catalog.json.
type CatalogPage struct {
	Page    int    `json:"page"`
	Threads []Post `json:"threads"`
}

// UploadInfo describes one upload found in a thread.
type UploadInfo struct {
	Tim              string // Server side identifier; for /f/ this is the uploaded file name.
	URL              string
	UploadedFilename string // Sanitised, without extension.
	Ext              string // With leading period.
	No               int64  // Post number.
}

// ThumbInfo describes one thumbnail worth saving.
type ThumbInfo struct {
	No  int64
	URL string
}

// Uploads lists the uploads of a thread.
func Uploads(e Endpoints, board string, posts []Post) []UploadInfo {
	var out []UploadInfo
	for i := range posts {
		p := &posts[i]
		if !p.HasFile() {
			continue
		}
		original := ""
		if p.Filename != nil {
			original = *p.Filename
		}
		// The /f/ (flash) board stores files under their uploaded name.
		stem := p.TimString()
		if board == "f" {
			stem = original
		}
		if stem == "" {
			continue
		}
		out = append(out, UploadInfo{
			Tim:              stem,
			URL:              fmt.Sprintf("%s/%s/%s%s", e.Images, board, url.PathEscape(stem), *p.Ext),
			UploadedFilename: SanitiseFilename(original),
			Ext:              *p.Ext,
			No:               p.No,
		})
	}
	return out
}

// Thumbnails lists thumbnails that are needed for a locally saved page.
// Only video uploads need one; for images the page links to the full size file instead.
func Thumbnails(e Endpoints, board string, posts []Post) []ThumbInfo {
	var out []ThumbInfo
	for i := range posts {
		p := &posts[i]
		if !p.HasFile() || *p.Ext != ".webm" {
			continue
		}
		out = append(out, ThumbInfo{No: p.No, URL: fmt.Sprintf("%s/%s/%ss.jpg", e.Thumbs, board, p.TimString())})
	}
	return out
}

// SubjectFromTextLength is the longest subject taken from the start of a post's text.
const SubjectFromTextLength = 40

// ExtractSubject picks a subject from an opening post: the subject field, else a non default name
// (people often type the subject into the name field), else the start of the post's text.
// Returns "" when nothing suitable is present.
func ExtractSubject(p *Post) string {
	if p.Sub != nil {
		if s := SanitiseSubject(html.UnescapeString(*p.Sub)); s != "" {
			return s
		}
	}
	if p.Name != nil && *p.Name != "Anonymous" {
		if s := SanitiseSubject(html.UnescapeString(*p.Name)); s != "" {
			return s
		}
	}
	if p.Com != nil {
		return SanitiseSubject(SubjectFromText(*p.Com, SubjectFromTextLength))
	}
	return ""
}

var (
	quoteLinkRe = regexp.MustCompile(`(?is)<a[^>]*class="[^"]*quotelink[^"]*"[^>]*>.*?</a>|<span[^>]*class="[^"]*deadlink[^"]*"[^>]*>.*?</span>`)
	lineBreakRe = regexp.MustCompile(`(?i)<br\s*/?>|<wbr\s*/?>`)
	tagRe       = regexp.MustCompile(`<[^>]*>`)
	urlRe       = regexp.MustCompile(`(?i)\bhttps?://\S+`)
)

// SubjectFromText turns a post's HTML into a short subject: reply links, tags and URLs are removed,
// greentext arrows dropped, whitespace collapsed, and the text cut at a word boundary to at most max runes.
func SubjectFromText(com string, max int) string {
	com = quoteLinkRe.ReplaceAllString(com, " ")
	com = lineBreakRe.ReplaceAllString(com, " ")
	com = tagRe.ReplaceAllString(com, "")
	com = html.UnescapeString(com)
	com = urlRe.ReplaceAllString(com, " ")
	words := strings.Fields(com)
	for i, w := range words {
		words[i] = strings.TrimLeft(w, ">")
	}
	text := strings.Join(strings.Fields(strings.Join(words, " ")), " ")
	if utf8.RuneCountInString(text) <= max {
		return text
	}
	runes := []rune(text)
	cut := runes[:max]
	if runes[max] == ' ' {
		return string(cut) // The cut falls exactly between two words.
	}
	if i := strings.LastIndexByte(string(cut), ' '); i > max/2 {
		return strings.TrimSpace(string(cut)[:i])
	}
	return strings.TrimSpace(string(cut))
}

// illegalNameChars are the characters Windows (and therefore SMB clients) reject in file names.
const illegalNameChars = `<>:"/\|?*`

func removeIllegal(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 || r == 127 || strings.ContainsRune(illegalNameChars, r) {
			return -1
		}
		return r
	}, s)
}

// SanitiseSubject makes a subject usable as part of a folder name.
func SanitiseSubject(s string) string {
	return strings.TrimRight(strings.TrimSpace(removeIllegal(s)), ". ")
}

// SanitiseFilename makes an uploaded file name usable on disk.
func SanitiseFilename(s string) string {
	return strings.TrimRight(strings.TrimSpace(removeIllegal(s)), ". ")
}

// MaxNameBytes is the longest file or folder name written. Encrypted shared folders on DSM
// (eCryptfs) only allow 143 bytes, plain btrfs/ext4 allow 255.
const MaxNameBytes = 143

// ClampName shortens a file name to MaxNameBytes, keeping the extension and valid UTF-8.
func ClampName(name string) string {
	if len(name) <= MaxNameBytes {
		return name
	}
	ext := path.Ext(name)
	if len(ext) > 16 {
		ext = ""
	}
	stem := name[:len(name)-len(ext)]
	limit := MaxNameBytes - len(ext)
	for len(stem) > limit || !utf8.ValidString(stem) {
		stem = stem[:len(stem)-1]
	}
	return stem + ext
}
