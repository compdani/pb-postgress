export function openBackupRestoreModal(key) {
    const modal = backupRestoreModal(key);

    document.body.appendChild(modal);

    app.modals.open(modal);
}

function backupRestoreModal(key) {
    const uniqueId = "backup_restore_" + app.utils.randomString();

    const data = store({
        key: key,
        keyConfirm: "",
        isSubmitting: false,
        report: null,
        get canSubmit() {
            return data.key && data.key == data.keyConfirm;
        },
    });

    let reloadTimeoutId;

    async function submit() {
        if (data.isSubmitting || !data.canSubmit) {
            return;
        }

        clearTimeout(reloadTimeoutId);

        data.isSubmitting = true;

        try {
            const report = await app.pb.send(
                "/api/backups/" + encodeURIComponent(data.keyConfirm) + "/restore",
                { method: "POST" },
            );

            app.toasts.success("Successfully restored the backup.");

            // show the views a PocketBase import translated or skipped
            // before reloading (the restored superusers replace the session)
            if (report?.skippedViews?.length || report?.translatedViews?.length) {
                data.report = report;
                data.isSubmitting = false;
                return;
            }

            reloadTimeoutId = setTimeout(() => {
                window.location.reload();
                data.isSubmitting = false;
            }, 1000);
        } catch (err) {
            clearTimeout(reloadTimeoutId);

            if (!err?.isAbort) {
                data.isSubmitting = false;
                app.checkApiError(err);
            }
        }
    }

    return t.div(
        {
            pbEvent: "backupRestoreModal",
            className: "modal popup backup-restore-modal",
            onbeforeclose: () => {
                if (data.report) {
                    window.location.reload();
                }
                return !data.isSubmitting;
            },
            onafterclose: (el) => {
                el?.remove();
            },
            onunmount: () => {
                clearTimeout(reloadTimeoutId);
            },
        },
        t.header(
            { className: "modal-header" },
            t.h5(
                { className: "m-auto txt-center" },
                "Restore ",
                t.strong(null, () => data.key),
            ),
        ),
        t.div({ className: "modal-content", hidden: () => !data.report }, () => {
            if (!data.report) {
                return null;
            }
            const viewItem = (v, showError) =>
                t.li(
                    null,
                    t.strong(null, v.name),
                    showError && v.error ? t.div({ className: "txt-hint" }, v.error) : null,
                    t.div({ className: "txt-hint" }, "Original query:"),
                    t.pre({ className: "txt-code" }, v.originalQuery),
                    v.query ? t.div({ className: "txt-hint" }, "Translated query:") : null,
                    v.query ? t.pre({ className: "txt-code" }, v.query) : null,
                );
            return [
                data.report.skippedViews.length
                    ? t.div(
                        { className: "alert warning" },
                        t.div(
                            { className: "content" },
                            t.p(
                                { className: "txt-bold" },
                                "These views could not be created and were left out. Everything else was restored. Recreate them from the original query in the collections page.",
                            ),
                            t.ul(null, data.report.skippedViews.map((v) => viewItem(v, true))),
                        ),
                    )
                    : null,
                data.report.translatedViews.length
                    ? t.div(
                        { className: "alert info" },
                        t.div(
                            { className: "content" },
                            t.p(
                                { className: "txt-bold" },
                                "These views were translated from SQLite SQL. Check that their results are what you expect.",
                            ),
                            t.ul(null, data.report.translatedViews.map((v) => viewItem(v, false))),
                        ),
                    )
                    : null,
            ];
        }),
        t.form(
            {
                id: uniqueId,
                hidden: () => !!data.report,
                className: "modal-content backup-restore-form",
                autocomplete: "off",
                onsubmit: (e) => {
                    e.preventDefault();
                    submit();
                },
            },
            t.div(
                { className: "grid" },
                t.div(
                    { className: "col-lg-12" },
                    t.div(
                        { className: "alert danger" },
                        t.div(
                            { className: "content" },
                            t.p(
                                { className: "txt-bold" },
                                "Please proceed with extreme caution and use it only with trusted backups!",
                            ),
                            t.p(
                                null,
                                "The restore replaces all collections, records, superusers and app settings in the database with the ones from the backup. Every instance sharing this database is affected; restart the other instances afterwards.",
                            ),
                            t.p(null, "Two kinds of backups can be restored:"),
                            t.ul(
                                null,
                                t.li(
                                    null,
                                    "Backups created on this page, from PostgreSQL or MySQL (either database).",
                                ),
                                t.li(
                                    null,
                                    "Standard PocketBase backups (a ",
                                    t.code(null, "pb_data"),
                                    " zip with ",
                                    t.code(null, "data.db"),
                                    ") from PocketBase v0.23.0 to v0.41.x. View queries are translated from SQLite where possible; a view that still fails is left out and listed after the restore.",
                                ),
                            ),
                            t.p(
                                null,
                                "The backup is fully checked before anything changes. Files are copied to the configured file storage. The file storage, S3 and backups settings of this deployment are kept.",
                            ),
                            t.p(
                                null,
                                "On MySQL, schema changes cannot be rolled back if a later step fails. Take a database snapshot first.",
                            ),
                        ),
                    ),
                ),
                t.div(
                    { className: "col-lg-12" },
                    t.div(
                        { className: "confirm-key-label m-b-sm" },
                        "Type the backup name ",
                        t.div(
                            { className: "label" },
                            () => data.key,
                            app.components.copyButton(() => data.key),
                        ),
                        " to confirm:",
                    ),
                    t.div(
                        { className: "field" },
                        t.label({ htmlFor: uniqueId + "_key" }, "Backup name"),
                        t.input({
                            id: uniqueId + "_key",
                            name: "key",
                            type: "text",
                            required: true,
                            value: () => data.keyConfirm,
                            oninput: (e) => (data.keyConfirm = e.target.value),
                        }),
                    ),
                ),
            ),
        ),
        t.footer(
            { className: "modal-footer" },
            t.button(
                {
                    type: "button",
                    className: "btn transparent m-r-auto",
                    onclick: () => app.modals.close(),
                    disabled: () => data.isSubmitting,
                    hidden: () => !!data.report,
                },
                t.span({ className: "txt" }, "Cancel"),
            ),
            t.button(
                {
                    type: "button",
                    className: "btn m-l-auto",
                    hidden: () => !data.report,
                    onclick: () => window.location.reload(),
                },
                t.span({ className: "txt" }, "Continue to login"),
            ),
            t.button(
                {
                    "html-form": uniqueId,
                    type: "submit",
                    hidden: () => !!data.report,
                    className: () => `btn ${data.isSubmitting ? "loading" : ""}`,
                    disabled: () => data.isSubmitting || !data.canSubmit,
                },
                t.span({ className: "txt" }, "Restore backup"),
            ),
        ),
    );
}
