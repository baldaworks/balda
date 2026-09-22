"use strict";

document.addEventListener("htmx:afterSwap", function (event) {
  if (event.detail.target && event.detail.target.id === "main-content") {
    event.detail.target.focus({ preventScroll: true });
  }
});
