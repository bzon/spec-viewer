const FEEDBACK_EXCERPT_LIMIT = 120;

// Builds the plain-text message for "Copy feedback": the user's comments,
// as their reply to the agent, plus a command that marks them read. Nothing is
// inferred. Document text and comment bodies are single-line quotations or
// indented lines; the only JSON in the closing command is the status body, which
// reaches the shell on stdin through a quoted heredoc.
export function buildFeedback({ fileName, relativePath, origin, workspaceId, comments }) {
  const safeOrigin = String(origin).replace(/'/g, '%27');
  const statusUrl = `${safeOrigin}/api/annotations/status?${documentQuery(relativePath, workspaceId)}`;
  const count = comments.length;

  const lines = [`Feedback on ${clean(fileName || relativePath)} (${count === 1 ? '1 comment' : `${count} comments`}):`, ''];
  comments.forEach((comment, index) => {
    const indent = ' '.repeat(String(index + 1).length + 2);
    lines.push(`${index + 1}. ${feedbackTarget(comment)}`);
    const note = plainLines(comment.note);
    if (note.length > 0) lines.push(...note.map((line) => (line ? indent + line : line)));
    const replies = comment.replies;
    replies.forEach((reply, replyIndex) => {
      if (!reply.author) {
        const followUp = plainLines(reply.body);
        followUp.forEach((line, lineIndex) => lines.push(`${indent}${lineIndex === 0 ? 'Follow-up: ' : ''}${line}`));
      } else if (replyIndex === replies.length - 1) {
        lines.push(`${indent}(Last reply from ${clean(reply.author)}: "${excerpt(reply.body)}")`);
      }
    });
    lines.push('');
  });

  const ids = JSON.stringify(comments.map((comment) => comment.id)).replace(/[\u2028\u2029]/g, (c) => `\\u${c.charCodeAt(0).toString(16)}`);
  lines.push(
    'When you have read this, mark the comments as read (the viewer shows it to me):',
    `curl -sS -X POST '${statusUrl}' -H 'Content-Type: application/json' --data-binary @- <<'EOF'`,
    `{"status":"read","by":"<your agent name>","annotation_ids":${ids}}`,
    'EOF',
  );
  return lines.join('\n');
}

// "path=...&ws=..." for a single-quoted shell URL: apostrophes become %27. The ws
// parameter makes the agent's call land in the folder the document belongs to.
function documentQuery(relativePath, workspaceId) {
  const encode = (value) => encodeURIComponent(value).replace(/'/g, '%27');
  return `path=${encode(relativePath)}${workspaceId ? `&ws=${encode(workspaceId)}` : ''}`;
}

function feedbackTarget(comment) {
  const detached = comment.detached ? ' (text no longer in the document)' : '';
  if (comment.scope !== 'block') return `On "${excerpt(comment.quote)}"${detached}`;
  const kind = clean(comment.blockKind).replace(/\s+/g, ' ').trim();
  const more = comment.blockCount > 1 ? ` (${comment.blockCount} blocks)` : '';
  if (!kind) return `On the block starting "${excerpt(comment.quote)}"${more}${detached}`;
  const label = kind.charAt(0).toLowerCase() + kind.slice(1);
  const heading = comment.heading ? ` under "${excerpt(comment.heading)}"` : '';
  return `On the ${label}${heading}${more}${detached}`;
}

// One line of at most about 120 characters for use between double quotes.
function excerpt(text) {
  const flat = clean(text).replace(/\s+/g, ' ').replace(/"/g, '\u201D').trim();
  return flat.length > FEEDBACK_EXCERPT_LIMIT ? `${flat.slice(0, FEEDBACK_EXCERPT_LIMIT).trimEnd()}\u2026` : flat;
}

// Lines of a multi-line text with control characters removed; blank runs collapse.
function plainLines(text) {
  const lines = [];
  for (const raw of String(text ?? '').split(/\r\n|[\n\r\u0085\u2028\u2029]/)) {
    const line = clean(raw).trimEnd();
    if (line.trim() === '' && (lines.length === 0 || lines[lines.length - 1] === '')) continue;
    lines.push(line.trim() === '' ? '' : line);
  }
  while (lines.length > 0 && lines[lines.length - 1] === '') lines.pop();
  return lines;
}

// Author names and ids are data: flatten control characters and line separators.
function clean(value) {
  return String(value ?? '').replace(/[\u0000-\u001f\u007f-\u009f\u2028\u2029]/g, ' ');
}
