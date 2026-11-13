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
4. If the command prints a URL, give it to the user. If it reports sending the path to an existing instance, give the printed URL and say it opened in the existing viewer (a running viewer adds the folder or file as a workspace).

If the executable is missing, tell the user to install it with one of these commands:

```bash
brew install bzon/tap/spec-viewer
# or
go install github.com/bzon/spec-viewer/cmd/spec-viewer@latest
```

## Review comments

The user may leave comments on a document in the viewer. Read them from the running instance (use the URL printed on startup):

```bash
curl -s "http://127.0.0.1:<port>/api/annotations?path=<relative-path.md>&ws=<workspace-id>"
```

The response is a JSON array. Each comment has `id`, `quote` (the highlighted text), `note`, `author`, optional `scope: "block"` with `block_count` (the comment refers to that many consecutive blocks starting at the quoted text, not just the quoted text), and a `replies` array. The `X-Document-Path` response header holds the percent-encoded absolute file path.

Reply to a comment, with the `id` from the list above:

```bash
curl -s -X POST "http://127.0.0.1:<port>/api/annotations/replies?path=<relative-path.md>&ws=<workspace-id>" \
  -H "Content-Type: application/json" \
  --data-binary @- <<'EOF'
{"annotation_id":"<id>","author":"agent","body":"Done: updated section 2."}
EOF
```

After reading a user's comments, acknowledge them so the user sees they were received. Comments carry `sent_at`, `read_at` and `read_by` once marked:

```bash
curl -s -X POST "http://127.0.0.1:<port>/api/annotations/status?path=<relative-path.md>&ws=<workspace-id>" \
  -H "Content-Type: application/json" \
  --data-binary @- <<'EOF'
{"status":"read","by":"agent","annotation_ids":["<id>","<id>"]}
EOF
```

One viewer process serves many folders (workspaces). `ws` selects the workspace; copy it from the prompt or the page URL (`/w/<id>/`). Omitting `ws` means the viewer's default workspace, the folder it was first started on. `path` is relative to the selected workspace's root; a workspace opened on a single file (its `target` in `GET /api/workspaces`) answers 403 for any other path. Replies appear in the browser live. Do not use `PUT /api/annotations`; it replaces the user's whole list.
