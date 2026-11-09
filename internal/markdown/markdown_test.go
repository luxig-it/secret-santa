package markdown

import (
	"strings"
	"testing"
)

func TestRender(t *testing.T) {
	cases := []struct {
		name, in   string
		want, deny []string
	}{
		{"emphasis", "**fett** und *kursiv*", []string{"<strong>fett</strong>", "<em>kursiv</em>"}, nil},
		{"list", "- eins\n- zwei", []string{"<ul>", "<li>eins</li>"}, nil},
		{"hard wraps", "Zeile 1\nZeile 2", []string{"Zeile 1<br>"}, nil},
		{"link", "[Shop](https://example.com/x)", []string{`href="https://example.com/x"`, `target="_blank"`, `rel="noopener noreferrer nofollow"`}, nil},
		{"autolink", "siehe https://example.com", []string{`<a href="https://example.com" target="_blank"`}, nil},
		{"raw html", "<script>alert(1)</script><b>x</b>", nil, []string{"<script", "<b>"}},
		{"javascript url", "[x](javascript:alert(1))", nil, []string{"javascript:"}},
		{"image becomes link", "![Bild](https://tracker.example/p.gif)", []string{`<a href="https://tracker.example/p.gif"`, ">Bild</a>"}, []string{"<img"}},
		{"bare image", "![](https://tracker.example/p.gif)", []string{">https://tracker.example/p.gif</a>"}, []string{"<img"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := string(Render(c.in))
			for _, w := range c.want {
				if !strings.Contains(got, w) {
					t.Errorf("output %q lacks %q", got, w)
				}
			}
			for _, d := range c.deny {
				if strings.Contains(got, d) {
					t.Errorf("output %q contains %q", got, d)
				}
			}
		})
	}
}
