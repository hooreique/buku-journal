package web

import "embed"

// FS contains the buildless web interface.
//go:embed index.html app.js style.css manifest.webmanifest icon.svg pwa-192.png pwa-512.png apple-touch-icon.png
var FS embed.FS
