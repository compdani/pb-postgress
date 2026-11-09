export function passkeyAccordion(collection) {
    const uniqueId = "passkey_" + app.utils.randomString();

    const data = store({
        get config() {
            if (!collection.passkey) {
                collection.passkey = {
                    enabled: false,
                    rpId: "",
                    rpName: "",
                    origins: [],
                };
            }

            return collection.passkey;
        },
    });

    return t.details(
        {
            pbEvent: "passkeyAccordion",
            name: "auth-methods",
            className: "accordion passkey-accordion",
        },
        t.summary(
            null,
            t.i({ className: "ri-fingerprint-line", ariaHidden: true }),
            t.span({ className: "txt", textContent: "Passkeys (no email needed)" }),
            t.span({
                className: () => `label m-l-auto ${data.config.enabled ? "success" : ""}`,
                textContent: () => (data.config.enabled ? "Enabled" : "Disabled"),
            }),
            () => {
                if (app.utils.isEmpty(app.store.errors?.passkey)) {
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
                            "Users sign in with a passkey (fingerprint, face or screen lock) without entering an email or username. "
                                + "Signed-in users register passkeys with the request/confirm-passkey-registration endpoints "
                                + "and manage them through the _passkeys collection.",
                        ),
                    ),
                ),
            ),
            t.div(
                { className: "col-sm-12" },
                t.div(
                    { className: "field" },
                    t.input({
                        type: "checkbox",
                        id: uniqueId + ".enabled",
                        name: "passkey.enabled",
                        className: "switch",
                        checked: () => data.config.enabled,
                        onchange: (e) => (data.config.enabled = e.target.checked),
                    }),
                    t.label({ htmlFor: uniqueId + ".enabled", textContent: "Enable" }),
                ),
            ),
            t.div(
                { className: "col-sm-6" },
                t.div(
                    { className: "field" },
                    t.label({ htmlFor: uniqueId + ".rpId", textContent: "Relying party ID (domain)" }),
                    t.input({
                        type: "text",
                        id: uniqueId + ".rpId",
                        name: "passkey.rpId",
                        spellcheck: false,
                        placeholder: "Defaults to the Application URL host",
                        value: () => data.config.rpId || "",
                        oninput: (e) => (data.config.rpId = e.target.value.trim()),
                    }),
                ),
                t.div(
                    { className: "field-help" },
                    "Passkeys are bound to this domain. Changing it later invalidates the existing passkeys.",
                ),
            ),
            t.div(
                { className: "col-sm-6" },
                t.div(
                    { className: "field" },
                    t.label({ htmlFor: uniqueId + ".rpName", textContent: "Display name" }),
                    t.input({
                        type: "text",
                        id: uniqueId + ".rpName",
                        name: "passkey.rpName",
                        placeholder: "Defaults to the Application name",
                        value: () => data.config.rpName || "",
                        oninput: (e) => (data.config.rpName = e.target.value),
                    }),
                ),
            ),
            t.div(
                { className: "col-sm-12" },
                t.div(
                    { className: "field" },
                    t.label({ htmlFor: uniqueId + ".origins", textContent: "Allowed origins (one per line)" }),
                    t.textarea({
                        id: uniqueId + ".origins",
                        name: "passkey.origins",
                        rows: 2,
                        spellcheck: false,
                        placeholder: "Defaults to the Application URL origin\nhttps://app.example.com",
                        value: () => (data.config.origins || []).join("\n"),
                        onchange: (e) => {
                            data.config.origins = e.target.value
                                .split("\n")
                                .map((v) => v.trim())
                                .filter(Boolean);
                        },
                    }),
                ),
                t.div(
                    { className: "field-help" },
                    "Pages that run the passkey ceremonies, eg. https://app.example.com. They must be on the relying party domain or its subdomains.",
                ),
            ),
        ),
    );
}
