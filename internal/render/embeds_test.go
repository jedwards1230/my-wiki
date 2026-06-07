package render

import (
	"strings"
	"testing"
)

func TestYouTubeEmbed(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string // all substrings must be present
	}{
		{
			"watch url",
			"![](https://www.youtube.com/watch?v=dQw4w9WgXcQ)",
			[]string{`class="embed embed-video embed-youtube"`, `<iframe`, `https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ`, `allowfullscreen`},
		},
		{
			"short url",
			"![](https://youtu.be/dQw4w9WgXcQ)",
			[]string{`youtube-nocookie.com/embed/dQw4w9WgXcQ`},
		},
		{
			"shorts url",
			"![](https://www.youtube.com/shorts/dQw4w9WgXcQ)",
			[]string{`embed/dQw4w9WgXcQ`},
		},
		{
			"timestamp seconds",
			"![](https://youtu.be/dQw4w9WgXcQ?t=90)",
			[]string{`embed/dQw4w9WgXcQ?start=90`},
		},
		{
			"timestamp hms",
			"![](https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=1m30s)",
			[]string{`embed/dQw4w9WgXcQ?start=90`},
		},
		{
			"alt becomes title",
			"![Rick Astley](https://youtu.be/dQw4w9WgXcQ)",
			[]string{`title="Rick Astley"`},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := renderMD(t, c.in, nil)
			for _, w := range c.want {
				if !strings.Contains(out, w) {
					t.Errorf("want %q in output:\n%s", w, out)
				}
			}
			// A successful embed must NOT leave a raw <img> behind.
			if strings.Contains(out, "<img") {
				t.Errorf("youtube embed left an <img> tag:\n%s", out)
			}
		})
	}
}

func TestVimeoEmbed(t *testing.T) {
	out := renderMD(t, "![](https://vimeo.com/123456789)", nil)
	if !strings.Contains(out, "https://player.vimeo.com/video/123456789") {
		t.Errorf("vimeo embed missing src:\n%s", out)
	}
	if !strings.Contains(out, "embed-vimeo") {
		t.Errorf("vimeo embed missing kind class:\n%s", out)
	}
}

func TestNonProviderImageStaysImage(t *testing.T) {
	// A plain external image must remain an <img>, not become an embed.
	out := renderMD(t, "![cat](https://example.com/cat.png)", nil)
	if !strings.Contains(out, "<img") {
		t.Errorf("plain image should stay <img>:\n%s", out)
	}
	if strings.Contains(out, "embed-video") {
		t.Errorf("plain image wrongly embedded:\n%s", out)
	}
}

func TestMediaFileEmbeds(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"video", "![[clips/demo.mp4]]", []string{`<video`, `controls`, `src="/raw/clips/demo.mp4"`, `embed-video`}},
		{"webm video", "![[demo.webm]]", []string{`<video`, `src="/raw/demo.webm"`}},
		{"audio", "![[song.mp3]]", []string{`<audio`, `controls`, `src="/raw/song.mp3"`, `embed-audio`}},
		{"pdf", "![[manual.pdf]]", []string{`<iframe`, `src="/raw/manual.pdf"`, `embed-pdf`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := renderMD(t, c.in, nil)
			for _, w := range c.want {
				if !strings.Contains(out, w) {
					t.Errorf("want %q in output:\n%s", w, out)
				}
			}
		})
	}
}

func TestImageEmbedStillWorks(t *testing.T) {
	// Regression: ![[x.png]] must still render an <img>, not a media embed.
	out := renderMD(t, "![[photo.png]]", nil)
	if !strings.Contains(out, `<img src="/raw/photo.png"`) {
		t.Errorf("image embed regressed:\n%s", out)
	}
	if strings.Contains(out, "<video") || strings.Contains(out, "embed-video") {
		t.Errorf("image wrongly treated as media:\n%s", out)
	}
}

func TestParseYouTubeStart(t *testing.T) {
	cases := map[string]string{
		"90":     "90",
		"1m30s":  "90",
		"1h":     "3600",
		"1h1m1s": "3661",
		"":       "",
		"0":      "0",
	}
	for in, want := range cases {
		if got := parseYouTubeStart(in); got != want {
			t.Errorf("parseYouTubeStart(%q) = %q, want %q", in, got, want)
		}
	}
}
