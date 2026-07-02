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
