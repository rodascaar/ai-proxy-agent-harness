"use strict";

// ---------------------------------------------------------------------------
// Referencias del DOM
// ---------------------------------------------------------------------------

const messagesEl = document.getElementById("messages");
const chatEmptyEl = document.getElementById("chat-empty");
const typingEl = document.getElementById("typing");
const composer = document.getElementById("composer");
const inputEl = document.getElementById("input");
const sendBtn = document.getElementById("send");
const attachBtn = document.getElementById("attach");
const fileInput = document.getElementById("file-input");
const attachmentsEl = document.getElementById("attachments");
const statusDot = document.getElementById("status-dot");
const statusText = document.getElementById("status-text");
const configForm = document.getElementById("config-form");
const keyBadge = document.getElementById("key-badge");
const saveNote = document.getElementById("save-note");
const convListEl = document.getElementById("conv-list");
const sidebarEmptyEl = document.getElementById("sidebar-empty");
const sidebar = document.getElementById("sidebar");
const sidebarToggle = document.getElementById("sidebar-toggle");
const sidebarOverlay = document.getElementById("sidebar-overlay");
const configPanel = document.getElementById("config-panel");
const configToggle = document.getElementById("config-toggle");
const configOverlay = document.getElementById("config-overlay");
const app = document.querySelector(".app");

const WIDE_QUERY = "(min-width: 1100px)";
const MAX_FILE_BYTES = 20 * 1024 * 1024;

// Estado global del chat
let history = [];
let busy = false;
let activeConvId = null;
let conversations = [];
let attachments = [];
let drawerFocusReturnEl = null;

// ---------------------------------------------------------------------------
// Utilidades
// ---------------------------------------------------------------------------

function escapeHtml(text) {
  return text
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}

// renderInline formatea el texto inline de una línea (negrita, código, links).
// Los links se restringen a http/https para evitar XSS vía javascript:.
function renderInline(line) {
  return line
    .replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>")
    .replace(/`([^`]+)`/g, "<code>$1</code>")
    .replace(/\[([^\]]+)\]\((https?:\/\/[^\s)]+)\)/g, '<a href="$2" target="_blank" rel="noopener noreferrer">$1</a>');
}

// renderLines procesa un bloque de texto plano línea por línea y produce
// párrafos, headings, listas, blockquotes y tablas simples.
function renderLines(block) {
  const lines = block.split("\n");
  let html = "";
  let list = null;
  let para = [];
  let table = [];

  const flushPara = () => {
    if (para.length) {
      html += "<p>" + para.map(renderInline).join("<br>") + "</p>";
      para = [];
    }
  };
  const flushList = () => {
    if (list) {
      html += "</" + list.tag + ">";
      list = null;
    }
  };
  const flushTable = () => {
    if (table.length) {
      const head = table[0];
      const body = table.slice(1);
      html += "<table><thead><tr>" + head.map((c) => "<th>" + renderInline(c) + "</th>").join("") + "</tr></thead>";
      if (body.length) {
        html += "<tbody>" + body.map((row) => "<tr>" + row.map((c) => "<td>" + renderInline(c) + "</td>").join("") + "</tr>").join("") + "</tbody>";
      }
      html += "</table>";
      table = [];
    }
  };

  for (const raw of lines) {
    const line = raw.trimEnd();

    let m = /^(#{1,4})\s+(.*)$/.exec(line);
    if (m) {
      flushPara();
      flushList();
      flushTable();
      const lvl = m[1].length;
      html += "<h" + lvl + ">" + renderInline(m[2]) + "</h" + lvl + ">";
      continue;
    }

    m = /^&gt;\s?(.*)$/.exec(line);
    if (m) {
      flushPara();
      flushList();
      flushTable();
      html += "<blockquote><p>" + renderInline(m[1]) + "</p></blockquote>";
      continue;
    }

    m = /^[-*]\s+(.*)$/.exec(line);
    if (m) {
      flushPara();
      flushTable();
      if (!list || list.tag !== "ul") {
        flushList();
        html += "<ul>";
        list = { tag: "ul" };
      }
      html += "<li>" + renderInline(m[1]) + "</li>";
      continue;
    }

    m = /^\d+\.\s+(.*)$/.exec(line);
    if (m) {
      flushPara();
      flushTable();
      if (!list || list.tag !== "ol") {
        flushList();
        html += "<ol>";
        list = { tag: "ol" };
      }
      html += "<li>" + renderInline(m[1]) + "</li>";
      continue;
    }

    if (/^\|.*\|$/.test(line)) {
      flushPara();
      flushList();
      const cells = line.replace(/^\|/, "").replace(/\|$/, "").split("|").map((c) => c.trim());
      if (cells.length && !cells.every((c) => /^:?-+:?$/.test(c))) {
        table.push(cells);
      }
      continue;
    }

    if (table.length && line.trim() === "") {
      flushTable();
      continue;
    }

    if (line.trim() === "") {
      flushPara();
      flushList();
      flushTable();
      continue;
    }

    if (table.length) flushTable();
    flushList();
    para.push(line);
  }

  flushPara();
  flushList();
  flushTable();
  return html;
}

// markdownLite: escapa HTML, detecta bloques de código (```...```), y delega el
// resto a renderLines (headings, listas, tablas, links, negrita, inline code).
// Suficiente para un chat sin arrastrar una librería.
function markdownLite(text) {
  const escaped = escapeHtml(text);
  const parts = escaped.split(/```/);
  const unclosed = parts.length % 2 === 0;
  let out = "";
  for (let i = 0; i < parts.length; i++) {
    const openFence = unclosed && i === parts.length - 1;
    if (i % 2 === 1 && !openFence) {
      out += "<pre><code>" + parts[i] + "</code></pre>";
    } else {
      out += renderLines(parts[i]);
    }
  }
  return out;
}

// contentToHTML renderiza un content de mensaje, que puede ser un string
// (texto plano) o un arreglo de partes multimodal (texto + image_url).
function contentToHTML(content) {
  if (typeof content === "string") return markdownLite(content);
  if (Array.isArray(content)) {
    const images = [];
    let text = "";
    for (const part of content) {
      if (!part || typeof part !== "object") continue;
      if (part.type === "text") {
        text += part.text || "";
      } else if (part.type === "image_url" && part.image_url && part.image_url.url) {
        images.push('<img class="attachment-img" src="' + escapeHtml(part.image_url.url) + '" alt="imagen adjunta" loading="lazy" />');
      }
    }
    const imagesHTML = images.join("");
    const textHTML = text ? markdownLite(text) : "";
    return (imagesHTML ? '<div class="attachments-row">' + imagesHTML + "</div>" : "") + textHTML;
  }
  return "";
}

function isNearBottom() {
  return messagesEl.scrollHeight - messagesEl.scrollTop - messagesEl.clientHeight < 80;
}

function scrollToBottom(force) {
  if (!force && !isNearBottom()) return;
  messagesEl.scrollTop = messagesEl.scrollHeight;
}

function updateEmptyState() {
  chatEmptyEl.hidden = messagesEl.children.length > 0;
}

function setStatus(ok, text) {
  statusDot.className = "dot " + (ok ? "ok" : "err");
  statusText.textContent = text;
}

function formatBytes(bytes) {
  if (bytes < 1024) return bytes + " B";
  if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + " KB";
  return (bytes / (1024 * 1024)).toFixed(1) + " MB";
}

function formatWhen(iso) {
  if (!iso) return "";
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "";
  const now = new Date();
  const diff = now - date;
  const minutes = Math.floor(diff / 60000);
  if (minutes < 1) return "ahora";
  if (minutes < 60) return minutes + " min";
  if (diff < 24 * 3600 * 1000) return date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  const days = Math.floor(diff / (24 * 3600 * 1000));
  if (days === 1) return "ayer";
  if (days < 7) return days + " d";
  return date.toLocaleDateString();
}

// ellipsize recorta un texto largo por el medio para que los nombres de modelo
// no desborden el selector; el nombre completo queda en el atributo title.
function ellipsize(text, max) {
  if (text.length <= max) return text;
  const head = Math.ceil((max - 1) / 2);
  const tail = Math.floor((max - 1) / 2);
  return text.slice(0, head) + "…" + text.slice(text.length - tail);
}

// ---------------------------------------------------------------------------
// Chat
// ---------------------------------------------------------------------------

function addMessage(role, html, className) {
  const wrap = document.createElement("div");
  wrap.className = "msg " + role + (className ? " " + className : "");
  const bubble = document.createElement("div");
  bubble.className = "bubble";
  bubble.innerHTML = html;
  wrap.appendChild(bubble);
  messagesEl.appendChild(wrap);
  scrollToBottom(true);
  updateEmptyState();
  return bubble;
}

// createAssistant crea el contenedor de una respuesta en streaming, con un
// bloque de reasoning colapsable y otro de contenido. Devuelve referencias
// para ir actualizando en vivo.
function createAssistant() {
  const wrap = document.createElement("div");
  wrap.className = "msg assistant";

  const bubble = document.createElement("div");
  bubble.className = "bubble";

  const reasoning = document.createElement("details");
  reasoning.className = "reasoning";
  reasoning.open = false;
  reasoning.innerHTML = "<summary>Razonamiento</summary><pre></pre>";
  reasoning.style.display = "none";

  const content = document.createElement("div");
  content.className = "content";

  bubble.appendChild(content);
  bubble.appendChild(reasoning);
  wrap.appendChild(bubble);
  messagesEl.appendChild(wrap);
  scrollToBottom(true);

  const el = {
    wrap,
    reasoning,
    reasoningPre: reasoning.querySelector("pre"),
    content,
    contentText: "",
    reasoningText: "",
  };

  el._renderTimer = null;
  el._dirty = false;
  el.scheduleRender = function () {
    el._dirty = true;
    if (el._renderTimer) return;
    el._renderTimer = setTimeout(function () {
      el._renderTimer = null;
      if (!el._dirty) return;
      el._dirty = false;
      renderInto(el.content, el.contentText);
    }, 60);
  };
  el.flushRender = function () {
    if (el._renderTimer) {
      clearTimeout(el._renderTimer);
      el._renderTimer = null;
    }
    if (el._dirty) {
      el._dirty = false;
      renderInto(el.content, el.contentText);
    }
  };
  el.dispose = function () {
    if (el._renderTimer) clearTimeout(el._renderTimer);
    el._renderTimer = null;
    el._dirty = false;
  };

  return el;
}

function renderInto(el, text) {
  el.innerHTML = markdownLite(text);
  scrollToBottom(false);
}

// buildUserContent compone el content del mensaje user: string plano cuando no
// hay adjuntos (compatibilidad), o arreglo de partes (texto + imágenes) cuando
// los hay.
function buildUserContent(text) {
  if (attachments.length === 0) return text;
  const parts = [];
  if (text) parts.push({ type: "text", text: text });
  for (const att of attachments) {
    if (att.kind === "image") {
      parts.push({ type: "image_url", image_url: { url: att.dataUrl } });
    } else {
      parts.push({ type: "text", text: "[Archivo: " + att.name + "]\n" + att.text });
    }
  }
  return parts;
}

function clearAttachments() {
  attachments = [];
  attachmentsEl.hidden = true;
  attachmentsEl.innerHTML = "";
}

async function sendMessage() {
  const text = inputEl.value.trim();
  const content = buildUserContent(text);
  history.push({ role: "user", content: content });
  addMessage("user", contentToHTML(content));
  inputEl.value = "";
  inputEl.style.height = "auto";
  clearAttachments();

  const assistant = createAssistant();
  const modelEl = document.getElementById("model-select");
  const model = (modelEl && modelEl.value) || "";

  busy = true;
  sendBtn.disabled = true;
  inputEl.disabled = true;
  attachBtn.disabled = true;
  typingEl.hidden = false;

  const headers = { "Content-Type": "application/json" };
  if (activeConvId) headers["X-Conversation-ID"] = activeConvId;
  messagesEl.setAttribute("aria-busy", "true");

  try {
    const res = await fetch("/v1/chat/completions", {
      method: "POST",
      headers: headers,
      body: JSON.stringify({
        model: model,
        stream: true,
        messages: history,
      }),
    });

    if (!res.ok) {
      const body = await res.text();
      throw new Error(body || "HTTP " + res.status);
    }

    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";

    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });

      let idx;
      while ((idx = buffer.indexOf("\n\n")) !== -1) {
        const raw = buffer.slice(0, idx);
        buffer = buffer.slice(idx + 2);
        handleSSE(raw, assistant);
      }
    }

    assistant.flushRender();
    if (assistant.contentText.trim() !== "") {
      history.push({ role: "assistant", content: assistant.contentText });
    }
    refreshConversations();
  } catch (err) {
    assistant.dispose();
    assistant.wrap && assistant.wrap.remove();
    addMessage(
      "assistant",
      escapeHtml("Error: " + (err.message || err)),
      "error"
    );
  } finally {
    busy = false;
    sendBtn.disabled = false;
    inputEl.disabled = false;
    attachBtn.disabled = false;
    typingEl.hidden = true;
    messagesEl.removeAttribute("aria-busy");
    inputEl.focus();
  }
}

function handleSSE(raw, assistant) {
  const lines = raw.split("\n");
  for (const line of lines) {
    if (!line.startsWith("data:")) continue;
    const data = line.slice(5).trim();
    if (data === "" || data === "[DONE]") continue;

    let chunk;
    try {
      chunk = JSON.parse(data);
    } catch {
      continue;
    }

    if (chunk.error) {
      assistant.contentText += "\n\n**Error:** " + chunk.error.message;
      assistant.scheduleRender();
      continue;
    }
    if (!chunk.choices || chunk.choices.length === 0) continue;

    const delta = chunk.choices[0].delta || {};
    if (delta.reasoning_content) {
      assistant.reasoning.style.display = "";
      assistant.reasoningText += delta.reasoning_content;
      assistant.reasoningPre.textContent = assistant.reasoningText;
      scrollToBottom(false);
    }
    if (delta.content) {
      assistant.contentText += delta.content;
      assistant.scheduleRender();
    }
    if (delta.tool_calls && delta.tool_calls.length > 0) {
      const names = delta.tool_calls.map((tc) => tc.function && tc.function.name).filter(Boolean);
      if (names.length > 0) {
        assistant.contentText += "\n\n_[Herramienta: " + names.join(", ") + "]_";
        assistant.scheduleRender();
      }
    }
  }
}

// ---------------------------------------------------------------------------
// Adjuntos
// ---------------------------------------------------------------------------

async function onFilesSelected(fileList) {
  const files = Array.from(fileList || []);
  for (const file of files) {
    if (file.size > MAX_FILE_BYTES) {
      addMessage("assistant", escapeHtml("El archivo \"" + file.name + "\" supera el límite de 20 MB."), "error");
      continue;
    }
    if (file.type.startsWith("image/") || /\.(png|jpe?g|gif|webp|bmp)$/i.test(file.name)) {
      attachments.push(await readImage(file));
    } else {
      attachments.push(await extractFile(file));
    }
  }
  renderAttachments();
  fileInput.value = "";
}

function readImage(file) {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve({ kind: "image", name: file.name, size: file.size, dataUrl: reader.result });
    reader.onerror = () => reject(new Error("No se pudo leer la imagen " + file.name));
  });
}

async function extractFile(file) {
  const form = new FormData();
  form.append("file", file);
  try {
    const res = await fetch("/api/extract-file", { method: "POST", body: form });
    const payload = await res.json();
    if (!res.ok) {
      const msg = (payload.error && payload.error.message) || "HTTP " + res.status;
      return { kind: "text", name: file.name, size: file.size, text: msg, error: true };
    }
    if (payload.kind === "image") {
      return { kind: "image", name: payload.name, size: payload.size, dataUrl: payload.dataUrl };
    }
    return { kind: "text", name: payload.name, size: payload.size, text: payload.text };
  } catch {
    return { kind: "text", name: file.name, size: file.size, text: "No se pudo procesar el archivo.", error: true };
  }
}

function renderAttachments() {
  if (attachments.length === 0) {
    attachmentsEl.hidden = true;
    attachmentsEl.innerHTML = "";
    return;
  }
  attachmentsEl.hidden = false;
  attachmentsEl.innerHTML = "";
  attachments.forEach((att, index) => {
    const chip = document.createElement("div");
    chip.className = "att-chip" + (att.error ? " error" : "");

    if (att.kind === "image") {
      const img = document.createElement("img");
      img.src = att.dataUrl;
      img.alt = att.name;
      chip.appendChild(img);
    }

    const info = document.createElement("div");
    info.className = "att-info";
    const name = document.createElement("span");
    name.className = "att-name";
    name.textContent = att.name;
    name.title = att.name;
    const meta = document.createElement("span");
    meta.className = "att-size";
    meta.textContent = att.error ? "error" : formatBytes(att.size || 0);
    info.append(name, meta);

    const remove = document.createElement("button");
    remove.className = "btn btn-danger att-remove";
    remove.textContent = "×";
    remove.title = "Quitar adjunto";
    remove.addEventListener("click", () => {
      attachments.splice(index, 1);
      renderAttachments();
    });

    chip.append(info, remove);
    attachmentsEl.appendChild(chip);
  });
}

// ---------------------------------------------------------------------------
// Conversaciones (historial)
// ---------------------------------------------------------------------------

async function refreshConversations() {
  try {
    const res = await fetch("/api/conversations");
    if (!res.ok) return;
    const payload = await res.json();
    conversations = payload.conversations || [];
    renderConversationList();
  } catch {
    // El listado es de conveniencia: si falla, se ignora.
  }
}

function renderConversationList() {
  convListEl.innerHTML = "";
  sidebarEmptyEl.hidden = conversations.length > 0;
  for (const conv of conversations) {
    const li = document.createElement("li");
    const active = conv.id === activeConvId;
    li.className = "conv-item" + (active ? " active" : "");
    li.dataset.id = conv.id;
    li.tabIndex = 0;
    li.setAttribute("role", "button");
    li.setAttribute("aria-label", "Conversación: " + (conv.title || "(sin título)"));
    if (active) li.setAttribute("aria-current", "true");

    const title = document.createElement("span");
    title.className = "conv-title";
    title.textContent = conv.title || "(sin título)";
    title.title = conv.title;

    const meta = document.createElement("span");
    meta.className = "conv-meta";
    meta.textContent = formatWhen(conv.updated_at) + " · " + conv.messages_count + " msg";

    const del = document.createElement("button");
    del.className = "btn btn-danger conv-delete";
    del.textContent = "🗑";
    del.title = "Eliminar conversación";
    del.setAttribute("aria-label", "Eliminar conversación " + (conv.title || "(sin título)"));
    del.addEventListener("click", (event) => {
      event.stopPropagation();
      deleteConversation(conv.id);
    });

    li.append(title, meta, del);
    li.addEventListener("click", () => selectConversation(conv.id));
    li.addEventListener("dblclick", () => renameConversation(conv.id));
    li.addEventListener("keydown", (event) => {
      if (event.key === "Enter" || event.key === " ") {
        event.preventDefault();
        selectConversation(conv.id);
      } else if (event.key === "F2") {
        event.preventDefault();
        renameConversation(conv.id);
      } else if (event.key === "Delete") {
        event.preventDefault();
        deleteConversation(conv.id);
      }
    });
    convListEl.appendChild(li);
  }
}

function newChat() {
  if (busy) return;
  activeConvId = null;
  history = [];
  messagesEl.innerHTML = "";
  clearAttachments();
  updateEmptyState();
  renderConversationList();
  closeDrawers();
  inputEl.focus();
}

async function selectConversation(id) {
  if (busy || id === activeConvId) return;
  try {
    const res = await fetch("/api/conversations/" + encodeURIComponent(id));
    if (!res.ok) return;
    const payload = await res.json();
    const conv = payload.conversation;
    if (!conv || !Array.isArray(conv.messages)) return;

    activeConvId = conv.id;
    history = conv.messages.map((m) => ({ role: m.role, content: m.content }));
    messagesEl.innerHTML = "";
    clearAttachments();
    for (const msg of history) {
      const role = msg.role === "user" ? "user" : "assistant";
      addMessage(role, contentToHTML(msg.content));
    }
    updateEmptyState();
    renderConversationList();
    closeDrawers();
  } catch {
    // silencioso: la UI no debe romperse si falla la carga.
  }
}

async function deleteConversation(id) {
  if (!confirm("¿Eliminar esta conversación?")) return;
  try {
    await fetch("/api/conversations/" + encodeURIComponent(id), { method: "DELETE" });
    if (id === activeConvId) {
      activeConvId = null;
      history = [];
      messagesEl.innerHTML = "";
      clearAttachments();
      updateEmptyState();
    }
    refreshConversations();
  } catch {
    // silencioso
  }
}

async function renameConversation(id) {
  const current = (conversations.find((c) => c.id === id) || {}).title || "";
  const title = prompt("Nuevo título:", current);
  if (title === null) return;
  const trimmed = title.trim();
  if (trimmed === "") return;
  try {
    await fetch("/api/conversations/" + encodeURIComponent(id), {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ title: trimmed }),
    });
    refreshConversations();
  } catch {
    // silencioso
  }
}

// ---------------------------------------------------------------------------
// Drawers (sidebar y configuración) + responsive
// ---------------------------------------------------------------------------

function closeDrawers() {
  const hadOpen = sidebar.classList.contains("open") || configPanel.classList.contains("open");
  sidebar.classList.remove("open");
  configPanel.classList.remove("open");
  sidebarOverlay.hidden = true;
  configOverlay.hidden = true;
  sidebarToggle.setAttribute("aria-expanded", "false");
  configToggle.setAttribute("aria-expanded", "false");
  if (hadOpen && drawerFocusReturnEl) {
    drawerFocusReturnEl.focus();
    drawerFocusReturnEl = null;
  }
}

function openDrawer(drawer, overlay, trigger) {
  drawerFocusReturnEl = trigger;
  drawer.classList.add("open");
  overlay.hidden = false;
  trigger.setAttribute("aria-expanded", "true");
  const first = drawer.querySelector('button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])');
  (first || drawer).focus();
}

function toggleSidebar() {
  if (window.matchMedia(WIDE_QUERY).matches) {
    app.classList.toggle("no-sidebar");
    sidebarToggle.setAttribute("aria-expanded", app.classList.contains("no-sidebar") ? "false" : "true");
    return;
  }
  if (sidebar.classList.contains("open")) {
    closeDrawers();
  } else {
    openDrawer(sidebar, sidebarOverlay, sidebarToggle);
  }
}

function toggleConfig() {
  if (window.matchMedia(WIDE_QUERY).matches) {
    app.classList.toggle("no-config");
    configToggle.setAttribute("aria-expanded", app.classList.contains("no-config") ? "false" : "true");
    return;
  }
  if (configPanel.classList.contains("open")) {
    closeDrawers();
  } else {
    openDrawer(configPanel, configOverlay, configToggle);
  }
}

document.addEventListener("keydown", (event) => {
  if (event.key !== "Escape") return;
  if (sidebar.classList.contains("open") || configPanel.classList.contains("open")) {
    event.preventDefault();
    closeDrawers();
  }
});

// Al pasar de narrow (drawer abierto) a wide, se limpian los estados de drawer.
window.addEventListener("resize", () => {
  if (window.matchMedia(WIDE_QUERY).matches) {
    sidebar.classList.remove("open");
    configPanel.classList.remove("open");
    sidebarOverlay.hidden = true;
    configOverlay.hidden = true;
    drawerFocusReturnEl = null;
  }
});

// ---------------------------------------------------------------------------
// Modelos (selector dinámico)
// ---------------------------------------------------------------------------

let defaultModel = "";

// refreshModels consulta GET /v1/models y popula el <select> con los modelos
// disponibles en todos los upstreams, preseleccionando el modelo por defecto.
async function refreshModels() {
  const select = document.getElementById("model-select");
  if (!select) return;
  const previous = select.value;
  try {
    const res = await fetch("/v1/models");
    if (!res.ok) {
      select.innerHTML = '<option value="">(no disponible)</option>';
      return;
    }
    const payload = await res.json();
    const models = (payload.data || []).map((m) => m.id);
    select.innerHTML = "";
    if (models.length === 0) {
      const opt = document.createElement("option");
      opt.value = "";
      opt.textContent = "(sin modelos)";
      select.appendChild(opt);
      return;
    }
    for (const id of models) {
      const opt = document.createElement("option");
      opt.value = id;
      opt.textContent = ellipsize(id, 48);
      opt.title = id;
      select.appendChild(opt);
    }
    // Preselecciona el default; en un refresh manual conserva la selección.
    const want = previous || defaultModel;
    if (want && models.includes(want)) {
      select.value = want;
    } else if (defaultModel && models.includes(defaultModel)) {
      select.value = defaultModel;
    }
  } catch {
    select.innerHTML = '<option value="">(error de conexión)</option>';
  }
  syncCurrentModel();
}

// ---------------------------------------------------------------------------
// Configuración
// ---------------------------------------------------------------------------

async function loadConfig() {
  try {
    const res = await fetch("/api/config");
    if (!res.ok) {
      setStatus(false, "no se pudo leer la configuración");
      return;
    }
    const payload = await res.json();
    const config = payload.config || {};
    if (payload.defaultModel) {
      defaultModel = payload.defaultModel;
    }

    for (const el of configForm.elements) {
      if (!el.name || el.name.endsWith("_API_KEY")) continue;
      if (el.type === "checkbox") {
        el.checked = config[el.name] === "true";
      } else if (config[el.name] !== undefined) {
        el.value = config[el.name];
      }
    }
    if (payload.apiKeySet) {
      keyBadge.hidden = false;
    }
    await refreshModels();
    setStatus(true, "listo");
  } catch (err) {
    setStatus(false, "error de conexión");
  }
}

async function saveConfig(event) {
  event.preventDefault();
  const values = {};
  const form = new FormData(configForm);
  for (const [key, value] of form.entries()) {
    const el = configForm.elements[key];
    if (el && el.type === "checkbox") {
      values[key] = el.checked ? "true" : "false";
    } else if (key.endsWith("_API_KEY")) {
      const v = String(value || "").trim();
      if (v !== "") values[key] = v; // vacío = conservar la key actual
    } else {
      const v = String(value).trim();
      if (v !== "") values[key] = v;
    }
  }

  try {
    const res = await fetch("/api/config", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ config: values }),
    });
    const body = await res.json();
    saveNote.hidden = false;
    if (res.ok) {
      saveNote.className = "save-note ok";
      saveNote.textContent = body.message || "Guardado. Reinicia el proxy.";
    } else {
      saveNote.className = "save-note err";
      saveNote.textContent = (body.error && body.error.message) || "Error al guardar";
    }
  } catch (err) {
    saveNote.hidden = false;
    saveNote.className = "save-note err";
    saveNote.textContent = "Error de conexión al guardar";
  }
}

// detectModels consulta POST /api/detect-models con la URL del upstream y, si
// el servidor responde, rellena el campo MODELS con los modelos reales
// (separados por coma). No hace falta saber/teclear los nombres exactos.
async function detectModels(prefix) {
  const form = configForm.elements;
  const url = String(form[prefix + "_BASE_URL"].value || "").trim();
  const apiKey = String(form[prefix + "_API_KEY"].value || "").trim();
  const modelsEl = form[prefix + "_MODELS"];
  const statusEl = document.querySelector('[data-status-for="' + prefix + '"]');
  const btn = document.querySelector('.detect-models[data-upstream="' + prefix + '"]');

  if (!url) {
    statusEl.textContent = "Ingresá la URL del servidor primero.";
    statusEl.className = "detect-status err";
    statusEl.hidden = false;
    return;
  }

  statusEl.textContent = "Consultando…";
  statusEl.className = "detect-status";
  statusEl.hidden = false;
  btn.disabled = true;
  btn.textContent = "…";

  try {
    const res = await fetch("/api/detect-models", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ url: url, apiKey: apiKey }),
    });
    const payload = await res.json();
    if (!res.ok || !payload.reachable) {
      const msg = (payload.error && payload.error.message) || payload.error || "HTTP " + res.status;
      statusEl.textContent = "✗ " + msg;
      statusEl.className = "detect-status err";
      return;
    }
    const ids = (payload.models || []).map((m) => m.id);
    if (ids.length === 0) {
      statusEl.textContent = "✓ Conectado, pero el servidor no publica modelos.";
      statusEl.className = "detect-status ok";
      return;
    }
    modelsEl.value = ids.join(", ");
    statusEl.textContent = "✓ " + ids.length + " modelo(s) detectado(s): " + ids.join(", ");
    statusEl.className = "detect-status ok";
  } catch (err) {
    statusEl.textContent = "✗ error de conexión";
    statusEl.className = "detect-status err";
  } finally {
    btn.disabled = false;
    btn.textContent = "Detectar";
  }
}

// ---------------------------------------------------------------------------
// Wiring
// ---------------------------------------------------------------------------

composer.addEventListener("submit", (event) => {
  event.preventDefault();
  if (busy) return;
  sendMessage();
});

inputEl.addEventListener("keydown", (event) => {
  if (event.key === "Enter" && !event.shiftKey) {
    event.preventDefault();
    composer.requestSubmit();
  }
});

inputEl.addEventListener("input", () => {
  inputEl.style.height = "auto";
  inputEl.style.height = Math.min(inputEl.scrollHeight, 160) + "px";
});

attachBtn.addEventListener("click", () => fileInput.click());
fileInput.addEventListener("change", () => onFilesSelected(fileInput.files));

document.getElementById("new-chat").addEventListener("click", newChat);
sidebarToggle.addEventListener("click", toggleSidebar);
configToggle.addEventListener("click", toggleConfig);
sidebarOverlay.addEventListener("click", closeDrawers);
configOverlay.addEventListener("click", closeDrawers);

configForm.addEventListener("submit", saveConfig);

const refreshBtn = document.getElementById("refresh-models");
if (refreshBtn) {
  refreshBtn.addEventListener("click", refreshModels);
}

const modelSelect = document.getElementById("model-select");
const currentModelBtn = document.getElementById("current-model");

function syncCurrentModel() {
  if (!currentModelBtn || !modelSelect) return;
  const v = modelSelect.value;
  currentModelBtn.textContent = v;
  currentModelBtn.title = v ? "Modelo activo: " + v : "Sin modelo seleccionado. Abrí configuración para elegir.";
  currentModelBtn.hidden = !v;
}

if (currentModelBtn) {
  currentModelBtn.addEventListener("click", toggleConfig);
}
if (modelSelect) {
  modelSelect.addEventListener("change", syncCurrentModel);
}

document.querySelectorAll(".detect-models").forEach((btn) => {
  btn.addEventListener("click", () => detectModels(btn.dataset.upstream));
});

// Arranque
updateEmptyState();
loadConfig();
refreshConversations();