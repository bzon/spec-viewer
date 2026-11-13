import { spawn, type ChildProcess } from "node:child_process";
import { closeSync, mkdtempSync, openSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";

/** Shared by /spec-view and /spec-response: runs the spec-viewer binary and reads what it prints. */

const START_TIMEOUT_MS = 3000;
const POLL_INTERVAL_MS = 50;

export const INSTALL_HINT =
	"spec-viewer binary not found. Install it with `brew install bzon/tap/spec-viewer` " +
	"or `go install github.com/bzon/spec-viewer/cmd/spec-viewer@latest`, " +
	"or set SPEC_VIEWER_BIN to its path.";

export interface StartedViewer {
	child: ChildProcess;
	pid: number;
	target: string;
	url: string;
	logPath: string;
	exited: boolean;
	/** Holds the single-instance lock, so other sessions may have handed folders to it. */
	sharedInstance: boolean;
}

export type NotifyLevel = "info" | "warning" | "error";

/** The part of pi's context that notifications need (command and event contexts both fit). */
export interface NotifyContext {
	hasUI: boolean;
	ui: { notify(message: string, level?: NotifyLevel): void };
}

/** Notify through the UI, or print to stderr (prefixed with the command name) when there is none. */
export function notify(prefix: string, ctx: NotifyContext, message: string, level: NotifyLevel = "info"): void {
	if (ctx.hasUI) {
		ctx.ui.notify(message, level);
	} else {
		console.error(`${prefix}: ${message}`);
	}
}

export function sleep(ms: number): Promise<void> {
	return new Promise((done) => setTimeout(done, ms));
}

function readLog(logPath: string): string {
	try {
		return readFileSync(logPath, "utf8");
	} catch {
		return "";
	}
}

/** Remove the per-launch temp directory that holds the viewer's log. */
export function removeLogDir(logPath: string): void {
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

export type LaunchResult =
	/** This invocation became the server; it is recorded in `viewers`. */
	| { kind: "serving"; url: string }
	/** The running viewer took the path; the process has exited. */
	| { kind: "sent"; port: string; url: string | undefined }
	/** Spawn failure, non-zero exit, or still starting after the timeout. */
	| { kind: "problem"; message: string; level: NotifyLevel };

/**
 * Run `spec-viewer <args>` (never through a shell) and wait for its first
 * recognisable output. `args` ends with the path to show; flags such as
 * --no-open must come before it. A process that became the server keeps
 * running and is pushed onto `viewers`.
 */
export async function launchViewer(viewers: StartedViewer[], args: string[], cwd: string): Promise<LaunchResult> {
	const bin = process.env.SPEC_VIEWER_BIN || "spec-viewer";
	const target = args[args.length - 1] ?? "";
	const logPath = join(mkdtempSync(join(tmpdir(), "spec-viewer-pi-")), "spec-viewer.log");
	const logFd = openSync(logPath, "a");

	let child: ChildProcess;
	try {
		child = spawn(bin, args, {
			cwd,
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
			return { kind: "serving", url: record.url };
		}
		const sent = log.match(/spec-viewer: sent .* to existing instance on port (\d+)/);
		if (sent) {
			// The process exits right after the hand-off, so it is not recorded in `viewers`:
			// there is nothing for stop/status to manage, and its log is no longer needed.
			removeLogDir(logPath);
			const handedUrl = log.match(/spec-viewer: url (\S+)/);
			return { kind: "sent", port: sent[1], url: handedUrl?.[1] };
		}
		if (exitCode !== undefined) break;
		await sleep(POLL_INTERVAL_MS);
	}

	if (spawnError) {
		removeLogDir(logPath);
		if (spawnError.code === "ENOENT") {
			return {
				kind: "problem",
				message: process.env.SPEC_VIEWER_BIN ? `${INSTALL_HINT} (SPEC_VIEWER_BIN=${bin} was not found)` : INSTALL_HINT,
				level: "error",
			};
		}
		return { kind: "problem", message: `Could not start spec-viewer: ${spawnError.message}`, level: "error" };
	}
	const log = readLog(logPath).trim();
	if (exitCode !== undefined) {
		removeLogDir(logPath);
		return {
			kind: "problem",
			message: exitCode === 0 && log ? log : `spec-viewer exited with code ${exitCode}${log ? `: ${log}` : ""}`,
			level: exitCode === 0 ? "info" : "error",
		};
	}
	// Still running with no recognisable output yet: keep tracking it, it may still come up.
	viewers.push(record);
	return { kind: "problem", message: `spec-viewer is still starting (no output after 3s). Log: ${logPath}`, level: "warning" };
}
