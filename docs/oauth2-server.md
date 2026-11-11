# OAuth2 server (single sign-on)

Any auth collection can act as an OAuth2 / OIDC server (**Collection → Options → OAuth2 server**), so other apps can sign its users in with a single token:

- authorization code flow with optional PKCE (S256), `client_secret_basic` or `client_secret_post`, OIDC `id_token`, `userinfo` and discovery;
- the issued access token is the collection's regular auth token;
- a built-in sign-in page with password, passkey or any OAuth2 provider enabled on the collection, a remembered sign-in cookie (configurable duration), an account chooser (continue, switch account, sign out), `prompt=none|login|select_account|consent`, and `logout`;
- apps that share the database accept the same token. Other PgBase apps can use it as an `oidc` provider through the normal popup + realtime flow, which lets all of them share one passkey domain.
- the collection's own frontends can use the sign-in page in a popup with the SDK's `authWithOAuth2({ provider: "sso" })` and no client setup (see below).

The sign-in page shows a **Continue with …** button for every provider under the collection's **Options → OAuth2**, and follows that list as providers are added or removed. The provider must have `https://<sign-in server>/api/oauth2-redirect` registered as a redirect URL, the same one the JS SDK flow uses.

Endpoints live under `/api/collections/{collection}/oauth2-server/` (`authorize`, `token`, `userinfo`, `logout`, `.well-known/openid-configuration`).

## Sign in with a popup

A frontend can sign users in without ever handling a password, with the regular JS SDK call. A popup opens on the PgBase sign-in page, the user signs in there with a password, a passkey or an OAuth2 provider, and the call returns the auth token:

```js
import PocketBase from "pocketbase";

const pb = new PocketBase("https://pb.example.com");

document.getElementById("login").addEventListener("click", async () => {
    const authData = await pb.collection("users").authWithOAuth2({ provider: "sso" });

    console.log(authData.record.id, authData.token);
    console.log(pb.authStore.isValid); // true, the token is stored for later requests
});
```

There is nothing extra to install: `sso` is a built-in provider that `auth-methods` lists for the allowed frontends, and it uses the same popup + realtime flow as any other OAuth2 provider.

### Reacting to a closed popup

If the user presses **Cancel** on the sign-in page, the call rejects (`OAuth2 redirect error … access_denied`) and the popup closes, so a loading state ends in `.catch`/`.finally` on its own.

If the user closes the popup window instead, the SDK isn't told and the call stays pending. To reset the button, open the popup yourself and watch it:

```js
const LOGIN_KEY = "sso-login";
let attempt = 0;

// not async: the popup must be opened directly in the click (Safari)
loginButton.addEventListener("click", () => {
    const current = ++attempt;
    setLoading(true);

    const popup = window.open("", "sso-login", "width=480,height=640");

    // the popup can also look "closed" while the sign-in goes on: right after a successful
    // sign-in, and while the user is on some OAuth2 provider pages. So only reset the button
    // and keep the call pending; if the sign-in still completes, .then() runs as usual.
    const watcher = setInterval(() => {
        if (!popup || popup.closed) {
            clearInterval(watcher);
            setTimeout(() => {
                if (current === attempt) {
                    setLoading(false);
                }
            }, 3000);
        }
    }, 500);

    pb.collection("users")
        .authWithOAuth2({
            provider: "sso",
            // a new click with the same key cancels the previous pending call
            requestKey: LOGIN_KEY,
            urlCallback: (url) => {
                popup.location.href = url;
            },
        })
        .then((authData) => {
            onSignedIn(authData);
        })
        .catch((err) => {
            if (err.isAbort) {
                return; // replaced by a newer click
            }
            showError("Sign-in was cancelled or did not complete.");
        })
        .finally(() => {
            clearInterval(watcher);
            if (current === attempt) {
                setLoading(false);
            }
            try {
                popup?.close();
            } catch {}
        });
});
```

`pb.cancelRequest(LOGIN_KEY)` also ends a pending call right away (it rejects with `err.isAbort`), for example from a separate "Stop" button.

**Setup.** In the dashboard, open the auth collection → **Options → OAuth2 server** and turn on **Popup sign-in**. No client, secret or OAuth2 provider is needed. List the address of every frontend that may use it (for example `https://app.example.com` or `http://localhost:5173`) in the allowed origins. Only the Application URL (and the server itself on localhost) is allowed implicitly. Set the **Application URL** to the public address of PgBase.

Notes:

- The provider is listed only to the allowed origins (the SDK sends the `Origin` header), and the code can be redeemed only from the origin it was issued for.
- Everything else of `authWithOAuth2` works as usual (`urlCallback`, `createData` is ignored, the auth store is updated). It needs the realtime connection, which must reach the same PgBase instance that serves the sign-in page, so behind several instances use sticky routing.
- The sign-in page remembers the user, so the next popup offers **Continue as…**, **Use another account** and **Sign out** instead of asking for the password again. To end the remembered sign-in, open `https://pb.example.com/api/collections/users/oauth2-server/logout`.
- `pb.authStore.clear()` forgets the token in the frontend only.
- Signing in with a provider inside the popup works even though the provider's pages may cut the popup off from your page, because the result comes over realtime.
- `prompt=none` (silent sign-in) is not supported by the popup, so the user is always asked.
- Accounts that require MFA can't sign in this way, because the sign-in page has no second step.

