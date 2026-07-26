import { Buffer } from "node:buffer";
import { readFileSync, writeFileSync } from "node:fs";
import { isAbsolute, relative, resolve, sep } from "node:path";
import { Type } from "typebox";

function fail(message) {
  throw new Error(`canvasbench phase contract: ${message}`);
}

function parseContract() {
  const raw = process.env.CANVASBENCH_PI_PHASE_CONTRACT;
  if (!raw) fail("CANVASBENCH_PI_PHASE_CONTRACT is required");
  let contract;
  try {
    contract = JSON.parse(raw);
  } catch {
    fail("contract is not valid JSON");
  }
  if (contract?.kind !== "pi_fixed_slots_v1" ||
      !/^[a-z][a-z0-9_]{0,63}$/.test(contract?.tool_name ?? "") ||
      typeof contract?.target_path !== "string" || isAbsolute(contract.target_path) ||
      !Array.isArray(contract?.slots) || contract.slots.length < 1 || contract.slots.length > 16) {
    fail("contract shape is invalid");
  }
  const ids = new Set();
  const markers = new Set();
  for (const slot of contract.slots) {
    if (!/^[a-z][a-z0-9_]{0,63}$/.test(slot?.id ?? "") || ids.has(slot.id) ||
        typeof slot?.marker !== "string" || slot.marker.length === 0 || markers.has(slot.marker) ||
        typeof slot?.description !== "string" || slot.description.trim() === "" ||
        typeof slot?.replacement_pattern !== "string" ||
        (slot?.fixed_replacement !== undefined && typeof slot.fixed_replacement !== "string") ||
        !Array.isArray(slot?.forbidden_substrings ?? []) ||
        !Number.isInteger(slot?.max_bytes) || slot.max_bytes < 1 || slot.max_bytes > 4096) {
      fail("slot shape is invalid");
    }
    try {
      slot.pattern = new RegExp(slot.replacement_pattern);
    } catch {
      fail(`slot ${slot.id} pattern is invalid`);
    }
    if (slot.fixed_replacement !== undefined &&
        (Buffer.byteLength(slot.fixed_replacement, "utf8") > slot.max_bytes || !slot.pattern.test(slot.fixed_replacement))) {
      fail(`slot ${slot.id} fixed replacement is invalid`);
    }
    for (const forbidden of slot.forbidden_substrings ?? []) {
      if (slot.fixed_replacement?.includes(forbidden)) fail(`slot ${slot.id} fixed replacement contains forbidden token ${forbidden}`);
    }
    ids.add(slot.id);
    markers.add(slot.marker);
  }
  return contract;
}

export default function canvasbenchPhaseContract(pi) {
  const contract = parseContract();
  const properties = {};
  for (const slot of contract.slots) {
    properties[slot.id] = slot.fixed_replacement === undefined
      ? Type.String({ description: slot.description, minLength: 1, maxLength: slot.max_bytes })
      : Type.Boolean({ description: `${slot.description} Set true to accept the orchestration-compiled replacement.` });
  }

  pi.registerTool({
    name: contract.tool_name,
    label: "Complete compiled contract",
    description: "Supply every compiled replacement slot exactly once. The host validates every slot before applying the replacements.",
    parameters: Type.Object(properties, { additionalProperties: false }),
    async execute(_toolCallId, params) {
      const workspace = resolve(process.cwd());
      const target = resolve(workspace, contract.target_path);
      const targetRelative = relative(workspace, target);
      if (targetRelative === "" || targetRelative === ".." || targetRelative.startsWith(`..${sep}`) || isAbsolute(targetRelative)) {
        fail("target escapes the workspace");
      }
      let content = readFileSync(target, "utf8");
      for (const slot of contract.slots) {
        if (slot.fixed_replacement !== undefined && params[slot.id] !== true) {
          fail(`slot ${slot.id} must accept its fixed replacement`);
        }
        const replacement = slot.fixed_replacement ?? params[slot.id];
        if (typeof replacement !== "string" || Buffer.byteLength(replacement, "utf8") > slot.max_bytes ||
            !slot.pattern.test(replacement)) {
          fail(`slot ${slot.id} violates its semantic pattern`);
        }
        for (const forbidden of slot.forbidden_substrings ?? []) {
          if (replacement.includes(forbidden)) fail(`slot ${slot.id} contains forbidden token ${forbidden}`);
        }
        const parts = content.split(slot.marker);
        if (parts.length !== 2) fail(`marker for slot ${slot.id} must occur exactly once`);
        const markerIndent = parts[0].slice(parts[0].lastIndexOf("\n") + 1);
        if (!/^\s*$/.test(markerIndent)) fail(`marker for slot ${slot.id} is not isolated on its line`);
        content = parts[0] + replacement.replaceAll("\n", `\n${markerIndent}`) + parts[1];
      }
      for (const slot of contract.slots) {
        if (content.includes(slot.marker)) fail(`marker for slot ${slot.id} remains after replacement`);
      }
      writeFileSync(target, content, { encoding: "utf8", mode: 0o644 });
      return {
        content: [{ type: "text", text: `Applied ${contract.slots.length} validated contract slots to ${contract.target_path}.` }],
        details: { target: contract.target_path, slots: contract.slots.map((slot) => slot.id) },
        terminate: true,
      };
    },
  });
}
