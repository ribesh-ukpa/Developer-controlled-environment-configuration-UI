"use strict";

// Tiny vanilla-JS frontend. No framework, no build step.
const $ = (sel) => document.querySelector(sel);

const state = {
  ns: null,
  kind: "configmaps",
  name: null,
  original: {}, // last-loaded data, for diffing
};

// --- API helpers -----------------------------------------------------------
async function api(path, opts) {
  const res = await fetch(path, opts);
  if (!res.ok) {
    let msg = res.statusText;
    try { msg = (await res.json()).error || msg; } catch (_) {}
    throw new Error(msg);
  }
  return res.json();
}

// --- Persisted developer name ---------------------------------------------
const devNameInput = $("#devName");
devNameInput.value = localStorage.getItem("devName") || "";
devNameInput.addEventListener("input", () =>
  localStorage.setItem("devName", devNameInput.value.trim())
);

// --- Bootstrapping ---------------------------------------------------------
async function init() {
  const { namespaces, allowSecrets } = await api("/api/namespaces");
  const nsSel = $("#ns");
  nsSel.innerHTML = namespaces.map((n) => `<option>${n}</option>`).join("");
  state.ns = namespaces[0];

  if (!allowSecrets) {
    $("#kind").querySelector('option[value="secrets"]').remove();
  }

  nsSel.addEventListener("change", () => { state.ns = nsSel.value; loadList(); });
  $("#kind").addEventListener("change", (e) => { state.kind = e.target.value; loadList(); });

  await loadList();
}

// --- Resource list ---------------------------------------------------------
async function loadList() {
  closeEditor();
  const ul = $("#list");
  ul.innerHTML = '<li class="muted">loading…</li>';
  try {
    const { names } = await api(`/api/resources/${state.ns}/${state.kind}`);
    if (!names || names.length === 0) {
      ul.innerHTML = '<li class="muted">none found</li>';
      return;
    }
    ul.innerHTML = "";
    names.forEach((name) => {
      const li = document.createElement("li");
      li.textContent = name;
      li.onclick = () => { selectItem(li, name); };
      ul.appendChild(li);
    });
  } catch (e) {
    ul.innerHTML = `<li class="err">${e.message}</li>`;
  }
}

function selectItem(li, name) {
  document.querySelectorAll("#list li").forEach((x) => x.classList.remove("active"));
  li.classList.add("active");
  loadEditor(name);
}

// --- Editor ----------------------------------------------------------------
async function loadEditor(name) {
  const { data } = await api(`/api/resources/${state.ns}/${state.kind}/${name}`);
  state.name = name;
  state.original = { ...data };

  $("#editorTitle").textContent = `${state.kind.slice(0, -1)} · ${name}`;
  const isSecret = state.kind === "secrets";
  $("#revealWrap").classList.toggle("hidden", !isSecret);
  $("#reveal").checked = false;

  const body = $("#kvBody");
  body.innerHTML = "";
  Object.entries(data).forEach(([k, v]) => addRow(k, v));
  applyReveal();
  $("#status").textContent = "";
  $("#editor").classList.remove("hidden");
}

function addRow(key = "", value = "") {
  const tr = document.createElement("tr");
  const masked = state.kind === "secrets";
  tr.innerHTML = `
    <td><input class="k" value="${escapeAttr(key)}" placeholder="KEY" /></td>
    <td><textarea class="v" rows="1" ${masked ? 'data-secret="1"' : ""}>${escapeHtml(value)}</textarea></td>
    <td><button class="del ghost" title="remove">✕</button></td>`;
  tr.querySelector(".del").onclick = () => tr.remove();
  $("#kvBody").appendChild(tr);
}

function applyReveal() {
  const reveal = $("#reveal").checked;
  document.querySelectorAll('textarea.v[data-secret="1"]').forEach((t) => {
    t.style.webkitTextSecurity = reveal ? "none" : "disc";
    t.style.textSecurity = reveal ? "none" : "disc";
  });
}

function collect() {
  const data = {};
  document.querySelectorAll("#kvBody tr").forEach((tr) => {
    const k = tr.querySelector(".k").value.trim();
    const v = tr.querySelector(".v").value;
    if (k) data[k] = v;
  });
  return data;
}

function closeEditor() {
  $("#editor").classList.add("hidden");
  state.name = null;
}

// --- Diff + save -----------------------------------------------------------
function buildDiff(oldData, newData) {
  const keys = new Set([...Object.keys(oldData), ...Object.keys(newData)]);
  const lines = [];
  [...keys].sort().forEach((k) => {
    const o = oldData[k], n = newData[k];
    if (o === n) return;
    if (o === undefined) lines.push(`+ ${k}`);
    else if (n === undefined) lines.push(`- ${k}`);
    else lines.push(`~ ${k}`);
  });
  return lines;
}

$("#save").onclick = () => {
  const newData = collect();
  const diff = buildDiff(state.original, newData);
  if (diff.length === 0) {
    $("#status").textContent = "no changes";
    return;
  }
  $("#confirmTarget").textContent = `${state.ns} / ${state.kind} / ${state.name}`;
  $("#diff").textContent = diff.join("\n");
  $("#confirm").__data = newData;
  $("#confirm").showModal();
};

$("#apply").onclick = async () => {
  const dlg = $("#confirm");
  const newData = dlg.__data;
  dlg.close();
  $("#status").textContent = "saving…";
  try {
    const res = await api(`/api/resources/${state.ns}/${state.kind}/${state.name}`, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ data: newData, devName: devNameInput.value.trim() }),
    });
    state.original = { ...newData };
    $("#status").textContent = `saved (${res.changedKeys.length} key(s) changed)`;
  } catch (e) {
    $("#status").textContent = `error: ${e.message}`;
  }
};

$("#cancel").onclick = () => $("#confirm").close();
$("#addRow").onclick = () => addRow();
$("#reveal").onchange = applyReveal;

// --- escaping --------------------------------------------------------------
function escapeHtml(s) {
  return String(s).replace(/[&<>]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;" }[c]));
}
function escapeAttr(s) {
  return escapeHtml(s).replace(/"/g, "&quot;");
}

init().catch((e) => {
  document.body.innerHTML = `<p class="err" style="padding:2rem">Failed to load: ${e.message}</p>`;
});
