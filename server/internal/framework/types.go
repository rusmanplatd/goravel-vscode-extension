// Package framework provides Goravel framework-level LSP analysis for
// Form Requests, Routes/Swagger, Views, Model-Migration drift, Bootstrap
// registrations, and Schedules.
package framework

import "github.com/rusmanplatd/goravel-lsp/internal/indexer"

// Diag represents a diagnostic in Go byte offsets.
type Diag struct {
	Start, End int
	Severity   int // 1 Error, 2 Warning, 3 Info, 4 Hint
	Message    string
}

// CompItem represents a completion item.
type CompItem struct {
	Label  string
	Detail string
	Doc    string
	Kind   int // 5 Field, 10 Property, 14 Keyword, 17 File
}

// TextEdit represents a byte-offset replacement in the current document (or File when non-empty).
type TextEdit struct {
	File       string // optional target file path for cross-file edits
	Start, End int
	NewText    string
}

// CodeAction represents a quick fix or refactoring action.
type CodeAction struct {
	Title string
	Kind  string
	Edits []TextEdit
}

// InlayHint represents an inline hint at a byte offset.
type InlayHint struct {
	Offset int
	Label  string
}

// HoverResult represents markdown hover text.
type HoverResult struct {
	Markdown string
}

// DefResult represents a jump-to-definition target.
type DefResult struct {
	Loc indexer.Location
}

