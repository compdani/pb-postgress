import { settingsSidebar } from "../settingsSidebar";

export function pageImportPostgres(route) {
    app.store.title = "Import from PostgreSQL";

    const uniqueId = "import_pg_" + app.utils.randomString();

    const data = store({
        isLoading: false,
        postgresConfigured: true,
        search: "",
        tables: [],
        selectedTable: null,
        preview: null,
        isLoadingPreview: false,
        collectionName: "",
        collectionType: "base",
        get filteredTables() {
            const term = (data.search || "").trim().toLowerCase();
            if (!term) {
                return data.tables;
            }
            return data.tables.filter((table) => {
                const label = `${table.schema}.${table.table}`.toLowerCase();
                return label.includes(term) || (table.collectionName || "").toLowerCase().includes(term);
            });
        },
        get canImport() {
            return (
                !!data.preview
                && !!data.collectionName.trim()
                && !data.selectedTable?.registered
                && !data.isLoadingPreview
            );
        },
    });

    loadTables();

    async function loadTables() {
        data.isLoading = true;

        try {
            const result = await app.pb.send("/api/collections/postgres/tables", {
                method: "GET",
                query: { search: data.search },
                requestKey: uniqueId + "_tables",
            });

            data.tables = result?.tables || [];
            data.postgresConfigured = true;
            data.isLoading = false;
        } catch (err) {
            if (!err.isAbort) {
                data.isLoading = false;
                if (err?.status === 400) {
                    data.postgresConfigured = false;
                    data.tables = [];
                } else {
                    app.checkApiError(err);
                }
            }
        }
    }

    async function selectTable(table) {
        if (table.registered) {
            return;
        }

        data.selectedTable = table;
        data.collectionName = table.table;
        data.collectionType = "base";
        data.preview = null;
        data.isLoadingPreview = true;

        try {
            const preview = await app.pb.send(
                `/api/collections/postgres/tables/${encodeURIComponent(table.schema)}/${
                    encodeURIComponent(table.table)
                }`,
                {
                    method: "GET",
                    requestKey: uniqueId + "_preview",
                },
            );

            data.preview = preview;
            data.isLoadingPreview = false;
        } catch (err) {
            if (!err.isAbort) {
                data.isLoadingPreview = false;
                app.checkApiError(err);
            }
        }
    }

    function openReview() {
        if (!data.canImport) {
            return;
        }

        app.modals.openPostgresImportReview({
            schema: data.selectedTable.schema,
            table: data.selectedTable.table,
            collectionName: data.collectionName.trim(),
            type: data.collectionType,
            preview: data.preview,
            onsubmit: async () => {
                await app.store.loadCollections();
                data.selectedTable = null;
                data.preview = null;
                await loadTables();
            },
        });
    }

    return t.div(
        {
            pbEvent: "pageImportPostgres",
            className: "page page-import-postgres",
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
                                " to enable importing external tables.",
                            ),
                        ),
                    );
                }

                if (data.isLoading) {
                    return t.div({ className: "block txt-center" }, t.span({ className: "loader lg" }));
                }

                return t.div(
                    { className: "grid" },
                    t.div(
                        { className: "col-lg-5" },
                        t.div(
                            { className: "field m-b-base" },
                            t.label({ htmlFor: uniqueId + "_search" }, "Search tables"),
                            t.input({
                                id: uniqueId + "_search",
                                type: "text",
                                placeholder: "schema.table or collection name",
                                value: () => data.search,
                                oninput: (e) => (data.search = e.target.value),
                                onchange: () => loadTables(),
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
                                        t.th(null, "Table"),
                                        t.th(null, "Status"),
                                    ),
                                ),
                                t.tbody(null, () => {
                                    if (!data.filteredTables.length) {
                                        return t.tr(
                                            null,
                                            t.td({ colspan: 2, className: "txt-hint txt-center" }, "No tables found."),
                                        );
                                    }

                                    return data.filteredTables.map((table) => {
                                        const isSelected = data.selectedTable?.schema == table.schema
                                            && data.selectedTable?.table == table.table;
                                        const isRegistered = !!table.registered;

                                        return t.tr(
                                            {
                                                className: () =>
                                                    `${isSelected ? "active" : ""} ${
                                                        isRegistered ? "disabled" : "row-clickable"
                                                    }`,
                                                onclick: () => selectTable(table),
                                            },
                                            t.td(
                                                null,
                                                t.strong(null, `${table.schema}.${table.table}`),
                                                table.collectionName
                                                    ? t.span(
                                                        { className: "txt-hint m-l-5" },
                                                        `(${table.collectionName})`,
                                                    )
                                                    : null,
                                            ),
                                            t.td(
                                                null,
                                                isRegistered
                                                    ? t.span({ className: "label" }, "Registered")
                                                    : t.span({ className: "label outline" }, "Available"),
                                            ),
                                        );
                                    });
                                }),
                            ),
                        ),
                    ),
                    t.div(
                        { className: "col-lg-7" },
                        () => {
                            if (!data.selectedTable) {
                                return t.p({ className: "txt-hint" }, "Select a table to preview its columns.");
                            }

                            if (data.isLoadingPreview) {
                                return t.div({ className: "block txt-center" }, t.span({ className: "loader lg" }));
                            }

                            return t.div(
                                null,
                                t.h5(null, `${data.selectedTable.schema}.${data.selectedTable.table}`),
                                t.div(
                                    { className: "table-wrapper m-b-base" },
                                    t.table(
                                        { className: "table" },
                                        t.thead(
                                            null,
                                            t.tr(
                                                null,
                                                t.th(null, "Column"),
                                                t.th(null, "PG type"),
                                                t.th(null, "Field type"),
                                            ),
                                        ),
                                        t.tbody(null, () => {
                                            const rows = data.preview?.inferred || [];
                                            return rows.map((row) =>
                                                t.tr(
                                                    null,
                                                    t.td(null, row.column),
                                                    t.td(null, row.dataType),
                                                    t.td(null, row.fieldType),
                                                )
                                            );
                                        }),
                                    ),
                                ),
                                t.div(
                                    { className: "grid" },
                                    t.div(
                                        { className: "col-lg-6" },
                                        t.div(
                                            { className: "field" },
                                            t.label({ htmlFor: uniqueId + "_name" }, "Collection name"),
                                            t.input({
                                                id: uniqueId + "_name",
                                                type: "text",
                                                required: true,
                                                value: () => data.collectionName,
                                                oninput: (e) => (data.collectionName = e.target.value),
                                            }),
                                        ),
                                    ),
                                    t.div(
                                        { className: "col-lg-6" },
                                        t.div(
                                            { className: "field" },
                                            t.label({ htmlFor: uniqueId + "_type" }, "Collection type"),
                                            t.select(
                                                {
                                                    id: uniqueId + "_type",
                                                    value: () => data.collectionType,
                                                    onchange: (e) => (data.collectionType = e.target.value),
                                                },
                                                t.option({ value: "base" }, "Base"),
                                                t.option({ value: "auth" }, "Auth"),
                                            ),
                                        ),
                                    ),
                                ),
                                t.div(
                                    { className: "flex m-t-base" },
                                    t.button(
                                        {
                                            type: "button",
                                            className: () => `btn ${data.canImport ? "" : "btn-disabled"}`,
                                            disabled: () => !data.canImport,
                                            onclick: openReview,
                                        },
                                        t.i({ className: "ri-install-line" }),
                                        t.span({ className: "txt" }, "Review import"),
                                    ),
                                ),
                            );
                        },
                    ),
                );
            }),
        ),
    );
}
