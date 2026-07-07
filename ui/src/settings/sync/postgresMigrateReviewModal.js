window.app = window.app || {};
window.app.modals = window.app.modals || {};

window.app.modals.openPostgresMigrateReview = function(settings = {}) {
    const modal = postgresMigrateReviewModal(settings);
    if (!modal) {
        return;
    }

    document.body.appendChild(modal);
    app.modals.open(modal);
};

function postgresMigrateReviewModal(settingsArg) {
    let modal;

    const settings = store({
        collection: null,
        s3Files: false,
        onsubmit: async () => {},
    });

    app.utils.extendStore(settings, settingsArg);

    const collection = settings.collection || {};
    const collectionIdOrName = collection.id || collection.name;

    const data = store({
        isMigrating: false,
        dryRunResult: null,
        dryRunError: "",
        isDryRunning: false,
    });

    function migrationBody(dryRun) {
        const body = { dryRun };
        const s3Enabled = app.store.settings?.s3?.enabled;
        const perCollection = app.store.settings?.s3?.scope === "perCollection";

        if (s3Enabled && perCollection) {
            body.s3Files = settings.s3Files;
        }

        return body;
    }

    async function runDryRun() {
        data.isDryRunning = true;
        data.dryRunError = "";
        data.dryRunResult = null;

        try {
            const result = await app.pb.send(
                `/api/collections/${encodeURIComponent(collectionIdOrName)}/migrate/postgres`,
                {
                    method: "POST",
                    body: migrationBody(true),
                },
            );

            data.dryRunResult = result;
            data.isDryRunning = false;
        } catch (err) {
            if (!err.isAbort) {
                data.isDryRunning = false;
                data.dryRunError = err?.data?.message || err?.message || "Dry-run failed.";
            }
        }
    }

    async function submitMigration() {
        data.isMigrating = true;

        try {
            const result = await app.pb.send(
                `/api/collections/${encodeURIComponent(collectionIdOrName)}/migrate/postgres`,
                {
                    method: "POST",
                    body: migrationBody(false),
                },
            );

            data.isMigrating = false;
            app.close(modal);

            const count = result?.migratedCount ?? 0;
            app.notify(
                `Successfully migrated "${collection.name}" to PostgreSQL (${count} record${count === 1 ? "" : "s"}).`,
                "success",
            );

            if (typeof settings.onsubmit === "function") {
                await settings.onsubmit(result);
            }
        } catch (err) {
            if (!err.isAbort) {
                data.isMigrating = false;
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
                    t.h5({ className: "modal-title" }, "Migrate to PostgreSQL"),
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
                    t.p(
                        null,
                        () =>
                            `Migrate collection "${collection.name}" from SQLite to a PocketBase-managed PostgreSQL table. Record IDs and collection settings are preserved.`,
                    ),
                    t.div(
                        { className: "alert warning m-b-base" },
                        t.div(
                            { className: "content" },
                            t.p(
                                null,
                                "Back up your ",
                                t.code(null, "pb_data"),
                                " directory before migrating production data. This action cannot be undone automatically.",
                            ),
                        ),
                    ),
                    () => {
                        const s3Enabled = app.store.settings?.s3?.enabled;
                        const perCollection = app.store.settings?.s3?.scope === "perCollection";

                        if (!s3Enabled || !perCollection) {
                            return null;
                        }

                        return t.div(
                            { className: "field m-b-base" },
                            t.label({ className: "inline-flex" }, () => {
                                return t.input({
                                    type: "checkbox",
                                    checked: () => settings.s3Files,
                                    onchange: (e) => {
                                        settings.s3Files = e.target.checked;
                                        runDryRun();
                                    },
                                });
                            }, " Store files in S3 after migration"),
                            t.p(
                                { className: "txt-hint m-t-xs" },
                                "When enabled, uploaded files for this collection will be stored in S3 instead of local disk.",
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

                        const count = data.dryRunResult?.migratedCount;
                        if (count === undefined || count === null) {
                            return t.p({ className: "txt-hint" }, "No preview data returned from dry-run.");
                        }

                        const schema = collection.postgresSchema || "public";
                        const table = collection.postgresTable || collection.name;

                        return t.div(
                            { className: "alert info" },
                            t.div(
                                { className: "content" },
                                t.p(
                                    null,
                                    t.strong(null, `${count}`),
                                    ` record${count === 1 ? "" : "s"} will be copied to `,
                                ),
                                t.code(null, `${schema}.${table}`),
                                t.span(null, "."),
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
                            disabled: () => data.isMigrating,
                            onclick: () => app.close(modal),
                        },
                        t.span({ className: "txt" }, "Cancel"),
                    ),
                    t.button(
                        {
                            type: "button",
                            className: () =>
                                `btn ${
                                    data.isMigrating || data.isDryRunning || data.dryRunError ? "btn-disabled" : ""
                                }`,
                            disabled: () => data.isMigrating || data.isDryRunning || !!data.dryRunError,
                            onclick: submitMigration,
                        },
                        t.i({ className: "ri-database-2-line" }),
                        t.span({ className: "txt" }, () => (data.isMigrating ? "Migrating..." : "Migrate")),
                    ),
                ),
            ),
        ),
    );

    return modal;
}
