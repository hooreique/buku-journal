package web

import "embed"

// FS contains the buildless web interface.
//go:embed index.html app.js style.css
var FS embed.FS
