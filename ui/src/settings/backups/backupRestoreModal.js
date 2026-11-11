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
        dropCollections: true,
        collections: [],
        resolutions: {},
        hasSettings: false,
        settings: "",
        loadingCollections: true,
        analysisOk: false,
        filesError: "",
        otherInstances: 0,
        isSubmitting: false,
        report: null,
        get canSubmit() {
            return data.key && data.key == data.keyConfirm && data.analysisOk;
        },
    });

    let reloadTimeoutId;

    async function filesAction(discard) {
        if (data.isSubmitting) {
            return;
        }
        data.isSubmitting = true;
        try {
            await app.pb.send("/api/backups/restore-files", { method: discard ? "DELETE" : "POST" });
            app.toasts.success(discard ? "Discarded the pending files." : "Copied the files.");
            window.location.reload();
        } catch (err) {
            data.isSubmitting = false;
            app.checkApiError(err);
        }
    }

    app.pb
        .send("/api/backups/" + encodeURIComponent(key) + "/collections", { method: "GET" })
        .then((analysis) => {
            const list = analysis?.collections || [];
            const resolutions = {};
            for (const c of list) {
                resolutions[c.id] = {
                    // same behavior as before for conflicts: the backup replaces them
                    // with other instances on this database nothing is replaced by default
                    action: c.conflict ? (analysis.otherInstances ? "skip" : "replace") : "import",
                    newName: "",
                    shared: false,
                };
            }
            data.resolutions = resolutions;
            data.collections = list;
            data.hasSettings = !!analysis?.hasSettings;
            data.otherInstances = analysis?.otherInstances || 0;
            if (analysis?.otherInstances) {
                data.dropCollections = false;
            }
            data.analysisOk = true;
        })
        .catch(() => {})
        .finally(() => {
            data.loadingCollections = false;
        });

    async function submit() {
        if (data.isSubmitting || !data.canSubmit) {
            return;
        }

        clearTimeout(reloadTimeoutId);

        data.isSubmitting = true;

        try {
            const resolutions = {};
            for (const c of data.collections) {
                const r = data.resolutions[c.id];
                if (!r || (!c.conflict && r.action === "import")) {
                    continue;
                }
                if (r.action === "rename" && !r.newName.trim()) {
                    throw new Error("Enter a new name for \"" + c.name + "\".");
                }
                resolutions[c.id] = {
                    action: r.action === "import" ? "skip" : r.action,
                    newName: r.action === "rename" ? r.newName.trim() : "",
                    shared: r.action === "skip" && !!r.shared,
                };
            }
            const report = await app.pb.send(
                "/api/backups/" + encodeURIComponent(data.keyConfirm) + "/restore",
                {
                    method: "POST",
                    body: {
                        dropCollections: data.dropCollections,
                        resolutions: resolutions,
                        requireResolution: true,
                        settings: data.settings,
                    },
                },
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

            // the database was restored (or is waiting on) but the files were not copied
            const message = err?.response?.message || "";
            if (/cop(y|ying) (the|its) files/i.test(message)) {
                data.filesError = message;
                data.isSubmitting = false;
                return;
            }

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
        t.div(
            { className: "modal-content", hidden: () => !data.filesError },
            t.div(
                { className: "alert warning" },
                t.div(
                    { className: "content" },
                    t.p(
                        { className: "txt-bold" },
                        "The restore changed the database but the files are not all copied.",
                    ),
                    t.p(null, () => data.filesError),
                    t.div(
                        { className: "flex m-t-sm" },
                        t.button(
                            {
                                type: "button",
                                className: "btn transparent",
                                disabled: () => data.isSubmitting,
                                onclick: () => {
                                    if (window.confirm("Discard the missing files?")) {
                                        filesAction(true);
                                    }
                                },
                            },
                            t.span({ className: "txt" }, "Discard"),
                        ),
                        t.button(
                            {
                                type: "button",
                                className: () => `btn m-l-auto ${data.isSubmitting ? "loading" : ""}`,
                                disabled: () => data.isSubmitting,
                                onclick: () => filesAction(false),
                            },
                            t.span({ className: "txt" }, "Retry copying files"),
                        ),
                    ),
                ),
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
                hidden: () => !!data.report || !!data.filesError,
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
                        { className: "field" },
                        t.input({
                            id: uniqueId + "_drop",
                            type: "checkbox",
                            checked: () => data.dropCollections,
                            onchange: (e) => (data.dropCollections = e.target.checked),
                        }),
                        t.label(
                            { htmlFor: uniqueId + "_drop" },
                            "Drop collections that are not in the backup",
                        ),
                    ),
                    t.div(
                        { className: "m-t-sm", hidden: () => !data.collections.length },
                        t.div(
                            { className: "txt-bold m-b-xs" },
                            "Collections in the backup:",
                        ),
                        () =>
                            data.collections.map((c) => {
                                const r = data.resolutions[c.id];
                                const conflict = c.conflict;
                                return t.div(
                                    { className: "field" },
                                    t.label(
                                        null,
                                        c.name,
                                        " ",
                                        t.small({ className: "txt-hint" }, c.system ? c.type + ", system" : c.type),
                                    ),
                                    conflict
                                        ? t.div(
                                            { className: "txt-hint" },
                                            "Exists here (" + conflict.localRecords + " records)"
                                                + (conflict.localReferencedBy?.length
                                                    ? ", used by "
                                                        + conflict.localReferencedBy.map((x) =>
                                                            x.collection + "." + x.field
                                                        ).join(", ")
                                                    : ""),
                                        )
                                        : null,
                                    t.select(
                                        { value: () => r.action, onchange: (e) => (r.action = e.target.value) },
                                        conflict ? null : t.option({ value: "import" }, "Import"),
                                        t.option(
                                            { value: "skip" },
                                            conflict ? "Skip (keep existing)" : "Don't import",
                                        ),
                                        conflict && (!c.system || !data.otherInstances)
                                            ? t.option({ value: "replace" }, "Replace existing")
                                            : null,
                                        conflict && !c.system
                                            ? t.option({ value: "rename" }, "Import under a new name")
                                            : null,
                                    ),
                                    () =>
                                        r.action === "rename"
                                            ? t.input({
                                                type: "text",
                                                placeholder: "New collection name",
                                                value: () => r.newName,
                                                oninput: (e) => (r.newName = e.target.value),
                                            })
                                            : null,
                                );
                            }),
                        () =>
                            data.hasSettings
                                ? t.div(
                                    { className: "field m-t-sm" },
                                    t.label(null, "Settings"),
                                    t.select(
                                        {
                                            value: () => data.settings,
                                            onchange: (e) => (data.settings = e.target.value),
                                        },
                                        t.option({ value: "" }, "Restore the backup's settings"),
                                        t.option({ value: "none" }, "Keep the current settings"),
                                        t.option({ value: "instance" }, "Restore for this instance only"),
                                    ),
                                )
                                : null,
                        t.div(
                            { className: "txt-hint" },
                            "Skipped collections keep their current schema and records. Collections used by other collections are re-pointed automatically; a renamed collection gets a new id.",
                        ),
                    ),
                    t.div(
                        { className: "txt-hint" },
                        "Unchecked, collections missing from the backup (and their records) are kept as they are.",
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
                    hidden: () => !!data.report || !!data.filesError,
                    className: () => `btn ${data.isSubmitting ? "loading" : ""}`,
                    disabled: () => data.isSubmitting || !data.canSubmit,
                },
                t.span({ className: "txt" }, "Restore backup"),
            ),
        ),
    );
}
