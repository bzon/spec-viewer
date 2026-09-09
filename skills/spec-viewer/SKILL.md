---
name: spec-viewer
description: Open local Markdown files or directories in the spec-viewer browser app. Use when the user asks to view, preview, or review Markdown documentation, specifications, or plans in spec-viewer.
license: MIT
compatibility: Requires shell access and the spec-viewer executable on PATH.
---

# spec-viewer

Open the requested Markdown file or directory with `spec-viewer`.

1. Check that `spec-viewer` is available on `PATH`.
2. Run `spec-viewer "<path>"` through the shell. Preserve the user's path and quote it.
3. Do not pass `--no-open` unless the user asks to suppress the browser.
4. If the command prints a URL, give it to the user. If it reports an existing instance, say the file opened in the existing browser tab.

If the executable is missing, tell the user to install it with one of these commands:

```bash
brew install bzon/tap/spec-viewer
# or
go install github.com/bzon/spec-viewer/cmd/spec-viewer@latest
```
