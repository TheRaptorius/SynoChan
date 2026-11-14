package fourchan

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func strp(s string) *string { return &s }

func TestParseThreadURL(t *testing.T) {
	cases := []struct {
		in      string
		ok      bool
		board   string
		id      int64
		subject string
	}{
		{"https://boards.4chan.org/gif/thread/16245377", true, "gif", 16245377, ""},
		{"https://boards.4chan.org/gif/thread/16245377#p16245377", true, "gif", 16245377, ""},
		{"  boards.4channel.org/g/thread/123/some-slug  ", true, "g", 123, ""},
		{"http://boards.4chan.org/r9k/thread/77?subject=My_Nice_Thread", true, "r9k", 77, "My Nice Thread"},
		{"https://boards.4chan.org/wsg/thread/5?a=1&s=Bad%2FName", true, "wsg", 5, "BadName"},
		{"https://boards.4chan.org/gif/", false, "", 0, ""},
		{"https://example.com/g/thread/1", false, "", 0, ""},
		{"", false, "", 0, ""},
	}
	for _, c := range cases {
		ref, ok := ParseThreadURL(c.in)
		if ok != c.ok || ref.Board != c.board || ref.ID != c.id || ref.Subject != c.subject {
			t.Errorf("ParseThreadURL(%q) = %+v, %v; want board=%q id=%d subject=%q ok=%v", c.in, ref, ok, c.board, c.id, c.subject, c.ok)
		}
	}
}

func TestParseBoardURL(t *testing.T) {
	cases := map[string]string{
		"https://boards.4chan.org/wg/":          "wg",
		"https://boards.4chan.org/wg":           "wg",
		"boards.4channel.org/g//":               "g",
		"https://boards.4chan.org/gif/catalog":  "gif",
		"https://boards.4chan.org/gif/catalog/": "gif",
		"https://boards.4chan.org/gif/?x=1":     "gif",
		"https://boards.4chan.org/gif/thread/1": "",
		"https://boards.4chan.org/":             "",
		"https://example.org/g/":                "",
	}
	for in, want := range cases {
		got, ok := ParseBoardURL(in)
		if got != want || ok != (want != "") {
			t.Errorf("ParseBoardURL(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}

func TestSplitURLs(t *testing.T) {
	got := SplitURLs(" a,b  c\nd\r\n,,e#p1 ")
	want := []string{"a", "b", "c", "d", "e"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("SplitURLs = %q, want %q", got, want)
	}
}

func TestExtractSubject(t *testing.T) {
	cases := []struct {
		name string
		post Post
		want string
	}{
		{"subject wins", Post{Sub: strp("Cats &amp; dogs"), Name: strp("Bob"), Com: strp("hi")}, "Cats & dogs"},
		{"named poster", Post{Name: strp("Bob"), Com: strp("hi")}, "Bob"},
		{"anonymous uses short comment", Post{Name: strp("Anonymous"), Com: strp("short &quot;one&quot;")}, `short one`},
		{"no name uses comment", Post{Com: strp("short")}, "short"},
		{"multi line comment joined", Post{Name: strp("Anonymous"), Com: strp("a<br>b")}, "a b"},
		{"long comment cut at a word", Post{Name: strp("Anonymous"), Com: strp("Post your favourite mechanical keyboards and the switches you use daily")}, "Post your favourite mechanical keyboards"},
		{"reply links and urls removed", Post{Com: strp(`<a href="#p1" class="quotelink">&gt;&gt;123</a><br>Look at https://example.com/x this`)}, "Look at this"},
		{"greentext arrows dropped", Post{Com: strp(`<span class="quote">&gt;be me</span><br>&gt;mfw`)}, "be me mfw"},
		{"empty subject falls back", Post{Sub: strp("  "), Com: strp("from the text")}, "from the text"},
		{"illegal characters removed", Post{Sub: strp(`a/b\c:d*e?f"g<h>i|j`)}, "abcdefghij"},
		{"trailing dots trimmed", Post{Sub: strp("Ends with dots...")}, "Ends with dots"},
	}
	for _, c := range cases {
		if got := ExtractSubject(&c.post); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestUploadsAndThumbnails(t *testing.T) {
	body := `{"posts":[
		{"no":100,"sub":"OP","tim":1700000000001,"filename":"cat pic","ext":".jpg"},
		{"no":101,"com":"no file"},
		{"no":102,"tim":1700000000002,"filename":"clip","ext":".webm"},
		{"no":103,"tim":1700000000003,"filename":"bad:name?","ext":".png"}
	]}`
	var tr ThreadResponse
	if err := json.Unmarshal([]byte(body), &tr); err != nil {
		t.Fatal(err)
	}
	ups := Uploads(DefaultEndpoints, "g", tr.Posts)
	if len(ups) != 3 {
		t.Fatalf("got %d uploads", len(ups))
	}
	if ups[0].URL != "https://i.4cdn.org/g/1700000000001.jpg" || ups[0].Tim != "1700000000001" || ups[0].No != 100 || ups[0].UploadedFilename != "cat pic" {
		t.Errorf("upload 0 = %+v", ups[0])
	}
	if ups[2].UploadedFilename != "badname" {
		t.Errorf("unsanitised filename %q", ups[2].UploadedFilename)
	}
	thumbs := Thumbnails(DefaultEndpoints, "g", tr.Posts)
	if len(thumbs) != 1 || thumbs[0].URL != "https://t.4cdn.org/g/1700000000002s.jpg" || thumbs[0].No != 102 {
		t.Errorf("thumbs = %+v", thumbs)
	}
}

func TestUploadsFlashBoardUsesFilename(t *testing.T) {
	body := `{"posts":[{"no":1,"tim":1700000000001,"filename":"My Game","ext":".swf"}]}`
	var tr ThreadResponse
	if err := json.Unmarshal([]byte(body), &tr); err != nil {
		t.Fatal(err)
	}
	ups := Uploads(DefaultEndpoints, "f", tr.Posts)
	if len(ups) != 1 || ups[0].URL != "https://i.4cdn.org/f/My%20Game.swf" || ups[0].Tim != "My Game" {
		t.Fatalf("got %+v", ups)
	}
}

func TestClampName(t *testing.T) {
	long := strings.Repeat("ž", 200) + ".jpg" // 2 bytes per rune.
	got := ClampName(long)
	if len(got) > MaxNameBytes || !strings.HasSuffix(got, ".jpg") || !utf8.ValidString(got) {
		t.Fatalf("ClampName gave %d bytes %q", len(got), got)
	}
	if ClampName("short.png") != "short.png" {
		t.Fatal("short names must be unchanged")
	}
}

func TestFixThreadHTMLLinks(t *testing.T) {
	page := `<link href="//s.4cdn.org/css/x.css"><a href="//i.4cdn.org/g/1700000000001.jpg"><img src="//i.4cdn.org/g/1700000000001s.jpg"></a>` +
		`<a href="//i.4cdn.org/g/1700000000002.webm"><img src="//i.4cdn.org/g/1700000000002s.jpg"></a>`
	got := FixThreadHTMLLinks(page, "g", []LocalUpload{
		{Tim: "1700000000001", Ext: ".jpg", Filename: "100.jpg"},
		{Tim: "1700000000002", Ext: ".webm", Filename: "102.webm"},
	}, true)
	for _, want := range []string{`href="https://s.4cdn.org/css/x.css"`, `href="100.jpg"`, `src="100.jpg"`, `href="102.webm"`, `src="thumb/1700000000002s.jpg"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
	if strings.Contains(got, `="//`) {
		t.Errorf("protocol relative link left in %s", got)
	}
}
