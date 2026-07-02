window.app = window.app || {};
window.app.modals = window.app.modals || {};

window.app.modals.openPostgresImportReview = function(settings = {}) {
    const modal = postgresImportReviewModal(settings);
    if (!modal) {
        return;
    }

    document.body.appendChild(modal);
    app.modals.open(modal);
};

function postgresImportReviewModal(settingsArg) {
    let modal;

    const settings = store({
        schema: "",
        table: "",
        collectionName: "",
        type: "base",
        preview: null,
        s3Files: true,
        onsubmit: async () => {},
    });

    app.utils.extendStore(settings, settingsArg);

    const data = store({
        isImporting: false,
        dryRunResult: null,
        dryRunError: "",
        isDryRunning: false,
    });

    async function runDryRun() {
        data.isDryRunning = true;
        data.dryRunError = "";
        data.dryRunResult = null;

        try {
            const result = await app.pb.send("/api/collections/import/postgres", {
                method: "POST",
                body: {
                    schema: settings.schema,
                    table: settings.table,
                    collectionName: settings.collectionName,
                    type: settings.type,
                    dryRun: true,
                    s3Files: settings.s3Files,
                },
            });

            data.dryRunResult = result;
            data.isDryRunning = false;
        } catch (err) {
            if (!err.isAbort) {
                data.isDryRunning = false;
                data.dryRunError = err?.data?.message || err?.message || "Dry-run failed.";
            }
        }
    }

    async function submitImport() {
        data.isImporting = true;

        try {
            await app.pb.send("/api/collections/import/postgres", {
                method: "POST",
                body: {
                    schema: settings.schema,
                    table: settings.table,
                    collectionName: settings.collectionName,
                    type: settings.type,
                    dryRun: false,
                    s3Files: settings.s3Files,
                },
            });

            data.isImporting = false;
            app.close(modal);
            app.notify(`Successfully imported "${settings.collectionName}".`, "success");

            if (typeof settings.onsubmit === "function") {
                await settings.onsubmit();
            }
        } catch (err) {
            if (!err.isAbort) {
                data.isImporting = false;
                app.checkApiError(err);
            }
        }
    }

    runDryRun();

    modal = t.div(
        {
            className: "modal modal-lg",
            tabindex: "-1",
            role: "dialog",
            onmount: () => app.escAdd(modal, () => app.close(modal)),
            onunmount: () => app.escRemove(modal),
        },
        t.div(
            { className: "modal-dialog" },
            t.div(
                { className: "modal-content" },
                t.div(
                    { className: "modal-header" },
                    t.h5({ className: "modal-title" }, "Review PostgreSQL import"),
                    t.button(
                        {
                            type: "button",
                            className: "btn-close",
                            "aria-label": "Close",
                            onclick: () => app.close(modal),
                        },
                        t.i({ className: "ri-close-line" }),
                    ),
                ),
                t.div(
                    { className: "modal-body" },
                    t.p(null, () => {
                        return `Import ${settings.schema}.${settings.table} as collection "${settings.collectionName}" (${settings.type}).`;
                    }),
                    () => {
                        const s3Enabled = app.store.settings?.s3?.enabled;
                        const perCollection = app.store.settings?.s3?.scope === "perCollection";

                        if (!s3Enabled) {
                            return t.div(
                                { className: "alert warning m-b-base" },
                                "S3 is not enabled. Files for this collection will be stored on local disk unless you enable S3 in ",
                                t.a({ href: "#/settings/storage", className: "txt-bold" }, "File storage"),
                                " settings and set scope to ",
                                t.strong(null, "Per collection"),
                                ".",
                            );
                        }

                        if (!perCollection) {
                            return t.div(
                                { className: "alert info m-b-base" },
                                "S3 scope is set to ",
                                t.strong(null, "All collections"),
                                ". All collections will use S3 when enabled.",
                            );
                        }

                        return t.div(
                            { className: "field m-b-base" },
                            t.label({ className: "inline-flex" }, () => {
                                return t.input({
                                    type: "checkbox",
                                    checked: () => settings.s3Files,
                                    onchange: (e) => (settings.s3Files = e.target.checked),
                                });
                            }, " Store files in S3"),
                            t.p(
                                { className: "txt-hint m-t-xs" },
                                "When enabled, uploaded files for this PostgreSQL-backed collection are stored in S3 instead of local disk.",
                            ),
                        );
                    },
                    () => {
                        if (data.isDryRunning) {
                            return t.div({ className: "block txt-center m-b-base" }, t.span({ className: "loader" }));
                        }

                        if (data.dryRunError) {
                            return t.div(
                                { className: "alert danger m-b-base" },
                                t.div({ className: "content" }, data.dryRunError),
                            );
                        }

                        const fields = data.dryRunResult?.fields || [];
                        if (!fields.length) {
                            return t.p({ className: "txt-hint" }, "No fields returned from dry-run.");
                        }

                        return t.div(
                            { className: "table-wrapper" },
                            t.table(
                                { className: "table" },
                                t.thead(
                                    null,
                                    t.tr(null, t.th(null, "Field"), t.th(null, "Type")),
                                ),
                                t.tbody(
                                    null,
                                    () =>
                                        fields.map((field) =>
                                            t.tr(null, t.td(null, field.name), t.td(null, field.type))
                                        ),
                                ),
                            ),
                        );
                    },
                ),
                t.div(
                    { className: "modal-footer" },
                    t.button(
                        {
                            type: "button",
                            className: "btn btn-transparent",
                            disabled: () => data.isImporting,
                            onclick: () => app.close(modal),
                        },
                        t.span({ className: "txt" }, "Cancel"),
                    ),
                    t.button(
                        {
                            type: "button",
                            className: () =>
                                `btn ${
                                    data.isImporting || data.isDryRunning || data.dryRunError ? "btn-disabled" : ""
                                }`,
                            disabled: () => data.isImporting || data.isDryRunning || !!data.dryRunError,
                            onclick: submitImport,
                        },
                        t.i({ className: "ri-install-line" }),
                        t.span({ className: "txt" }, () => (data.isImporting ? "Importing..." : "Import")),
                    ),
                ),
            ),
        ),
    );

    return modal;
}
