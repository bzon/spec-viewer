import { readdirSync, statSync } from "node:fs";
import { homedir } from "node:os";
import { join, resolve } from "node:path";
import type { ExtensionAPI, ExtensionCommandContext } from "@earendil-works/pi-coding-agent";
import { registerSpecResponse } from "./spec-response.ts";
import { launchViewer, notify as notifyWith, removeLogDir, type NotifyLevel, type StartedViewer } from "./viewer-launch.ts";

const MAX_COMPLETIONS = 50;
const SKIPPED_DIRECTORIES = new Set(["node_modules", ".git"]);

function notify(ctx: ExtensionCommandContext, message: string, level: NotifyLevel = "info"): void {
	notifyWith("spec-view", ctx, message, level);
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

export default function (pi: ExtensionAPI) {
	const viewers: StartedViewer[] = [];
	let sessionCwd = process.cwd();

	async function start(target: string, ctx: ExtensionCommandContext): Promise<void> {
		const result = await launchViewer(viewers, [target], ctx.cwd);
		switch (result.kind) {
			case "serving":
				notify(ctx, `spec-viewer serving ${target} at ${result.url}`);
				return;
			case "sent":
				notify(
					ctx,
					`Sent ${target} to the already-running spec-viewer (port ${result.port})` +
						`${result.url ? ` at ${result.url}` : ""}. That shared viewer was started elsewhere; ` +
						"/spec-view stop does not stop it.",
				);
				return;
			case "problem":
				notify(ctx, result.message, result.level);
				return;
		}
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

	registerSpecResponse(pi, viewers);

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
