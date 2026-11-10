// Popup shown to a superuser when the server has migrations that were not
// applied yet: they are previewed (which collections each one changes) and
// applied on confirmation, optionally skipping the changes of some collections.

const laterKey = "pbMigrationsReviewLater";

let isChecking = false;
let isOpen = false;

function wasPostponed(files) {
    try {
        return window.sessionStorage.getItem(laterKey) === files.join("|");
    } catch (_) {
        return false;
    }
}

function postpone(files) {
    try {
        window.sessionStorage.setItem(laterKey, files.join("|"));
    } catch (_) {}
}

// Asks the server for pending migrations and opens the review popup when
// there are any (once per set of migrations when "Later" was chosen).
export async function checkPendingMigrations() {
    if (isChecking || isOpen || !app.pb.authStore.isSuperuser) {
        return;
    }
    isChecking = true;
    try {
        const result = await app.pb.send("/api/migrations/pending", {
            method: "GET",
            requestKey: "migrationsPending",
        });
        const files = result?.files || [];
        if (files.length && !wasPostponed(files)) {
            openMigrationsReviewModal(files);
        }
    } catch (err) {
        if (!err?.isAbort) {
            console.warn("failed to check the pending migrations:", err);
        }
    }
    isChecking = false;
}

export function openMigrationsReviewModal(files) {
    if (isOpen) {
        return;
    }
    isOpen = true;

    const modal = migrationsReviewModal(files);
    document.body.appendChild(modal);
    app.modals.open(modal);
}

function migrationsReviewModal(files) {
    const data = store({
        items: files.map((f) => ({ file: f, changes: [] })),
        preview: false,
        isLoading: true,
        isApplying: false,
        error: "",
        // "<file>\n<collection id>" -> true when that change is ignored
        ignored: {},
        get hasFailure() {
            return data.items.some((i) => i.error);
        },
    });

    loadPreview();

    async function loadPreview() {
        data.isLoading = true;
        data.error = "";
        try {
            const result = await app.pb.send("/api/migrations/preview", {
                method: "POST",
                requestKey: "migrationsPreview",
            });
            data.items = result?.items || [];
            data.preview = !!result?.preview;
        } catch (err) {
            if (!err?.isAbort) {
                data.error = err?.response?.message || err?.message || "Failed to preview the migrations.";
            }
        }
        data.isLoading = false;
    }

    async function apply() {
        if (data.isApplying || data.isLoading) {
            return;
        }
        const ignore = {};
        for (const key in data.ignored) {
            if (!data.ignored[key]) {
                continue;
            }
            // a new collection has no stable id yet, so it is matched by name
            const [file, id, name, action] = key.split("\n");
            (ignore[file] = ignore[file] || []).push(action == "created" ? name : id);
        }

        data.isApplying = true;
        data.error = "";
        try {
            const result = await app.pb.send("/api/migrations/apply", {
                method: "POST",
                body: { ignore: ignore },
                requestKey: "migrationsApply",
            });
            app.toasts.success("Applied " + (result?.applied?.length || 0) + " migration(s).");
            data.isApplying = false;
            app.modals.close();
            app.store.loadCollections();
        } catch (err) {
            data.isApplying = false;
            if (!err?.isAbort) {
                data.error = err?.response?.message || err?.message || "Failed to apply the migrations.";
            }
        }
    }

    const actionLabels = { created: "New collection", updated: "Changed", deleted: "Deleted" };

    function changeRow(item, change) {
        const key = [item.file, change.id, change.name, change.action].join("\n");
        const inputId = "mig_" + app.utils.randomString();
        return t.div(
            { className: "m-b-xs" },
            t.div(
                { className: "field" },
                t.input({
                    id: inputId,
                    type: "checkbox",
                    checked: () => !data.ignored[key],
                    disabled: () => data.isApplying,
                    onchange: (e) => (data.ignored[key] = !e.target.checked),
                }),
                t.label(
                    { htmlFor: inputId },
                    t.strong(null, change.name),
                    " ",
                    t.small(
                        { className: change.action == "deleted" ? "txt-danger" : "txt-hint" },
                        actionLabels[change.action] || change.action,
                    ),
                ),
            ),
            t.ul(
                { className: "txt-hint m-l-base" },
                (change.details || []).map((d) => t.li(null, d)),
            ),
        );
    }

    function itemBlock(item) {
        return t.div(
            { className: "block m-b-sm" },
            t.div({ className: "txt-bold txt-code" }, item.file),
            item.error ? t.div({ className: "alert danger m-t-xs" }, item.error) : null,
            () => {
                if (item.error || !data.preview) {
                    return null;
                }
                if (!item.changes?.length) {
                    return t.div({ className: "txt-hint" }, "No collection changes (it may change records or run SQL).");
                }
                return t.div({ className: "m-t-xs" }, item.changes.map((c) => changeRow(item, c)));
            },
        );
    }

    return t.div(
        {
            pbEvent: "migrationsReviewModal",
            className: "modal popup migrations-review-modal",
            onbeforeclose: () => !data.isApplying,
            onafterclose: (el) => {
                isOpen = false;
                el?.remove();
            },
        },
        t.header(
            { className: "modal-header" },
            t.h5({ className: "m-auto txt-center" }, "New migrations to review"),
        ),
        t.div(
            { className: "modal-content" },
            t.div(
                { className: "alert info" },
                t.div(
                    { className: "content" },
                    t.p(
                        null,
                        "The server has migrations that were not applied yet. Review what they change, then apply them. Nothing changes until you apply.",
                    ),
                    () =>
                        data.preview
                            ? t.p(
                                null,
                                "Uncheck a collection to skip its changes. Only collection changes are listed; record changes and raw SQL are not, and are not skipped.",
                            )
                            : null,
                    () =>
                        !data.isLoading && !data.preview && !data.error
                            ? t.p(
                                null,
                                "A preview is only available on PostgreSQL, so the changes cannot be listed here.",
                            )
                            : null,
                ),
            ),
            () => (data.error ? t.div({ className: "alert danger" }, data.error) : null),
            () =>
                data.isLoading
                    ? t.div({ className: "block txt-center" }, t.span({ className: "loader" }))
                    : t.div(null, data.items.map((item) => itemBlock(item))),
        ),
        t.footer(
            { className: "modal-footer" },
            t.button(
                {
                    type: "button",
                    className: "btn transparent m-r-auto",
                    disabled: () => data.isApplying,
                    onclick: () => {
                        postpone(files);
                        app.modals.close();
                    },
                },
                t.span({ className: "txt" }, "Later"),
            ),
            t.button(
                {
                    type: "button",
                    className: () => `btn ${data.isApplying ? "loading" : ""}`,
                    disabled: () => data.isApplying || data.isLoading || data.hasFailure,
                    onclick: () => apply(),
                },
                t.span({ className: "txt" }, "Apply migrations"),
            ),
        ),
    );
}
