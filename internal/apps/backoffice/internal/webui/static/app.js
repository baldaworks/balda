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

document.addEventListener("htmx:afterSwap", function (event) {
  if (event.detail.target && event.detail.target.id === "main-content") {
    event.detail.target.focus({ preventScroll: true });
  }
});
