// Popup sign-in helper for the PocketBase JS SDK.
//
// Opens the hosted sign-in page of an auth collection in a popup and
// resolves with the regular auth data ({ token, record }) after the user
// has signed in there. The page that calls it never sees the password.
//
//   import { authWithPopup } from "https://pb.example.com/api/collections/users/oauth2-server/popup.js";
//
//   loginButton.addEventListener("click", async () => {
//       const authData = await authWithPopup(pb, "users");
//   });
//
// Call it directly from a click handler, otherwise the browser may block the popup.
//
// Options:
//   prompt  "login" to always ask for the credentials (skips the "Continue as" step)
//   width   popup width  (default 480)
//   height  popup height (default 640)
export async function authWithPopup(pb, collection = "users", options = {}) {
    const path = "/api/collections/" + encodeURIComponent(collection);
    const base = new URL(pb.buildURL(path), window.location.href);

    const width = options.width || 480;
    const height = options.height || 640;
    const left = Math.max(0, window.screenX + (window.outerWidth - width) / 2);
    const top = Math.max(0, window.screenY + (window.outerHeight - height) / 2);

    // opened before any await, otherwise it is not tied to the user click anymore
    const popup = window.open(
        "",
        "pocketbase_popup",
        `popup=yes,width=${width},height=${height},left=${left},top=${top}`,
    );
    if (!popup) {
        throw new Error("The sign-in popup was blocked by the browser.");
    }

    try {
        const state = randomString(24);
        const codeVerifier = randomString(48);
        const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(codeVerifier));

        const params = new URLSearchParams({
            response_type: "code",
            client_id: "@popup",
            redirect_uri: window.location.origin,
            state: state,
            code_challenge: base64URL(new Uint8Array(digest)),
            code_challenge_method: "S256",
        });
        if (options.prompt) {
            params.set("prompt", options.prompt);
        }

        popup.location.href = base.href + "/oauth2-server/authorize?" + params;

        const code = await new Promise((resolve, reject) => {
            function finish(callback, value) {
                clearInterval(timer);
                window.removeEventListener("message", onMessage);
                callback(value);
            }

            function onMessage(event) {
                const data = event.data || {};
                if (
                    event.origin !== base.origin
                    || event.source !== popup
                    || data.type !== "pocketbase:popup"
                    || data.state !== state
                ) {
                    return;
                }

                if (data.code) {
                    finish(resolve, data.code);
                } else {
                    finish(reject, new Error(data.errorDescription || data.error || "The sign-in failed."));
                }
            }

            const timer = setInterval(() => {
                if (popup.closed) {
                    finish(reject, new Error("The sign-in popup was closed."));
                }
            }, 500);

            window.addEventListener("message", onMessage);
        });

        const authData = await pb.send(path + "/auth-with-popup", {
            method: "POST",
            body: { code: code, codeVerifier: codeVerifier },
        });

        pb.authStore.save(authData.token, authData.record);

        return authData;
    } finally {
        popup.close();
    }
}

function randomString(bytes) {
    return base64URL(crypto.getRandomValues(new Uint8Array(bytes)));
}

function base64URL(bytes) {
    let str = "";
    for (const b of bytes) {
        str += String.fromCharCode(b);
    }
    return btoa(str).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}
