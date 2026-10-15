import { readFile } from "node:fs/promises";
import { request } from "node:http";

// node:http has no fetch/Undici headers deadline. The host runner owns hook
// timeouts; releasing the session cancels the request at the loopback server.
const callHook = (binding, payload) => new Promise((resolve, reject) => {
  const req = request(binding.url, {
    method: "POST", agent: false,
    headers: { "Content-Type": "application/json", Authorization: `Bearer ${binding.token}` },
  }, res => {
    res.on("error", reject);
    if (res.statusCode !== 200) {
      res.resume();
      reject(new Error("Balda tool hook unavailable; invocation rejected."));
      return;
    }
    let size = 0;
    const chunks = [];
    res.on("data", chunk => {
      size += chunk.length;
      if (size > 262144) {
        res.destroy(new Error("Balda tool hook output exceeds its limit."));
        return;
      }
      chunks.push(chunk);
    });
    res.on("end", () => {
      try { resolve(JSON.parse(Buffer.concat(chunks).toString("utf8"))); }
      catch (error) { reject(error); }
    });
  });
  req.on("error", reject);
  req.end(payload);
});

// V1 tool.execute hooks remain supported by the published OpenCode 1.19 API.
export const BaldaHooks = async () => {
  const binding = JSON.parse(await readFile(process.env.BALDA_HOOK_BINDING, "utf8"));
  const ready = await fetch(binding.url.replace(/\/invoke$/, "/ready"), {
    method: "POST", headers: { Authorization: `Bearer ${binding.token}` },
    signal: AbortSignal.timeout(5000), redirect: "error",
  });
  if (!ready.ok) throw new Error("Balda native hook initialization failed.");
  const invoke = async (name, input, args, response) => {
    const payload = {
      hook_event_name: name,
      agent_session_id: input.sessionID,
      tool_name: input.tool,
      tool_call_id: input.callID,
      tool_input: args,
      ...(response === undefined ? {} : { tool_response: response }),
    };
    const serialized = JSON.stringify(payload);
    if (Buffer.byteLength(serialized) > 262144) throw new Error("Balda tool hook input exceeds its limit.");
    const verdict = await callHook(binding, serialized);
    if (verdict.decision === "deny") throw new Error(verdict.reason || "Balda hook rejected the tool event.");
    return verdict;
  };
  return {
    "tool.execute.before": async (input, output) => {
      const result = await invoke("PreToolUse", input, output.args);
      if (result.additional_context) throw new Error("OpenCode PreToolUse supports input rewrites but cannot inject model context.");
      if (result.updated_input) output.args = result.updated_input;
    },
    "tool.execute.after": async (input, output) => {
      const result = await invoke("PostToolUse", input, input.args, { title: output.title, output: output.output, metadata: output.metadata });
      if (result.additional_context) output.output += `\n\n<balda_hook_context>\n${result.additional_context}\n</balda_hook_context>`;
    },
  };
};
