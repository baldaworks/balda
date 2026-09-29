"use strict";

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
      (path.endsWith("/access") && location.pathname.startsWith(path + "/users/"));
    link.classList.toggle("active", current);
    if (current) link.setAttribute("aria-current", "page");
    else link.removeAttribute("aria-current");
  });
  const menu = document.querySelector(".mobile-navigation");
  if (menu) menu.open = false;
}

document.addEventListener("htmx:afterSettle", function (event) {
  if (event.detail.target?.id === "main-content") syncNavigation();
});
window.addEventListener("popstate", syncNavigation);

document.addEventListener("htmx:afterSwap", function (event) {
  if (event.detail.target && event.detail.target.id === "main-content") {
    const feedback = document.getElementById("request-error");
    if (feedback) {
      feedback.hidden = true;
      feedback.textContent = "";
    }
    event.detail.target.focus({ preventScroll: true });
  }
});
