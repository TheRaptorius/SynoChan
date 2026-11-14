package filter

import "testing"

func rules(t *testing.T, skip, only []string) Rules {
	t.Helper()
	r, err := NewRules(skip, only, nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestWholeWordsCaseAndDiacritics(t *testing.T) {
	r := rules(t, []string{"trap", "Café", "žluťoučký kůň"}, nil)
	cases := map[string]bool{
		"Trap thread":                 false,
		"TRAP!!":                      false,
		"trapped in the closet":       true,
		"best cafe in town":           false,
		"CAFÉ owners":                 false,
		"cafeteria":                   true,
		"Zlutoucky kun pel":           false,
		"žluťoučký pes":               true,
		"kůň žluťoučký (wrong order)": true,
	}
	for subject, allowed := range cases {
		if got := r.Check(subject, "", false).Allowed; got != allowed {
			t.Errorf("%q: allowed=%v, want %v", subject, got, allowed)
		}
	}
}

func TestWildcards(t *testing.T) {
	r := rules(t, []string{"trap*", "*mouse", "*cat*"}, nil)
	cases := map[string]bool{
		"trapped":     false,
		"entrapment":  true,
		"dormouse":    false,
		"mousepad":    true,
		"concatenate": false,
		"dog":         true,
	}
	for subject, allowed := range cases {
		if got := r.Check(subject, "", false).Allowed; got != allowed {
			t.Errorf("%q: allowed=%v, want %v", subject, got, allowed)
		}
	}
}

func TestTextStickyAndOnlyList(t *testing.T) {
	r := rules(t, []string{"spoiler"}, []string{"wallpaper", "space*"})
	if v := r.Check("Wallpapers", "", false); v.Allowed {
		t.Error("only-list is whole word: 'wallpapers' must not match 'wallpaper'")
	}
	if v := r.Check("", "post your Wallpaper here", false); !v.Allowed {
		t.Errorf("text should be searched: %+v", v)
	}
	if v := r.Check("Spacecraft", "", false); !v.Allowed {
		t.Errorf("wildcard in only-list: %+v", v)
	}
	if v := r.Check("wallpaper", "no spoiler please", false); v.Allowed || v.Reason != `contains "spoiler"` {
		t.Errorf("skip wins over only: %+v", v)
	}
	if v := r.Check("wallpaper", "", true); v.Allowed || v.Reason != "sticky thread" {
		t.Errorf("sticky: %+v", v)
	}
	if v := r.Check("cooking", "", false); v.Allowed || v.Reason != "matches nothing in the only-list" {
		t.Errorf("only-list miss: %+v", v)
	}
}

func TestBoardRulesCombine(t *testing.T) {
	r, err := NewRules([]string{"global"}, []string{"anime"}, []string{"local"}, []string{"space"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.Check("space global", "", false).Allowed || r.Check("space local", "", false).Allowed {
		t.Error("global and board skip lists must both apply")
	}
	if r.Check("anime", "", false).Allowed || !r.Check("space", "", false).Allowed {
		t.Error("the board's only-list must replace the global one")
	}
	if !r.Check("space", "", true).Allowed {
		t.Error("sticky allowed when skipping stickies is off")
	}
}

func TestCleanListRejectsMatchEverything(t *testing.T) {
	for _, bad := range []string{"*", "**", " * ", "a*b"} {
		if _, err := CleanList([]string{bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	got, err := CleanList([]string{" Trap ", "trap", "", "TRÁP", "two  words"})
	if err != nil || len(got) != 2 || got[0] != "Trap" || got[1] != "two words" {
		t.Fatalf("clean: %q %v", got, err)
	}
}
