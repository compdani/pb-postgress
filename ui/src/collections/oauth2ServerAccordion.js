export function oauth2ServerAccordion(collection) {
    const uniqueId = "oauth2Server_" + app.utils.randomString();

    const data = store({
        get config() {
            if (!collection.oauth2Server) {
                collection.oauth2Server = {
                    enabled: false,
                    codeDuration: 120,
                    sessionDuration: 2592000,
                    clients: [],
                    popup: { enabled: false, origins: [] },
                };
            }

            if (!collection.oauth2Server.popup) {
                collection.oauth2Server.popup = { enabled: false, origins: [] };
            }

            if (!Array.isArray(collection.oauth2Server.clients)) {
                collection.oauth2Server.clients = [];
            }

            return collection.oauth2Server;
        },
        get baseURL() {
            return app.pb.buildURL(
                "/api/collections/" + encodeURIComponent(collection.name || "COLLECTION") + "/oauth2-server",
            );
        },
    });

    // older collections can have no code duration saved, which is required once enabled
    function ensureDefaults() {
        if (!data.config.codeDuration) {
            data.config.codeDuration = 120;
        }
    }

    function clearClientErrors(index) {
        app.utils.deleteByPath(app.store.errors, "oauth2Server.clients." + index);
    }

    function addClient() {
        data.config.clients.push({
            id: "",
            name: "",
            secret: app.utils.randomSecret(40),
            redirectURIs: [],
        });

        if (data.config.clients.length == 1) {
            data.config.enabled = true;
            ensureDefaults();
        }
    }

    function clientCard(client, index) {
        const clientId = uniqueId + ".clients." + index;
        const prefix = "oauth2Server.clients." + index;

        return t.div(
            { className: "col-sm-12" },
            t.div(
                { className: "grid sm oauth2-server-client" },
                t.div(
                    { className: "col-sm-6" },
                    t.div(
                        { className: "field" },
                        t.label({ htmlFor: clientId + ".id", textContent: "Client ID" }),
                        t.input({
                            id: clientId + ".id",
                            name: prefix + ".id",
                            type: "text",
                            required: true,
                            spellcheck: false,
                            placeholder: "eg. event-pocket",
                            value: () => client.id || "",
                            oninput: (e) => (client.id = e.target.value.trim()),
                        }),
                    ),
                ),
                t.div(
                    { className: "col-sm-6" },
                    t.div(
                        { className: "field" },
                        t.label({ htmlFor: clientId + ".name", textContent: "Display name" }),
                        t.input({
                            id: clientId + ".name",
                            name: prefix + ".name",
                            type: "text",
                            placeholder: "Shown on the sign-in page",
                            value: () => client.name || "",
                            oninput: (e) => (client.name = e.target.value),
                        }),
                    ),
                ),
                t.div(
                    { className: "col-sm-12" },
                    t.div(
                        { className: "field" },
                        t.label({ htmlFor: clientId + ".secret", textContent: "Client secret" }),
                        t.input({
                            id: clientId + ".secret",
                            name: prefix + ".secret",
                            type: "text",
                            autocomplete: "off",
                            spellcheck: false,
                            required: () => typeof client.secret != "undefined",
                            placeholder: () => (typeof client.secret == "undefined" ? "* * * * * * (stored)" : ""),
                            value: () => client.secret || "",
                            oninput: (e) => (client.secret = e.target.value.trim()),
                        }),
                        t.button(
                            {
                                type: "button",
                                className: "btn secondary transparent sm m-t-5",
                                onclick: () => (client.secret = app.utils.randomSecret(40)),
                            },
                            t.i({ className: "ri-refresh-line", ariaHidden: true }),
                            t.span({ className: "txt" }, "Generate new secret"),
                        ),
                    ),
                    t.div(
                        { className: "field-help" },
                        () => {
                            if (typeof client.secret == "undefined") {
                                return "The stored secret is never shown again. Leave it as is to keep it.";
                            }

                            return "Copy the secret now. After saving it won't be shown again.";
                        },
                    ),
                ),
                t.div(
                    { className: "col-sm-12" },
                    t.div(
                        { className: "field" },
                        t.label({ htmlFor: clientId + ".redirectURIs", textContent: "Redirect URIs (one per line)" }),
                        t.textarea({
                            id: clientId + ".redirectURIs",
                            name: prefix + ".redirectURIs",
                            rows: 2,
                            required: true,
                            spellcheck: false,
                            placeholder: "https://example.com/auth/callback",
                            value: () => (client.redirectURIs || []).join("\n"),
                            onchange: (e) => {
                                client.redirectURIs = e.target.value
                                    .split("\n")
                                    .map((v) => v.trim())
                                    .filter(Boolean);
                            },
                        }),
                    ),
                    t.div(
                        { className: "field-help" },
                        "Exact match. Plain http is allowed only for localhost.",
                    ),
                ),
                t.div(
                    { className: "col-sm-12 txt-right" },
                    t.button(
                        {
                            type: "button",
                            className: "btn danger transparent sm",
                            onclick: () => {
                                app.modals.confirm(
                                    `Do you really want to remove client "${
                                        client.name || client.id || "new client"
                                    }"?`,
                                    () => {
                                        clearClientErrors(index);
                                        data.config.clients.splice(index, 1);

                                        if (data.config.clients.length == 0 && !data.config.popup.enabled) {
                                            data.config.enabled = false;
                                        }
                                    },
                                );
                            },
                        },
                        t.i({ className: "ri-delete-bin-7-line", ariaHidden: true }),
                        t.span({ className: "txt" }, "Remove client"),
                    ),
                ),
                t.div({ className: "col-sm-12" }, t.hr()),
            ),
        );
    }

    return t.details(
        {
            pbEvent: "oauth2ServerAccordion",
            name: "auth-methods",
            className: "accordion oauth2-server-accordion",
        },
        t.summary(
            null,
            t.i({ className: "ri-key-2-line", ariaHidden: true }),
            t.span({ className: "txt", textContent: "OAuth2 server (sign in other apps)" }),
            t.span({
                className: () => `label m-l-auto ${data.config.enabled ? "success" : ""}`,
                textContent: () => (data.config.enabled ? "Enabled" : "Disabled"),
            }),
            () => {
                if (app.utils.isEmpty(app.store.errors?.oauth2Server)) {
                    return;
                }

                return t.i({
                    className: "ri-error-warning-fill txt-danger",
                    ariaDescription: app.attrs.tooltip("Has errors", "left"),
                });
            },
        ),
        t.div(
            { className: "grid sm" },
            t.div(
                { className: "col-sm-12" },
                t.div(
                    { className: "alert info" },
                    t.div(
                        { className: "content" },
                        t.p(
                            null,
                            "Lets other apps sign in records of this collection with the OAuth2 authorization code flow (PKCE S256 supported). "
                                + "The issued access token is the regular auth token of this collection.",
                        ),
                        t.p(
                            null,
                            "Discovery: ",
                            t.code(null, () => data.baseURL + "/.well-known/openid-configuration"),
                        ),
                        t.p(
                            null,
                            "To sign in another PocketBase app with it (popup + realtime, passkeys on this single domain), "
                                + "add an OIDC provider in that app's auth collection with the client id/secret and:",
                        ),
                        t.p(null, "Auth URL: ", t.code(null, () => data.baseURL + "/authorize")),
                        t.p(null, "Token URL: ", t.code(null, () => data.baseURL + "/token")),
                        t.p(null, "User info URL: ", t.code(null, () => data.baseURL + "/userinfo")),
                        t.p(
                            null,
                            "and register ",
                            t.code(null, "https://<that app>/api/oauth2-redirect"),
                            " as the client redirect URI. Apps sharing this database sign in the same record.",
                        ),
                    ),
                ),
            ),
            t.div(
                { className: "col-sm-6" },
                t.div(
                    { className: "field" },
                    t.input({
                        type: "checkbox",
                        id: uniqueId + ".enabled",
                        name: "oauth2Server.enabled",
                        className: "switch",
                        checked: () => data.config.enabled,
                        onchange: (e) => {
                            data.config.enabled = e.target.checked;
                            if (e.target.checked) {
                                ensureDefaults();
                            }
                        },
                    }),
                    t.label({ htmlFor: uniqueId + ".enabled", textContent: "Enable" }),
                ),
            ),
            t.div(
                { className: "col-sm-6" },
                t.div(
                    { className: "field" },
                    t.label({
                        htmlFor: uniqueId + ".codeDuration",
                        textContent: "Authorization code duration (in seconds)",
                    }),
                    t.input({
                        type: "number",
                        id: uniqueId + ".codeDuration",
                        name: "oauth2Server.codeDuration",
                        min: 10,
                        max: 600,
                        step: 1,
                        required: () => data.config.enabled,
                        value: () => data.config.codeDuration || "",
                        oninput: (e) => (data.config.codeDuration = parseInt(e.target.value, 10)),
                    }),
                ),
            ),
            t.div(
                { className: "col-sm-12" },
                t.div(
                    { className: "field" },
                    t.label({
                        htmlFor: uniqueId + ".sessionDuration",
                        textContent: "Remember sign-in for (in seconds, 0 = always ask)",
                    }),
                    t.input({
                        type: "number",
                        id: uniqueId + ".sessionDuration",
                        name: "oauth2Server.sessionDuration",
                        min: 0,
                        max: 31536000,
                        step: 1,
                        value: () => data.config.sessionDuration ?? 0,
                        oninput: (e) => (data.config.sessionDuration = parseInt(e.target.value, 10) || 0),
                    }),
                ),
                t.div(
                    { className: "field-help" },
                    "Single sign-on: while remembered, other apps sign the user in without showing this page again. "
                        + "Ends on sign out (/oauth2-server/logout), password change or expiry. 2592000 = 30 days.",
                ),
            ),
            t.div(
                { className: "col-sm-12" },
                t.div(
                    { className: "field" },
                    t.input({
                        type: "checkbox",
                        id: uniqueId + ".popup.enabled",
                        name: "oauth2Server.popup.enabled",
                        className: "switch",
                        checked: () => data.config.popup.enabled,
                        onchange: (e) => {
                            data.config.popup.enabled = e.target.checked;
                            if (e.target.checked) {
                                data.config.enabled = true;
                                ensureDefaults();
                            }
                        },
                    }),
                    t.label({
                        htmlFor: uniqueId + ".popup.enabled",
                        textContent: "Popup sign-in for your own frontends (no client needed)",
                    }),
                ),
                t.div(
                    { className: "field-help" },
                    "The frontend opens this sign-in page in a popup and gets the auth token back, without handling the password: ",
                    t.code(null, "await authWithPopup(pb, \"" + (collection.name || "COLLECTION") + "\")"),
                    ". Import it from ",
                    t.code(null, () => data.baseURL + "/popup.js"),
                    ".",
                ),
            ),
            () => {
                if (!data.config.popup.enabled) {
                    return;
                }

                return t.div(
                    { className: "col-sm-12" },
                    t.div(
                        { className: "field" },
                        t.label({
                            htmlFor: uniqueId + ".popup.origins",
                            textContent: "Popup sign-in allowed origins (one per line)",
                        }),
                        t.textarea({
                            id: uniqueId + ".popup.origins",
                            name: "oauth2Server.popup.origins",
                            rows: 2,
                            spellcheck: false,
                            placeholder: "https://app.example.com\nhttp://localhost:5173",
                            value: () => (data.config.popup.origins || []).join("\n"),
                            onchange: (e) => {
                                data.config.popup.origins = e.target.value
                                    .split("\n")
                                    .map((v) => v.trim())
                                    .filter(Boolean);
                            },
                        }),
                    ),
                    t.div(
                        { className: "field-help" },
                        "Sites that may open the popup. Only the Application URL (and this server on localhost) is allowed implicitly, so list every other frontend origin.",
                    ),
                );
            },
            () => data.config.clients.map((client, index) => clientCard(client, index)),
            t.div(
                { className: "col-sm-12" },
                t.button(
                    {
                        type: "button",
                        className: "btn lg block secondary",
                        onclick: addClient,
                    },
                    t.i({ className: "ri-add-line", ariaHidden: true }),
                    t.span({ className: "txt" }, "Add client"),
                ),
            ),
        ),
    );
}
