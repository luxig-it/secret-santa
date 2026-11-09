// Package markdown renders the small amount of formatting people put into
// wishes and surprise ideas: emphasis, lists, links and line breaks.
//
// Input comes from other participants, so the output is locked down: raw HTML
// is dropped, dangerous URL schemes are removed, images become plain links
// (an embedded image would let its host track every viewer) and every link
// opens in a new tab without a referrer, since the page URL carries a secret.
package markdown

import (
	"bytes"
	"html/template"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

var md = goldmark.New(
	goldmark.WithExtensions(extension.Linkify, extension.Strikethrough),
	goldmark.WithParserOptions(
		parser.WithASTTransformers(util.Prioritized(safeLinks{}, 100)),
	),
	// People press Enter and expect a new line, not a paragraph join.
	goldmark.WithRendererOptions(html.WithHardWraps()),
)

// Render turns source text into sanitised HTML.
func Render(src string) template.HTML {
	var buf bytes.Buffer
	if err := md.Convert([]byte(src), &buf); err != nil {
		// Conversion only fails on a broken writer, which a bytes.Buffer is
		// not; fall back to escaped text rather than nothing.
		return template.HTML("<p>" + template.HTMLEscapeString(src) + "</p>")
	}
	return template.HTML(buf.String())
}

type safeLinks struct{}

func (safeLinks) Transform(doc *ast.Document, reader text.Reader, _ parser.Context) {
	var images []*ast.Image
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.Link:
			markExternal(n)
		case *ast.AutoLink:
			markExternal(n)
		case *ast.Image:
			images = append(images, n)
		}
		return ast.WalkContinue, nil
	})

	// Replace after walking so the tree is not modified mid-walk.
	for _, img := range images {
		parent := img.Parent()
		if insideLink(img) {
			// A link inside a link is invalid HTML and browsers would split
			// it, so an image that already is a link's label becomes text.
			for c := img.FirstChild(); c != nil; {
				next := c.NextSibling()
				parent.InsertBefore(parent, img, c)
				c = next
			}
			parent.RemoveChild(parent, img)
			continue
		}
		link := ast.NewLink()
		link.Destination = img.Destination
		link.Title = img.Title
		for c := img.FirstChild(); c != nil; {
			next := c.NextSibling()
			link.AppendChild(link, c)
			c = next
		}
		if !link.HasChildren() {
			link.AppendChild(link, ast.NewString(img.Destination))
		}
		markExternal(link)
		parent.ReplaceChild(parent, img, link)
	}
}

func insideLink(n ast.Node) bool {
	for p := n.Parent(); p != nil; p = p.Parent() {
		if _, ok := p.(*ast.Link); ok {
			return true
		}
	}
	return false
}

func markExternal(n ast.Node) {
	n.SetAttributeString("target", []byte("_blank"))
	n.SetAttributeString("rel", []byte("noopener noreferrer nofollow"))
}
