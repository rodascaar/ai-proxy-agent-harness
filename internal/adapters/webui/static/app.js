"use strict";

const messagesEl = document.getElementById("messages");
const composer = document.getElementById("composer");
const inputEl = document.getElementById("input");
const sendBtn = document.getElementById("send");
const statusDot = document.getElementById("status-dot");
const statusText = document.getElementById("status-text");
const configForm = document.getElementById("config-form");
const keyBadge = document.getElementById("key-badge");
const saveNote = document.getElementById("save-note");

let history = [];
let busy = false;

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

// markdownLite: escapa HTML, detecta bloques de código (```...```), inline
// code (`x`), negrita (**x**) y saltos de línea. Suficiente para un chat de
// pruebas sin arrastrar una librería.
function markdownLite(text) {
  const escaped = escapeHtml(text);
  const parts = escaped.split(/```/);
  let out = "";
  for (let i = 0; i < parts.length; i++) {
    if (i % 2 === 1) {
      out += "<pre><code>" + parts[i] + "</code></pre>";
    } else {
      out += parts[i]
        .replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>")
        .replace(/`([^`]+)`/g, "<code>$1</code>")
        .replace(/\n/g, "<br>");
    }
  }
  return out;
}

function scrollToBottom() {
  messagesEl.scrollTop = messagesEl.scrollHeight;
}

function setStatus(ok, text) {
  statusDot.className = "dot " + (ok ? "ok" : "err");
  statusText.textContent = text;
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
  scrollToBottom();
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
  scrollToBottom();

  return {
    wrap,
    reasoning,
    reasoningPre: reasoning.querySelector("pre"),
    content,
    contentText: "",
    reasoningText: "",
  };
}

function renderInto(el, text) {
  el.innerHTML = markdownLite(text);
  scrollToBottom();
}

async function sendMessage(text) {
  history.push({ role: "user", content: text });
  addMessage("user", markdownLite(text));

  const assistant = createAssistant();
  const model = document.querySelector('[name="UPSTREAM_MODEL"]').value || "";

  busy = true;
  sendBtn.disabled = true;
  inputEl.disabled = true;

  try {
    const res = await fetch("/v1/chat/completions", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
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

    if (assistant.contentText.trim() !== "") {
      history.push({ role: "assistant", content: assistant.contentText });
    }
  } catch (err) {
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
      assistant.content.innerHTML += markdownLite("\n\n**Error:** " + chunk.error.message);
      scrollToBottom();
      continue;
    }
    if (!chunk.choices || chunk.choices.length === 0) continue;

    const delta = chunk.choices[0].delta || {};
    if (delta.reasoning_content) {
      assistant.reasoning.style.display = "";
      assistant.reasoningText += delta.reasoning_content;
      assistant.reasoningPre.textContent = assistant.reasoningText;
      scrollToBottom();
    }
    if (delta.content) {
      assistant.contentText += delta.content;
      renderInto(assistant.content, assistant.contentText);
    }
    if (delta.tool_calls && delta.tool_calls.length > 0) {
      const names = delta.tool_calls.map((tc) => tc.function && tc.function.name).filter(Boolean);
      if (names.length > 0) {
        assistant.contentText += "\n\n_[Herramienta: " + names.join(", ") + "]_";
        renderInto(assistant.content, assistant.contentText);
      }
    }
  }
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

    for (const el of configForm.elements) {
      if (!el.name || el.name === "UPSTREAM_API_KEY") continue;
      if (el.type === "checkbox") {
        el.checked = config[el.name] === "true";
      } else if (config[el.name] !== undefined) {
        el.value = config[el.name];
      }
    }
    if (payload.apiKeySet) {
      keyBadge.hidden = false;
    }
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
    } else if (key === "UPSTREAM_API_KEY") {
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

// ---------------------------------------------------------------------------
// Wiring
// ---------------------------------------------------------------------------

composer.addEventListener("submit", (event) => {
  event.preventDefault();
  const text = inputEl.value.trim();
  if (text === "" || busy) return;
  inputEl.value = "";
  inputEl.style.height = "auto";
  sendMessage(text);
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

configForm.addEventListener("submit", saveConfig);

loadConfig();
