---
description: Open a markdown file or directory in spec-viewer (local browser viewer with syntax highlighting, TOC, and search)
argument: file or directory path to view (required)
---

Run `spec-viewer $ARGUMENTS` using the Bash tool.

- If the command outputs a URL, tell the user: "Viewing at [URL]"
- If it says "sent to existing instance", tell the user it opened in the existing browser tab
- If spec-viewer is not installed, tell the user to run: `go install github.com/bzon/spec-viewer/cmd/spec-viewer@latest`

Do NOT use --no-open — let it open the browser automatically.
