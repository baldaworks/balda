"use strict";

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
document.addEventListener("change", function (event) {
  if (event.target instanceof HTMLSelectElement && event.target.name === "transport" &&
      event.target.form?.matches("[data-mcp-create]")) syncMCPTransport();
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
      (path.endsWith("/schedules") && location.pathname.startsWith(path + "/")) ||
      (path.endsWith("/webhooks") && location.pathname.startsWith(path + "/")) ||
      (path.endsWith("/aliases") && location.pathname.startsWith(path + "/"));
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
    syncWebhookDedupe();
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
  syncWebhookDedupe();
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
  document.querySelectorAll("[data-binding-secret], [data-mcp-secret], [data-webhook-secret]").forEach(function (element) {
    if (element instanceof HTMLInputElement) {
      element.value = "";
      element.removeAttribute("value");
    } else if (element instanceof HTMLAnchorElement) {
      element.removeAttribute("href");
    }
  });
});

window.addEventListener("pageshow", function (event) {
  syncMCPTransport();
  syncWebhookDedupe();
  if (event.persisted) clearMCPInstructions();
  if (event.persisted) document.querySelectorAll("[data-mcp-secret]").forEach(function (input) { input.value = ""; });
  if (event.persisted) document.querySelectorAll("[data-webhook-secret]").forEach(function (input) { input.value = ""; input.removeAttribute("value"); });
});

const oneTimeWebhook = document.querySelector("[data-webhook-once]");
if (oneTimeWebhook) history.replaceState(null, "", oneTimeWebhook.dataset.webhookOnce);

document.addEventListener("htmx:afterRequest", function (event) {
  const form = event.detail.elt?.closest("form");
  form?.querySelectorAll("[data-mcp-secret]").forEach(function (input) { input.value = ""; });
});
