// loop-guard: a Pi extension used only by loop.sh. It blocks the coding
// agent from changing the files that define "done", and from the git
// commands the loop owns. loop.sh checks the same thing afterwards with git,
// so this is the first line of defense, not the only one.
//
// Load it explicitly:  pi -e pi/loop-guard.ts ...

import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

const PROTECTED: RegExp[] = [
  /_test\.go$/,
  /(^|\/)pi\//,
  /(^|\/)\.github\//,
  /(^|\/)internal\/sim\//,
  /(^|\/)cmd\/pllsim\//,
  /(^|\/)testdata\//,
  /(^|\/)(SPEC\.md|tasks\.json|PROMPT\.md|loop\.sh|Makefile|go\.mod|go\.sum|doc\.go|errors\.go|follower_errors\.go|status\.go|README\.md|LICENSE|\.gitignore)$/,
];

const FORBIDDEN_GIT = /\bgit\s+(commit|reset|checkout|restore|stash|clean|push|rebase|merge|switch)\b/;

function isProtected(path: string): boolean {
  const p = path.replace(/^@/, "").replace(/^\.\//, "");
  return PROTECTED.some((re) => re.test(p));
}

export default function (pi: ExtensionAPI) {
  pi.on("tool_call", async (event: any) => {
    const name = String(event.toolName ?? "").toLowerCase();
    const input = event.input ?? {};

    if (name === "write" || name === "edit") {
      const path = String(input.path ?? "");
      if (path && isProtected(path)) {
        return {
          block: true,
          reason: `${path} is protected: it defines the task. Change the implementation instead.`,
        };
      }
    }

    if (name === "bash") {
      const command = String(input.command ?? "");
      if (FORBIDDEN_GIT.test(command)) {
        return { block: true, reason: "The loop owns git history. Do not commit, reset, check out, restore, stash, clean or push." };
      }
    }
    return undefined;
  });
}
