"use strict";
(() => {
  const decode = value => Uint8Array.from(atob(value.replace(/-/g, "+").replace(/_/g, "/")), c => c.charCodeAt(0));
  const encode = value => value == null ? null : btoa(String.fromCharCode(...new Uint8Array(value))).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  const form = document.querySelector("form[data-webauthn]");
  if (!form) return;
  const button = form.querySelector("[data-webauthn-button]");
  const status = form.querySelector("[data-webauthn-status]");
  if (!window.PublicKeyCredential || !navigator.credentials || !window.isSecureContext) {
    status.textContent = "Passkeys are unavailable. Use a supported browser with JavaScript at the configured HTTPS domain or localhost.";
    return;
  }
  button.disabled = false;
  let busy = false;
  form.addEventListener("submit", async event => {
    event.preventDefault();
    if (busy) return;
    busy = true;
    button.disabled = true;
    status.textContent = "Waiting for your authenticator…";
    try {
      const registration = form.dataset.registration === "true";
      const options = JSON.parse(form.dataset.options).publicKey;
      options.challenge = decode(options.challenge);
      if (options.user) options.user.id = decode(options.user.id);
      for (const field of ["allowCredentials", "excludeCredentials"]) {
        if (options[field]) options[field] = options[field].map(item => ({...item, id: decode(item.id)}));
      }
      const credential = await navigator.credentials[registration ? "create" : "get"]({publicKey: options});
      if (!credential) throw new Error("cancelled");
      const response = credential.response;
      const result = {id: credential.id, rawId: encode(credential.rawId), type: credential.type, clientExtensionResults: credential.getClientExtensionResults(), response: {clientDataJSON: encode(response.clientDataJSON)}};
      if (registration) {
        result.response.attestationObject = encode(response.attestationObject);
        result.response.transports = response.getTransports ? response.getTransports() : [];
      } else {
        result.response.authenticatorData = encode(response.authenticatorData);
        result.response.signature = encode(response.signature);
        result.response.userHandle = encode(response.userHandle);
      }
      form.elements.credential.value = JSON.stringify(result);
      HTMLFormElement.prototype.submit.call(form);
    } catch (_) {
      status.textContent = "Verification was cancelled or failed. Your second-factor setting has not changed. Try again or cancel.";
      busy = false;
      button.disabled = false;
    }
  });
  window.addEventListener("pageshow", event => {
    if (event.persisted) {
      form.elements.credential.value = "";
      status.textContent = "This verification page has expired. Cancel and start again.";
      button.disabled = true;
    }
  });
})();
