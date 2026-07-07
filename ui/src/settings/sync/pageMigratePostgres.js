import { settingsSidebar } from "../settingsSidebar";

export function pageMigratePostgres(route) {
    app.store.title = "Migrate to PostgreSQL";

    const uniqueId = "migrate_pg_" + app.utils.randomString();

    const data = store({
        isLoading: false,
        postgresConfigured: true,
        search: "",
        get migratableCollections() {
            const term = (data.search || "").trim().toLowerCase();
            const collections = (app.store.collections || []).filter((collection) => {
                if (collection.type === "view") {
                    return false;
                }
                if (collection.external) {
                    return false;
                }
                if (collection.postgresRecords) {
                    return false;
                }
                return true;
            });

            if (!term) {
                return collections;
            }

            return collections.filter((collection) => {
                return (
                    (collection.name || "").toLowerCase().includes(term)
                    || (collection.id || "").toLowerCase().includes(term)
                );
            });
        },
    });

    loadStatus();

    async function loadStatus() {
        data.isLoading = true;

        try {
            await app.store.loadCollections();

            const result = await app.pb.send("/api/collections/postgres/status", {
                method: "GET",
                requestKey: uniqueId + "_status",
            });

            data.postgresConfigured = !!result?.configured;
            data.isLoading = false;
        } catch (err) {
            if (!err.isAbort) {
                data.isLoading = false;
                if (err?.status === 400) {
                    data.postgresConfigured = false;
                } else {
                    app.checkApiError(err);
                }
            }
        }
    }

    function openMigrateReview(collection) {
        app.modals.openPostgresMigrateReview({
            collection,
            onsubmit: async () => {
                await app.store.loadCollections();
            },
        });
    }

    return t.div(
        {
            pbEvent: "pageMigratePostgres",
            className: "page page-migrate-postgres",
        },
        settingsSidebar(),
        t.div(
            { className: "page-content full-height" },
            t.header(
                { className: "page-header" },
                t.nav(
                    { className: "breadcrumbs" },
                    t.div({ className: "breadcrumb-item" }, "Settings"),
                    t.div({ className: "breadcrumb-item" }, () => app.store.title),
                ),
            ),
            t.div({ className: "wrapper m-b-base" }, () => {
                if (!data.postgresConfigured) {
                    return t.div(
                        { className: "alert info" },
                        t.div(
                            { className: "content" },
                            t.p(
                                null,
                                "PostgreSQL is not configured. Set ",
                                t.code(null, "PB_POSTGRES_URL"),
                                " to enable migrating collections from SQLite to PostgreSQL.",
                            ),
                        ),
                    );
                }

                if (data.isLoading) {
                    return t.div({ className: "block txt-center" }, t.span({ className: "loader lg" }));
                }

                return t.div(
                    null,
                    t.p(
                        { className: "txt-hint m-b-base" },
                        "Move existing SQLite-backed collections to PocketBase-managed PostgreSQL tables. Collection names, schemas, rules, and record IDs are preserved.",
                    ),
                    t.div(
                        { className: "field m-b-base" },
                        t.label({ htmlFor: uniqueId + "_search" }, "Search collections"),
                        t.input({
                            id: uniqueId + "_search",
                            type: "text",
                            placeholder: "Collection name or id",
                            value: () => data.search,
                            oninput: (e) => (data.search = e.target.value),
                        }),
                    ),
                    t.div(
                        { className: "table-wrapper" },
                        t.table(
                            { className: "table" },
                            t.thead(
                                null,
                                t.tr(
                                    null,
                                    t.th(null, "Collection"),
                                    t.th(null, "Type"),
                                    t.th(null, "Storage"),
                                    t.th({ className: "min-width" }, ""),
                                ),
                            ),
                            t.tbody(null, () => {
                                if (!data.migratableCollections.length) {
                                    return t.tr(
                                        null,
                                        t.td(
                                            { colspan: 4, className: "txt-hint txt-center" },
                                            "No SQLite-backed collections available to migrate.",
                                        ),
                                    );
                                }

                                return data.migratableCollections.map((collection) => {
                                    return t.tr(
                                        null,
                                        t.td(
                                            null,
                                            t.strong(null, collection.name),
                                            t.span({ className: "txt-hint m-l-5" }, collection.id),
                                        ),
                                        t.td(null, app.utils.sentenize(collection.type, false)),
                                        t.td(null, t.span({ className: "label outline" }, "SQLite")),
                                        t.td(
                                            { className: "txt-right" },
                                            t.button(
                                                {
                                                    type: "button",
                                                    className: "btn btn-outline btn-sm",
                                                    onclick: () => openMigrateReview(collection),
                                                },
                                                t.i({ className: "ri-database-2-line" }),
                                                t.span({ className: "txt" }, "Migrate"),
                                            ),
                                        ),
                                    );
                                });
                            }),
                        ),
                    ),
                );
            }),
        ),
    );
}
