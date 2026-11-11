"use strict";

function syncPluginMarketplaceSource() {
  document.querySelectorAll("form[data-plugin-marketplace-create]").forEach(function (form) {
    const source = form.querySelector("[data-plugin-source]")?.value.trim() || "";
    const git = /^[a-z][a-z0-9+.-]*:\/\//i.test(source) || /^[^/\s:]+@?[^/\s:]*:.+/.test(source);
    const fields = form.querySelector("[data-plugin-git-fields]");
    if (fields) { fields.hidden = !git; fields.disabled = !git; }
  });
}
syncPluginMarketplaceSource();
document.addEventListener("input", function (event) {
  if (event.target instanceof HTMLInputElement && event.target.matches("[data-plugin-source]")) syncPluginMarketplaceSource();
});

function syncMCPTransport() {
  document.querySelectorAll("form[data-mcp-create]").forEach(function (form) {
    const transport = form.querySelector('[name="transport"]').value;
    form.querySelectorAll("fieldset[data-mcp-transports]").forEach(function (fieldset) {
      const active = fieldset.dataset.mcpTransports.split(" ").includes(transport);
      fieldset.hidden = !active;
      fieldset.disabled = !active;
    });
  });
}
syncMCPTransport();
function syncWebhookDedupe() {
  document.querySelectorAll("select[data-webhook-dedupe]").forEach(function (select) {
    const field = select.form?.querySelector("[data-webhook-dedupe-header]");
    const input = field?.querySelector("input");
    if (!field || !input) return;
    const active = select.value === "header";
    field.hidden = !active;
    input.disabled = !active;
    input.required = active;
  });
}
syncWebhookDedupe();
function syncWebhookURL() {
  document.querySelectorAll("input[data-webhook-url-prefix]").forEach(function (field) {
    const name = field.form?.querySelector("input[data-webhook-url-name]")?.value || "";
    field.value = /^[a-z0-9][a-z0-9_-]{0,63}$/.test(name) ? field.dataset.webhookUrlPrefix + name : "";
  });
}
syncWebhookURL();
document.addEventListener("input", function (event) {
  if (event.target instanceof HTMLInputElement && event.target.matches("[data-webhook-url-name]")) syncWebhookURL();
});
document.addEventListener("change", function (event) {
  if (event.target instanceof HTMLSelectElement && event.target.name === "transport" &&
      event.target.form?.matches("[data-mcp-create]")) syncMCPTransport();
  syncPluginMarketplaceSource();
  if (event.target instanceof HTMLSelectElement && event.target.matches("[data-webhook-dedupe]")) syncWebhookDedupe();
});

function closeMobileSidebar() {
  const sidebar = document.querySelector(".app-sidebar");
  if (sidebar && innerWidth < 992) window.adminlte.PushMenu.getOrCreateInstance(sidebar).collapse();
}
function syncSidebar() {
  const sidebar = document.querySelector(".app-sidebar");
  const toggle = document.querySelector(".sidebar-toggle");
  if (!sidebar || !toggle) return;
  const open = innerWidth < 992 ? document.body.classList.contains("sidebar-open") : !document.body.classList.contains("sidebar-collapse");
  toggle.setAttribute("aria-expanded", String(open));
  sidebar.inert = !open;
  const overlayOpen = open && innerWidth < 992;
  for (const selector of [".app-main", ".app-footer", ".viewer-actions"]) {
    const element = document.querySelector(selector);
    if (element) element.inert = overlayOpen;
  }
  if (!open && sidebar.contains(document.activeElement)) toggle.focus();
}
document.addEventListener("opened.lte.push-menu", function () {
  syncSidebar();
  if (innerWidth < 992) document.querySelector(".sidebar-menu a")?.focus();
});
document.addEventListener("collapsed.lte.push-menu", syncSidebar);
document.addEventListener("DOMContentLoaded", syncSidebar);
window.addEventListener("resize", function () { requestAnimationFrame(syncSidebar); });
document.addEventListener("keydown", function (event) {
  if (innerWidth >= 992 || !document.body.classList.contains("sidebar-open")) return;
  if (event.key === "Escape") {
    closeMobileSidebar(); document.querySelector(".sidebar-toggle")?.focus(); event.preventDefault();
  } else if (event.key === "Tab") {
    const controls = [...document.querySelectorAll(".sidebar-menu a")];
    const first = controls[0], last = controls[controls.length - 1];
    if (event.shiftKey && document.activeElement === first) { last.focus(); event.preventDefault(); }
    if (!event.shiftKey && document.activeElement === last) { first.focus(); event.preventDefault(); }
  }
});

const refreshForm = document.querySelector("form[data-auto-refresh]");
if (refreshForm) {
  refreshForm.requestSubmit();
}

document.addEventListener("htmx:responseError", function (event) {
  if (event.detail.xhr.status === 401) {
    const refreshURL = new URL(document.body.dataset.refreshPath, location.origin);
    let returnTo = location.pathname + location.search;
    const request = event.detail.requestConfig;
    if (request && request.verb === "get") {
      const target = new URL(request.path, location.href);
      if (target.origin === location.origin) {
        returnTo = target.pathname + target.search;
      }
    }
    refreshURL.searchParams.set("return_to", returnTo);
    location.assign(refreshURL);
  }
});

document.addEventListener("htmx:beforeSwap", function (event) {
  const response = event.detail.xhr;
  const contentType = response.getResponseHeader("Content-Type") || "";
  const requestPath = event.detail.requestConfig?.path;
  if (response.status >= 400 && response.status !== 401 && requestPath &&
      new URL(requestPath, location.href).origin === location.origin &&
      event.detail.target?.id === "main-content" && contentType.toLowerCase().startsWith("text/html")) {
    const responseDocument = new DOMParser().parseFromString(response.responseText, "text/html");
    const main = responseDocument.querySelector("main#main-content");
    const message = main?.querySelector('[role="alert"]');
    const heading = main?.querySelector("h1");
    const explanation = heading?.nextElementSibling;
    const feedback = document.getElementById("request-error");
    if (!feedback || !main) return;
    if (event.detail.target.hasAttribute("data-agent-hooks") && main.hasAttribute("data-agent-hooks")) {
      event.detail.shouldSwap = true;
      event.detail.isError = false;
      return;
    }
    if (event.detail.target.hasAttribute("data-fact-memory") && main.hasAttribute("data-fact-memory")) {
      event.detail.shouldSwap = true;
      event.detail.isError = false;
      return;
    }
    if ((event.detail.target.hasAttribute("data-skills") && main.hasAttribute("data-skills")) || (event.detail.target.hasAttribute("data-plugins") && main.hasAttribute("data-plugins"))) {
      event.detail.shouldSwap = true;
      event.detail.isError = false;
      return;
    }
    if (event.detail.requestConfig?.elt?.closest(".binding-panel") && main.querySelector(".binding-panel")) {
      event.detail.shouldSwap = true;
      event.detail.isError = false;
      return;
    }
    feedback.textContent = message?.textContent?.trim() ||
      [heading?.textContent?.trim(), explanation?.textContent?.trim()].filter(Boolean).join(" ") ||
      "The request could not be completed.";
    feedback.hidden = false;
    feedback.focus({ preventScroll: true });
    event.detail.shouldSwap = false;
    event.detail.isError = false;
  }
});

function syncNavigation() {
  document.querySelectorAll("[data-nav-link]").forEach(function (link) {
    const path = new URL(link.href).pathname;
    const current = location.pathname === path ||
      (path.endsWith("/access") && location.pathname.startsWith(path + "/users/")) ||
      (path.endsWith("/mcp") && location.pathname.startsWith(path + "/")) ||
      (path.endsWith("/skills") && location.pathname.startsWith(path + "/")) ||
      (path.endsWith("/plugins") && location.pathname.startsWith(path + "/")) ||
      (path.endsWith("/schedules") && location.pathname.startsWith(path + "/")) ||
      (path.endsWith("/webhooks") && location.pathname.startsWith(path + "/")) ||
      (path.endsWith("/aliases") && location.pathname.startsWith(path + "/")) ||
      (path.endsWith("/chat-sessions") && location.pathname.startsWith(path + "/")) ||
      (path.endsWith("/memory") && location.pathname.startsWith(path + "/")) ||
      (path.endsWith("/hooks") && location.pathname.startsWith(path + "/"));
    link.classList.toggle("active", current);
    if (current) link.setAttribute("aria-current", "page");
    else link.removeAttribute("aria-current");
  });
  closeMobileSidebar();
}

document.addEventListener("htmx:afterSettle", function (event) {
  if (event.detail.target?.id === "main-content") syncNavigation();
});
window.addEventListener("popstate", syncNavigation);

document.addEventListener("htmx:afterSwap", function (event) {
  if (event.detail.target && event.detail.target.id === "main-content") {
    syncMCPTransport();
    syncPluginMarketplaceSource();
    syncWebhookDedupe();
    syncWebhookURL();
    const feedback = document.getElementById("request-error");
    if (feedback) {
      feedback.hidden = true;
      feedback.textContent = "";
    }
    closeMobileSidebar();
    document.getElementById("main-content")?.focus({ preventScroll: true });
  }
});

document.addEventListener("htmx:historyRestore", function () {
  syncMCPTransport();
  syncPluginMarketplaceSource();
  syncWebhookDedupe();
  syncWebhookURL();
  syncNavigation();
  document.getElementById("main-content")?.focus({ preventScroll: true });
});

document.addEventListener("click", async function (event) {
  const button = event.target.closest("[data-binding-copy]");
  if (!button) return;
  const field = document.getElementById(button.dataset.bindingCopy);
  if (!field || !field.matches("input[data-binding-secret], input[data-webhook-secret]")) return;
  field.select();
  const status = button.closest("[data-binding-reveal]")?.querySelector("[data-binding-copy-status]");
  try {
    await navigator.clipboard.writeText(field.value);
    if (status) status.textContent = "Copied.";
  } catch {
    if (status) status.textContent = "Selected. Copy with Ctrl/Cmd+C.";
  }
});

function clearMCPInstructions() {
  document.querySelectorAll("[data-mcp-transient]").forEach(function (element) { element.replaceChildren(); });
}

window.addEventListener("pagehide", function () {
  clearMCPInstructions();
  document.querySelectorAll("[data-binding-secret], [data-mcp-secret], [data-webhook-secret], [data-plugin-source], [data-private-source]").forEach(function (element) {
    if (element instanceof HTMLInputElement) {
      element.value = "";
      element.removeAttribute("value");
    } else if (element instanceof HTMLAnchorElement) {
      element.removeAttribute("href");
    }
  });
});

window.addEventListener("pageshow", function (event) {
  if (event.persisted) document.querySelectorAll("[data-plugin-source], [data-private-source]").forEach(function (input) { input.value = ""; });
  syncMCPTransport();
  syncPluginMarketplaceSource();
  syncWebhookDedupe();
  syncWebhookURL();
  if (event.persisted) clearMCPInstructions();
  if (event.persisted) document.querySelectorAll("[data-mcp-secret]").forEach(function (input) { input.value = ""; });
  if (event.persisted) document.querySelectorAll("[data-webhook-secret]").forEach(function (input) { input.value = ""; input.removeAttribute("value"); });
});

const oneTimeWebhook = document.querySelector("[data-webhook-once]");
if (oneTimeWebhook) history.replaceState(null, "", oneTimeWebhook.dataset.webhookOnce);

document.addEventListener("htmx:afterRequest", function (event) {
  const form = event.detail.elt?.closest("form");
  form?.querySelectorAll("[data-mcp-secret], [data-plugin-source], [data-private-source]").forEach(function (input) { input.value = ""; });
  syncPluginMarketplaceSource();
});

// Consent belongs to one invocation, never to the form or HTMX history.
const confirmationElement = document.getElementById("action-confirmation");
const confirmationModal = confirmationElement && window.bootstrap?.Modal.getOrCreateInstance(confirmationElement);
let pendingConfirmation = null;
let acceptedConfirmation = null;
const nativeConfirmation = new WeakSet();
const enhancedConfirmation = new WeakMap();
const confirmationInFlight = new WeakSet();

function confirmationError() {
  const feedback = document.getElementById("request-error");
  if (!feedback) return;
  feedback.textContent = "Confirmation is unavailable. Reload the page and try again.";
  feedback.hidden = false;
  feedback.focus();
}
// Keep the Access page's capability projection separate from the presenter.
function requiresConfirmation(form) {
  if (!form.hasAttribute("data-confirm-bot-role")) return true;
  const capability = (role, status) => status !== "active" ? "none" : role === "administrator" ? "owner" : "collaborator";
  return capability(form.dataset.confirmBotRole, form.dataset.confirmBotStatus) !== capability(form.elements.role.value, form.elements.status.value);
}
function confirmAction(form, submitter, resume) {
  if (pendingConfirmation || acceptedConfirmation || confirmationInFlight.has(form)) return;
  const consent = form.querySelector("input[data-confirm-consent]");
  if (!confirmationModal || !consent?.name || !form.dataset.confirmTitle || !form.dataset.confirmMessage || !form.dataset.confirmAction) {
    confirmationError();
    return;
  }
  if (!form.reportValidity()) return;
  closeMobileSidebar();
  syncSidebar();
  const invocation = { form, submitter, consent, resume, action: form.action, values: new URLSearchParams(new FormData(form)).toString() };
  pendingConfirmation = invocation;
  confirmationElement.querySelector("#action-confirmation-title").textContent = form.dataset.confirmTitle;
  confirmationElement.querySelector("#action-confirmation-body").textContent = form.dataset.confirmMessage;
  confirmationElement.querySelector("[data-confirm-accept]").textContent = form.dataset.confirmAction;
  confirmationModal.show();
}
function cancelConfirmation() {
  pendingConfirmation = null;
  acceptedConfirmation = null;
  confirmationModal?.hide();
}
confirmationElement?.addEventListener("shown.bs.modal", function () {
  confirmationElement.querySelector("[data-confirm-cancel]").focus();
});
confirmationElement?.querySelector("[data-confirm-accept]").addEventListener("click", function () {
  if (!pendingConfirmation) return;
  acceptedConfirmation = pendingConfirmation;
  pendingConfirmation = null;
  confirmationModal.hide();
});
confirmationElement?.addEventListener("hide.bs.modal", function () {
  // Bootstrap also dismisses through Escape, backdrop and close controls.
  if (!acceptedConfirmation && pendingConfirmation) {
    acceptedConfirmation = { ...pendingConfirmation, resume: null };
    pendingConfirmation = null;
  }
});
confirmationElement?.addEventListener("hidden.bs.modal", function () {
  const invocation = acceptedConfirmation;
  acceptedConfirmation = null;
  if (!invocation) return;
  const { form, submitter, consent } = invocation;
  const usable = form.isConnected && (!submitter || submitter.isConnected && submitter.form === form && !submitter.disabled);
  if (usable) (submitter || form.querySelector('button[type="submit"], input[type="submit"]'))?.focus();
  else document.getElementById("main-content")?.focus({ preventScroll: true });
  if (!invocation.resume || !usable || form.action !== invocation.action || new URLSearchParams(new FormData(form)).toString() !== invocation.values) return;
  if (!form.reportValidity()) return;
  consent.disabled = false;
  try { invocation.resume(invocation); }
  finally { consent.disabled = true; }
});
document.addEventListener("submit", function (event) {
  const form = event.target;
  if (!(form instanceof HTMLFormElement) || !form.matches("[data-confirm-title]")) return;
  if (nativeConfirmation.delete(form) || event.defaultPrevented || !requiresConfirmation(form)) return;
  event.preventDefault();
  confirmAction(form, event.submitter, function () {
    confirmationInFlight.add(form);
    nativeConfirmation.add(form);
    try { form.requestSubmit(event.submitter || undefined); }
    finally { nativeConfirmation.delete(form); }
  });
});
document.addEventListener("htmx:confirm", function (event) {
  const form = event.detail.elt?.closest("form[data-confirm-title]");
  if (!form || event.detail.verb === "get" || !requiresConfirmation(form)) return;
  event.preventDefault();
  const trigger = event.detail.triggeringEvent;
  const submitter = trigger?.submitter || trigger?.target?.closest('button[type="submit"], input[type="submit"]');
  confirmAction(form, submitter, function (invocation) {
    enhancedConfirmation.set(form, invocation);
    confirmationInFlight.add(form);
    try { event.detail.issueRequest(true); }
    finally { enhancedConfirmation.delete(form); }
  });
});
document.addEventListener("htmx:configRequest", function (event) {
  const form = event.detail.elt?.closest("form");
  const invocation = enhancedConfirmation.get(form);
  if (!invocation) return;
  event.detail.parameters[invocation.consent.name] = invocation.consent.value;
  if (invocation.submitter?.name) event.detail.parameters[invocation.submitter.name] = invocation.submitter.value;
});
document.addEventListener("htmx:afterRequest", function (event) {
  const form = event.detail.elt?.closest("form");
  if (form) confirmationInFlight.delete(form);
});
document.addEventListener("htmx:beforeCleanupElement", function (event) {
  const form = (pendingConfirmation || acceptedConfirmation)?.form;
  if (form && event.detail.elt?.contains(form)) cancelConfirmation();
});
document.addEventListener("htmx:historyRestore", cancelConfirmation);
window.addEventListener("popstate", cancelConfirmation);
window.addEventListener("pagehide", cancelConfirmation);
window.addEventListener("pageshow", function (event) {
  if (event.persisted) cancelConfirmation();
  document.querySelectorAll("form[data-confirm-title]").forEach(form => confirmationInFlight.delete(form));
});
