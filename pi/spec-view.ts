import { spawn, type ChildProcess } from "node:child_process";
import { closeSync, mkdtempSync, openSync, readFileSync, readdirSync, rmSync, statSync } from "node:fs";
import { homedir, tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import type { ExtensionAPI, ExtensionCommandContext } from "@earendil-works/pi-coding-agent";

const START_TIMEOUT_MS = 3000;
const POLL_INTERVAL_MS = 50;
const MAX_COMPLETIONS = 50;
const SKIPPED_DIRECTORIES = new Set(["node_modules", ".git"]);
const INSTALL_HINT =
	"spec-viewer binary not found. Install it with `brew install bzon/tap/spec-viewer` " +
	"or `go install github.com/bzon/spec-viewer/cmd/spec-viewer@latest`, " +
	"or set SPEC_VIEWER_BIN to its path.";

interface StartedViewer {
	child: ChildProcess;
	pid: number;
	target: string;
	url: string;
	logPath: string;
	exited: boolean;
	/** Holds the single-instance lock, so other sessions may have handed folders to it. */
	sharedInstance: boolean;
}

type NotifyLevel = "info" | "warning" | "error";

function notify(ctx: ExtensionCommandContext, message: string, level: NotifyLevel = "info"): void {
	if (ctx.hasUI) {
		ctx.ui.notify(message, level);
	} else {
		console.error(`spec-view: ${message}`);
	}
}

function expandPath(raw: string, cwd: string): string {
	let p = raw.trim();
	if (p.length >= 2 && ((p.startsWith('"') && p.endsWith('"')) || (p.startsWith("'") && p.endsWith("'")))) {
		p = p.slice(1, -1);
	}
	if (p.startsWith("@")) p = p.slice(1);
	if (p === "~") p = homedir();
	else if (p.startsWith("~/")) p = join(homedir(), p.slice(2));
	return resolve(cwd, p || ".");
}

function isMarkdownFile(name: string): boolean {
	return /\.(md|markdown)$/i.test(name);
}

function readLog(logPath: string): string {
	try {
		return readFileSync(logPath, "utf8");
	} catch {
		return "";
	}
}

/** Remove the per-launch temp directory that holds the viewer's log. */
function removeLogDir(logPath: string): void {
	try {
		rmSync(dirname(logPath), { recursive: true, force: true });
	} catch {
		// best effort: the OS temp cleaner gets it eventually
	}
}

/**
 * True when the viewer printed neither "keeps the shared lock" (another viewer
 * owns it) nor "hand-off is disabled" (network-exposed, lock-less): it holds
 * the shared lock, so other sessions may have handed folders to it.
 */
function holdsSharedLock(log: string): boolean {
	return !/keeps the shared lock|hand-off is disabled/.test(log);
}

function sleep(ms: number): Promise<void> {
	return new Promise((done) => setTimeout(done, ms));
}

export default function (pi: ExtensionAPI) {
	const viewers: StartedViewer[] = [];
	let sessionCwd = process.cwd();

	async function start(target: string, ctx: ExtensionCommandContext): Promise<void> {
		const bin = process.env.SPEC_VIEWER_BIN || "spec-viewer";
		const logPath = join(mkdtempSync(join(tmpdir(), "spec-viewer-pi-")), "spec-viewer.log");
		const logFd = openSync(logPath, "a");

		let child: ChildProcess;
		try {
			child = spawn(bin, [target], {
				cwd: ctx.cwd,
				detached: true,
				stdio: ["ignore", logFd, logFd],
				windowsHide: true,
			});
		} finally {
			closeSync(logFd);
		}

		let exitCode: number | null | undefined;
		let spawnError: NodeJS.ErrnoException | undefined;
		child.on("error", (err) => {
			spawnError = err;
		});
		const record: StartedViewer = { child, pid: child.pid ?? -1, target, url: "", logPath, exited: false, sharedInstance: false };
		child.on("exit", (code, signal) => {
			record.exited = true;
			exitCode = code ?? (signal ? -1 : 0);
		});
		child.unref();

		const deadline = Date.now() + START_TIMEOUT_MS;
		while (Date.now() < deadline) {
			if (spawnError) break;
			const log = readLog(logPath);
			const served = log.match(/spec-viewer: serving .* at (\S+)/);
			if (served) {
				record.url = served[1];
				// The lock notice is printed right after the serving line; give it a moment.
				await sleep(150);
				record.sharedInstance = holdsSharedLock(readLog(logPath));
				viewers.push(record);
				notify(ctx, `spec-viewer serving ${target} at ${record.url}`);
				return;
			}
			const sent = log.match(/spec-viewer: sent .* to existing instance on port (\d+)/);
			if (sent) {
				// The process exits right after the hand-off, so it is not recorded in `viewers`:
				// there is nothing for stop/status to manage, and its log is no longer needed.
				removeLogDir(logPath);
				const handedUrl = log.match(/spec-viewer: url (\S+)/);
				notify(
					ctx,
					`Sent ${target} to the already-running spec-viewer (port ${sent[1]})` +
						`${handedUrl ? ` at ${handedUrl[1]}` : ""}. That shared viewer was started elsewhere; ` +
						"/spec-view stop does not stop it.",
				);
				return;
			}
			if (exitCode !== undefined) break;
			await sleep(POLL_INTERVAL_MS);
		}

		if (spawnError) {
			removeLogDir(logPath);
			if (spawnError.code === "ENOENT") {
				notify(ctx, process.env.SPEC_VIEWER_BIN ? `${INSTALL_HINT} (SPEC_VIEWER_BIN=${bin} was not found)` : INSTALL_HINT, "error");
			} else {
				notify(ctx, `Could not start spec-viewer: ${spawnError.message}`, "error");
			}
			return;
		}
		const log = readLog(logPath).trim();
		if (exitCode !== undefined) {
			removeLogDir(logPath);
			notify(
				ctx,
				exitCode === 0 && log
					? log
					: `spec-viewer exited with code ${exitCode}${log ? `: ${log}` : ""}`,
				exitCode === 0 ? "info" : "error",
			);
			return;
		}
		// Still running with no recognisable output yet: keep tracking it, it may still come up.
		viewers.push(record);
		notify(ctx, `spec-viewer is still starting (no output after 3s). Log: ${logPath}`, "warning");
	}

	function stop(ctx: ExtensionCommandContext): void {
		const alive = viewers.filter((v) => !v.exited);
		let stopped = 0;
		const sharedStopped: StartedViewer[] = [];
		for (const v of alive) {
			try {
				process.kill(v.pid, "SIGTERM");
				stopped++;
				if (v.sharedInstance) sharedStopped.push(v);
			} catch {
				v.exited = true;
			}
		}
		for (const v of viewers) removeLogDir(v.logPath);
		viewers.length = 0;
		if (stopped === 0) {
			notify(
				ctx,
				"No viewers started by this pi session are running. Nothing was touched " +
					"(a shared viewer that took a file by hand-off was started elsewhere).",
			);
		} else {
			let message = `Stopped ${stopped} spec-viewer instance${stopped === 1 ? "" : "s"} started by this session.`;
			if (sharedStopped.length > 0) {
				const urls = sharedStopped.map((v) => v.url || `pid ${v.pid}`).join(", ");
				message +=
					` ${sharedStopped.length === 1 ? "It" : "One of them"} (${urls}) held the shared viewer lock, so other pi sessions ` +
					"may have handed folders to it: those folders are no longer served and their open tabs lose live reload " +
					"until a viewer is started again.";
			}
			notify(ctx, message, sharedStopped.length > 0 ? "warning" : "info");
		}
	}

	function status(ctx: ExtensionCommandContext): void {
		if (viewers.length === 0) {
			notify(ctx, "No viewers started by this pi session (a shared viewer that took a file by hand-off was started elsewhere).");
			return;
		}
		const lines = viewers.map(
			(v) => `${v.exited ? "exited" : "running"}  ${v.url || "(no url yet)"}  ${v.target}  (pid ${v.pid})`,
		);
		notify(ctx, `spec-viewer instances from this session:\n${lines.join("\n")}`);
	}

	pi.registerCommand("spec-view", {
		description: "Open a markdown file or directory in spec-viewer (also: stop, status; use ./stop or ./status for a path with those names)",
		getArgumentCompletions: (prefix: string) => {
			const items: { value: string; label: string }[] = [];
			for (const word of ["stop", "status"]) {
				if (word.startsWith(prefix)) items.push({ value: word, label: word });
			}
			try {
				const slash = prefix.lastIndexOf("/");
				const dirPart = slash >= 0 ? prefix.slice(0, slash + 1) : "";
				const namePart = prefix.slice(dirPart.length);
				const dir = expandPath(dirPart || ".", sessionCwd);
				for (const entry of readdirSync(dir, { withFileTypes: true })) {
					if (items.length >= MAX_COMPLETIONS) break;
					if (SKIPPED_DIRECTORIES.has(entry.name) || !entry.name.startsWith(namePart)) continue;
					if (entry.name.startsWith(".") && !namePart.startsWith(".")) continue;
					if (entry.isDirectory()) {
						items.push({ value: `${dirPart}${entry.name}/`, label: `${entry.name}/` });
					} else if (entry.isFile() && isMarkdownFile(entry.name)) {
						items.push({ value: `${dirPart}${entry.name}`, label: entry.name });
					}
				}
			} catch {
				// unreadable directory: offer only the keywords
			}
			return items.length > 0 ? items : null;
		},
		handler: async (args, ctx) => {
			sessionCwd = ctx.cwd;
			const arg = args.trim();
			// Only the bare words are subcommands; "./stop" and "./status" are paths.
			if (arg === "stop") return stop(ctx);
			if (arg === "status") return status(ctx);

			const target = expandPath(arg, ctx.cwd);
			let info;
			try {
				info = statSync(target);
			} catch {
				notify(ctx, `Path not found: ${target}`, "error");
				return;
			}
			if (!info.isDirectory() && !(info.isFile() && isMarkdownFile(target))) {
				notify(ctx, `Not a directory or markdown (.md/.markdown) file: ${target}`, "error");
				return;
			}
			await start(target, ctx);
		},
	});
}
