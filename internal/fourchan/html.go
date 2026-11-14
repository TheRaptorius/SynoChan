package fourchan

import "strings"

// LocalUpload is an upload as it ends up on disk, used to rewrite links in the saved page.
type LocalUpload struct {
	Tim      string
	Ext      string
	Filename string // The file name it is saved under.
}

// PrepareThreadHTML neutralises a script switch in the downloaded page that breaks local viewing.
func PrepareThreadHTML(page string) string {
	return strings.ReplaceAll(page, `f="to"`, `f="none"`)
}

// FixThreadHTMLLinks rewrites a thread page so it shows the locally saved files: full size links point to the
// saved files, image thumbnails show the saved full size image, video thumbnails point to the saved thumbnail
// (when thumbsSaved, otherwise to the remote one), and protocol relative links get an https prefix.
func FixThreadHTMLLinks(page, board string, uploads []LocalUpload, thumbsSaved bool) string {
	r := strings.ReplaceAll
	hosts := []string{"//i.4cdn.org/" + board + "/", "//is2.4chan.org/" + board + "/"}

	for _, u := range uploads {
		thumb := u.Tim + "s.jpg"
		for _, host := range hosts {
			if IsVideo(u.Ext) {
				if thumbsSaved {
					page = r(page, host+thumb, "thumb/"+thumb)
				}
			} else {
				page = r(page, host+thumb, u.Filename)
			}
			page = r(page, host+u.Tim+u.Ext, u.Filename)
		}
	}

	// Pages use protocol relative links ("//…"), which resolve to file:// when opened locally.
	return r(page, `="//`, `="https://`)
}
