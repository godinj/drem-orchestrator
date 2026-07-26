import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import test from "node:test";

import phaseContractExtension from "./canvasbench-phase-contract.mjs";

function contract(overrides = {}) {
  return {
    kind: "pi_fixed_slots_v1",
    tool_name: "complete_contract",
    target_path: "contract.cpp",
    slots: [
      { id: "first", marker: "// TODO_FIRST", description: "first assertion", replacement_pattern: "^CHECK \\([^;]+\\);$", forbidden_substrings: ["executeAction"], max_bytes: 128 },
      { id: "second", marker: "// TODO_SECOND", description: "second assertion", replacement_pattern: "^CHECK_FALSE \\([^;]+\\);$", forbidden_substrings: ["undo("], max_bytes: 128 },
    ],
    ...overrides,
  };
}

async function withExtension(t, inputContract, initial) {
  const root = mkdtempSync(join(tmpdir(), "canvasbench-pi-contract-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const previous = process.cwd();
  process.chdir(root);
  t.after(() => process.chdir(previous));
  const target = join(root, inputContract.target_path);
  mkdirSync(dirname(target), { recursive: true });
  writeFileSync(target, initial);
  process.env.CANVASBENCH_PI_PHASE_CONTRACT = JSON.stringify(inputContract);
  let tool;
  phaseContractExtension({
    registerTool(value) { tool = value; },
  });
  assert.equal(tool.name, inputContract.tool_name);
  return { root, target, tool };
}

test("applies every validated slot and terminates", async (t) => {
  const { root, tool } = await withExtension(t, contract(), "// TODO_FIRST\n// TODO_SECOND\n");
  const result = await tool.execute("call", { first: "CHECK (ready);", second: "CHECK_FALSE (failed);" });
  assert.equal(result.terminate, true);
  assert.match(readFileSync(join(root, "contract.cpp"), "utf8"), /CHECK \(ready\);\nCHECK_FALSE \(failed\);/);
});

test("rejects forbidden content without a partial write", async (t) => {
  const initial = "// TODO_FIRST\n// TODO_SECOND\n";
  const { root, tool } = await withExtension(t, contract(), initial);
  await assert.rejects(
    tool.execute("call", { first: "CHECK (ready);", second: "CHECK_FALSE (undo());" }),
    /contains forbidden token/,
  );
  assert.equal(readFileSync(join(root, "contract.cpp"), "utf8"), initial);
});

test("rejects duplicate markers without writing", async (t) => {
  const duplicate = "// TODO_FIRST\n// TODO_FIRST\n// TODO_SECOND\n";
  const duplicateRun = await withExtension(t, contract(), duplicate);
  await assert.rejects(
    duplicateRun.tool.execute("call", { first: "CHECK (ready);", second: "CHECK_FALSE (failed);" }),
    /must occur exactly once/,
  );
  assert.equal(readFileSync(join(duplicateRun.root, "contract.cpp"), "utf8"), duplicate);
});

test("rejects a target that escapes the workspace", async (t) => {
  const run = await withExtension(t, contract({ target_path: "../escape.cpp" }), "// TODO_FIRST\n// TODO_SECOND\n");
  await assert.rejects(
    run.tool.execute("call", { first: "CHECK (ready);", second: "CHECK_FALSE (failed);" }),
    /target escapes the workspace/,
  );
});

test("accepts every portable pattern published by the case-04 contract", async (t) => {
  const task = JSON.parse(readFileSync(new URL(
    "../../../../../bench/canvasbench-v2/tasks/case-04.json",
    import.meta.url,
  ), "utf8"));
  const markers = task.phase_contract.slots.map((slot) => slot.marker).join("\n");
  const run = await withExtension(t, task.phase_contract, `${markers}\n`);
  const result = await run.tool.execute("call", {
    action_ids: true,
    prev_wrap_assertions: true,
    next_wrap_assertions: 'CHECK (wrapped.getActiveClipLaneVersionIndex() == 0);\nCHECK (notifications == 3);',
    midi_noop: 'CHECK (midi.getActiveClipLaneVersionIndex() == 0);\nCHECK_FALSE (f.project.getUndoSystem().canUndo());',
    single_take_noop: 'CHECK (dc::Track (f.project.getTrack (0)).getActiveClipLaneVersionIndex() == 0);\nCHECK_FALSE (f.project.getUndoSystem().canUndo());',
  });
  assert.equal(result.terminate, true);
  const output = readFileSync(run.target, "utf8");
  assert.doesNotMatch(output, /CANVASBENCH_TODO/);
  assert.equal((output.match(/CHECK/g) ?? []).length, 12);
});

test("rejects an unaccepted fixed replacement without writing", async (t) => {
  const fixed = contract();
  fixed.slots[0].fixed_replacement = "CHECK (ready);";
  const initial = "// TODO_FIRST\n// TODO_SECOND\n";
  const run = await withExtension(t, fixed, initial);
  await assert.rejects(
    run.tool.execute("call", { first: false, second: "CHECK_FALSE (failed);" }),
    /must accept its fixed replacement/,
  );
  assert.equal(readFileSync(run.target, "utf8"), initial);
});
