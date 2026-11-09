package views

import "github.com/luxig-it/secret-santa/internal/markdown"

func renderMarkdown(src string) string { return string(markdown.Render(src)) }
