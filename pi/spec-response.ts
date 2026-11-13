import { chmodSync, existsSync, mkdirSync, readFileSync, readdirSync, renameSync, rmSync, writeFileSync } from "node:fs";
import { basename, dirname, join } from "node:path";
import { getAgentDir, type ExtensionAPI, type ExtensionCommandContext } from "@earendil-works/pi-coding-agent";
import { launchViewer, notify as notifyWith, type NotifyContext, type NotifyLevel, type StartedViewer } from "./viewer-launch.ts";

type ReadonlySessionManager = ExtensionCommandContext["sessionManager"];

const SUBCOMMANDS = ["open", "status", "stop"];
const BACKFILL_COUNT = 10; // the latest response plus up to 9 earlier ones
const MAX_SLUG_LENGTH = 40;
const INDEX_FILE_NAME = ".index.json"; // dot-prefixed: the viewer ignores it

/** A finished assistant response found on the session's current branch. */
export interface FinishedResponse {
	entryId: string;
	markdown: string;
}

/** Maps session entry id -> response file name, so a response keeps its file once written. */
interface ResponseIndex {
	entries: Record<string, string>;
}

function notify(ctx: NotifyContext, message: string, level: NotifyLevel = "info"): void {
	notifyWith("spec-response", ctx, message, level);
}

/** Text blocks of an assistant message joined with a blank line; undefined when there is no visible text. */
export function extractMarkdown(content: unknown): string | undefined {
	if (!Array.isArray(content)) return undefined;
	const texts: string[] = [];
	for (const block of content) {
		if (block && typeof block === "object" && block.type === "text" && typeof block.text === "string" && block.text.trim()) {
			texts.push(block.text);
		}
	}
	return texts.length > 0 ? texts.join("\n\n") : undefined;
}

/**
 * Finished assistant responses on the current branch, oldest first. Only
 * stopReason "stop" counts: "toolUse" is a mid-run message, "aborted" and
 * "error" are not answers. Entries without a string id cannot be tracked.
 */
export function finishedResponses(sessionManager: Pick<ReadonlySessionManager, "getBranch">): FinishedResponse[] {
	const responses: FinishedResponse[] = [];
	for (const entry of sessionManager.getBranch()) {
		if (entry.type !== "message") continue;
		const message = entry.message;
		if (message.role !== "assistant" || message.stopReason !== "stop") continue;
		if (typeof entry.id !== "string" || !entry.id) continue;
		const markdown = extractMarkdown(message.content);
		if (markdown) responses.push({ entryId: entry.id, markdown });
	}
	return responses;
}

/** Lowercase [a-z0-9-] slug from the first non-empty line, heading markers stripped. */
export function slugFromMarkdown(markdown: string): string {
	const firstLine = markdown.split("\n").find((line) => line.trim().length > 0) ?? "";
	const slug = firstLine
		.replace(/^\s*#+\s*/, "")
		.toLowerCase()
		.replace(/[^a-z0-9]+/g, "-")
		.replace(/^-+/, "")
		.slice(0, MAX_SLUG_LENGTH)
		.replace(/-+$/, "");
	return slug || "response";
}

/** "<basename of cwd>-<first 8 chars of session id>", restricted to [A-Za-z0-9._-]. */
export function responseFolderName(cwd: string, sessionId: string): string {
	const base = basename(cwd).replace(/[^A-Za-z0-9._-]/g, "-").replace(/^\.+/, "") || "session";
	return `${base}-${sessionId.slice(0, 8)}`.replace(/[^A-Za-z0-9._-]/g, "-");
}

function responseFolderPath(cwd: string, sessionId: string): string {
	return join(getAgentDir(), "cache", "spec-viewer", "responses", responseFolderName(cwd, sessionId));
}

function loadIndex(folder: string): ResponseIndex {
	try {
		const parsed: unknown = JSON.parse(readFileSync(join(folder, INDEX_FILE_NAME), "utf8"));
		const entries = (parsed as { entries?: unknown } | null)?.entries;
		if (entries && typeof entries === "object") {
			const valid: Record<string, string> = {};
			for (const [entryId, fileName] of Object.entries(entries)) {
				if (typeof fileName === "string" && fileName && basename(fileName) === fileName) valid[entryId] = fileName;
			}
			return { entries: valid };
		}
	} catch {
		// missing or unreadable index: start empty; new files never overwrite existing ones
	}
	return { entries: {} };
}

/** Write via a dot-prefixed, non-markdown temp file and rename, so the viewer never reloads a half-written file. */
function writeFileAtomic(path: string, content: string): void {
	const tmp = join(dirname(path), `.${basename(path)}.part`);
	try {
		writeFileSync(tmp, content, { mode: 0o600 });
		renameSync(tmp, path);
	} catch (err) {
		rmSync(tmp, { force: true });
		throw err;
	}
}

function saveIndex(folder: string, index: ResponseIndex): void {
	writeFileAtomic(join(folder, INDEX_FILE_NAME), `${JSON.stringify(index, null, 2)}\n`);
}

function pad(n: number): string {
	return String(n).padStart(3, "0");
}

/**
 * Pick the file name for a new response: its 1-based position on the branch
 * unless a file with that number prefix already exists (it belongs to another
 * entry, e.g. after /tree or fork), in which case the next free number.
 */
export function pickFileName(takenNames: Iterable<string>, position: number, markdown: string): string {
	const taken = [...takenNames];
	let n = position;
	while (taken.some((name) => name.startsWith(`${pad(n)}-`))) n++;
	return `${pad(n)}-${slugFromMarkdown(markdown)}.md`;
}

/**
 * Make sure `response` (at 1-based `position` among the branch's finished
 * responses) has a file in `folder`. Returns the file path and whether anything
 * was written. A file whose content already matches is never rewritten.
 */
function ensureResponseFile(folder: string, index: ResponseIndex, response: FinishedResponse, position: number): { path: string; written: boolean } {
	let fileName = index.entries[response.entryId];
	let isNew = false;
	if (!fileName) {
		const taken = new Set([...Object.values(index.entries), ...readdirSync(folder)]);
		fileName = pickFileName(taken, position, response.markdown);
		isNew = true;
	}
	const path = join(folder, fileName);
	let current: string | undefined;
	try {
		current = readFileSync(path, "utf8");
	} catch {
		current = undefined;
	}
	const written = current !== response.markdown;
	if (written) writeFileAtomic(path, response.markdown);
	if (isNew) {
		index.entries[response.entryId] = fileName;
		saveIndex(folder, index);
	}
	return { path, written };
}

function prepareFolder(folder: string): void {
	mkdirSync(folder, { recursive: true, mode: 0o700 });
	chmodSync(folder, 0o700);
}

/** Write the last `count` finished responses (oldest first); returns the path of the latest one. */
function writeResponses(folder: string, responses: FinishedResponse[], count: number): string | undefined {
	prepareFolder(folder);
	const index = loadIndex(folder);
	let latestPath: string | undefined;
	for (let i = Math.max(0, responses.length - count); i < responses.length; i++) {
		latestPath = ensureResponseFile(folder, index, responses[i], i + 1).path;
	}
	return latestPath;
}

export function registerSpecResponse(pi: ExtensionAPI, viewers: StartedViewer[]): void {
	let following = false;
	let viewerUrl = "";
	let errorShown = false;
	// Serialises file writes and viewer launches so they happen in order and never overlap.
	let queue: Promise<void> = Promise.resolve();

	function enqueue(task: () => Promise<void>): Promise<void> {
		const next = queue.then(task, task);
		queue = next.catch(() => undefined);
		return next;
	}

	function snapshot(ctx: { cwd: string; sessionManager: ReadonlySessionManager }) {
		const sessionId = ctx.sessionManager.getSessionId();
		const cwd = ctx.sessionManager.getCwd() || ctx.cwd;
		return { folder: responseFolderPath(cwd, sessionId), responses: finishedResponses(ctx.sessionManager) };
	}

	/** Run the viewer on `path` and remember its URL; returns the URL, or a problem to report. */
	async function runViewer(path: string, noOpen: boolean, cwd: string): Promise<{ url?: string; problem?: { message: string; level: NotifyLevel } }> {
		const result = await launchViewer(viewers, noOpen ? ["--no-open", path] : [path], cwd);
		if (result.kind === "problem") return { problem: { message: result.message, level: result.level } };
		if (result.url) viewerUrl = result.url;
		return { url: result.url };
	}

	async function showLatest(ctx: ExtensionCommandContext, folder: string, latestPath: string, openFolderFirst: boolean): Promise<void> {
		if (openFolderFirst) {
			// Make the folder a workspace first; --no-open so only the file run opens a tab.
			const folderRun = await runViewer(folder, true, ctx.cwd);
			if (folderRun.problem) return notify(ctx, folderRun.problem.message, folderRun.problem.level);
		}
		const fileRun = await runViewer(latestPath, false, ctx.cwd);
		if (fileRun.problem) return notify(ctx, fileRun.problem.message, fileRun.problem.level);
		notify(ctx, `Showing the latest response${fileRun.url ? ` at ${fileRun.url}` : ""}`);
	}

	async function start(ctx: ExtensionCommandContext): Promise<void> {
		const { folder, responses } = snapshot(ctx);
		following = true;
		errorShown = false;
		let latestPath: string | undefined;
		try {
			latestPath = writeResponses(folder, responses, BACKFILL_COUNT);
		} catch (err) {
			notify(ctx, `Could not write responses to ${folder}: ${errorMessage(err)}`, "error");
			return;
		}
		if (!latestPath) {
			notify(ctx, "No finished assistant response yet. Following the session: each response will appear after its run ends.");
			return;
		}
		await showLatest(ctx, folder, latestPath, true);
		notify(ctx, `Following the session. Responses are written to ${folder}`);
	}

	async function open(ctx: ExtensionCommandContext): Promise<void> {
		const { folder, responses } = snapshot(ctx);
		let latestPath: string | undefined;
		try {
			latestPath = writeResponses(folder, responses, BACKFILL_COUNT);
		} catch (err) {
			notify(ctx, `Could not write responses to ${folder}: ${errorMessage(err)}`, "error");
			return;
		}
		if (!latestPath) {
			notify(ctx, "No finished assistant response yet.");
			return;
		}
		await showLatest(ctx, folder, latestPath, false);
	}

	function status(ctx: ExtensionCommandContext): void {
		const { folder } = snapshot(ctx);
		const written = existsSync(folder) ? Object.keys(loadIndex(folder).entries).length : 0;
		notify(
			ctx,
			[
				`Following: ${following ? "on" : "off"}`,
				`Folder: ${folder}`,
				`Responses written: ${written}`,
				`Viewer: ${viewerUrl || "unknown (not opened by this session yet)"}`,
			].join("\n"),
		);
	}

	/** Surface a failure of the background follow step once, not on every turn. */
	function reportFollowError(ctx: NotifyContext, message: string, level: NotifyLevel): void {
		if (errorShown) return;
		errorShown = true;
		try {
			notify(ctx, `${message} (further errors while following are not repeated)`, level);
		} catch {
			// the context may be gone after a session change
		}
	}

	async function followStep(ctx: NotifyContext, folder: string, response: FinishedResponse, position: number, cwd: string): Promise<void> {
		if (!following) return;
		try {
			prepareFolder(folder);
			const index = loadIndex(folder);
			const { path, written } = ensureResponseFile(folder, index, response, position);
			if (!written) return; // already shown: nothing changed, so no navigation either
			const run = await runViewer(path, true, cwd);
			if (run.problem) reportFollowError(ctx, run.problem.message, run.problem.level);
		} catch (err) {
			reportFollowError(ctx, `Could not follow the session: ${errorMessage(err)}`, "error");
		}
	}

	// agent_settled fires once Pi will not continue the run (after retries and queued work), so the
	// final assistant message is already persisted with its entry id. The work is queued and not
	// awaited: the agent loop is never delayed and nothing is thrown into it.
	pi.on("agent_settled", (_event, ctx) => {
		if (!following) return;
		try {
			const { folder, responses } = snapshot(ctx);
			const latest = responses[responses.length - 1];
			if (!latest) return;
			const position = responses.length;
			void enqueue(() => followStep(ctx, folder, latest, position, ctx.cwd));
		} catch (err) {
			reportFollowError(ctx, `Could not follow the session: ${errorMessage(err)}`, "error");
		}
	});

	// Switching, forking, reloading and quitting all end this extension instance: stop following.
	pi.on("session_shutdown", () => {
		following = false;
	});

	pi.registerCommand("spec-response", {
		description: "Show the agent's responses in spec-viewer and follow the session (also: open, status, stop)",
		getArgumentCompletions: (prefix: string) => {
			const items = SUBCOMMANDS.filter((word) => word.startsWith(prefix)).map((word) => ({ value: word, label: word }));
			return items.length > 0 ? items : null;
		},
		handler: async (args, ctx) => {
			const arg = args.trim();
			try {
				if (arg === "") return await enqueue(() => start(ctx));
				if (arg === "open") return await enqueue(() => open(ctx));
				if (arg === "status") return status(ctx);
				if (arg === "stop") {
					const wasFollowing = following;
					following = false;
					notify(ctx, wasFollowing ? "Stopped following. Response files and the viewer are left as they are." : "Not following.");
					return;
				}
				notify(ctx, `Unknown argument "${arg}". Usage: /spec-response [open|status|stop]`, "warning");
			} catch (err) {
				notify(ctx, `/spec-response failed: ${errorMessage(err)}`, "error");
			}
		},
	});
}

function errorMessage(err: unknown): string {
	return err instanceof Error ? err.message : String(err);
}
