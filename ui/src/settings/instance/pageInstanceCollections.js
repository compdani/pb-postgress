import { settingsSidebar } from "../settingsSidebar";

export function pageInstanceCollections() {
    app.store.title = "Instance collections";

    const data = store({
        isLoading: true,
        isSaving: false,
        isRefreshing: false,
        mode: "all",
        selected: [],
        instances: [],
        allCollections: [],
        initSerialized: "",
        get hasChanges() {
            return data.initSerialized !== JSON.stringify({ mode: data.mode, collections: selectedNames(data) });
        },
    });

    load();

    async function load() {
        data.isLoading = true;
        try {
            const [scope, instances, allCollections] = await Promise.all([
                app.pb.send("/api/instance/scope", { method: "GET", requestKey: "instance_scope" }),
                app.pb.send("/api/instances", { method: "GET", requestKey: "instance_list" }),
                app.pb.send("/api/instance/collections", { method: "GET", requestKey: "instance_collections" }),
                app.store.loadCollections(),
            ]);
            data.allCollections = Array.isArray(allCollections) ? allCollections : [];
            data.mode = scope?.mode || "all";
            data.selected = Array.isArray(scope?.collections) ? scope.collections.slice() : [];
            data.instances = instanceRows(instances);
            data.initSerialized = JSON.stringify({ mode: data.mode, collections: selectedNames(data) });
            data.isLoading = false;
        } catch (err) {
            if (!err?.isAbort) {
                data.isLoading = false;
                app.checkApiError(err);
            }
        }
    }

    // Reloads the collections and instances lists without touching unsaved selection changes.
    async function refresh() {
        if (data.isRefreshing) {
            return;
        }
        data.isRefreshing = true;
        try {
            const [instances, allCollections] = await Promise.all([
                app.pb.send("/api/instances", { method: "GET", requestKey: "instance_list" }),
                app.pb.send("/api/instance/collections", { method: "GET", requestKey: "instance_collections" }),
                app.store.loadCollections(),
            ]);
            data.instances = instanceRows(instances);
            data.allCollections = Array.isArray(allCollections) ? allCollections : [];
        } catch (err) {
            if (!err?.isAbort) {
                app.checkApiError(err);
            }
        }
        data.isRefreshing = false;
    }

    function toggleCollection(name, checked) {
        const next = data.selected.filter((item) => item !== name);
        if (checked) {
            next.push(name);
        }
        data.selected = next;
    }

    async function save(e) {
        e?.preventDefault();
        if (data.isSaving || !data.hasChanges) {
            return;
        }
        data.isSaving = true;
        try {
            const body = { mode: data.mode, collections: selectedNames(data) };
            const saved = await app.pb.send("/api/instance/scope", {
                method: "PUT",
                body,
                requestKey: "instance_scope_save",
            });
            data.mode = saved?.mode || body.mode;
            data.selected = Array.isArray(saved?.collections) ? saved.collections.slice() : body.collections.slice();
            data.initSerialized = JSON.stringify({ mode: data.mode, collections: selectedNames(data) });
            app.toasts.success("Successfully saved instance collections.");
        } catch (err) {
            app.checkApiError(err);
        }
        data.isSaving = false;
    }

    function reset() {
        const parsed = JSON.parse(data.initSerialized || "{\"mode\":\"all\",\"collections\":[]}");
        data.mode = parsed.mode || "all";
        data.selected = parsed.collections || [];
    }

    return t.div(
        {
            pbEvent: "pageInstanceCollections",
            className: "page page-instance-collections",
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
                if (data.isLoading) {
                    return t.div({ className: "block txt-center" }, t.span({ className: "loader lg" }));
                }

                return t.form(
                    {
                        pbEvent: "instanceCollectionsForm",
                        className: "grid",
                        inert: () => data.isSaving,
                        onsubmit: save,
                    },
                    t.div(
                        { className: "col-lg-12" },
                        t.p(
                            { className: "txt-hint" },
                            "Choose which collections this instance serves. The choice is stored for the superuser scope of this instance.",
                        ),
                    ),
                    t.div(
                        { className: "col-lg-12" },
                        t.button(
                            {
                                type: "button",
                                className: () => `btn sm secondary ${data.isRefreshing ? "loading" : ""}`,
                                disabled: () => data.isRefreshing || data.isSaving,
                                onclick: refresh,
                            },
                            t.i({ className: "ri-refresh-line", ariaHidden: "true" }),
                            t.span({ className: "txt" }, "Refresh collections"),
                        ),
                    ),
                    t.div(
                        { className: "col-lg-12" },
                        [
                            ["all", "All collections"],
                            ["allow", "Only selected"],
                            ["deny", "All except selected"],
                        ].map(([value, label]) => {
                            return t.div(
                                { className: "field m-b-10" },
                                t.input({
                                    type: "radio",
                                    id: "instance_scope_" + value,
                                    name: "scopeMode",
                                    checked: () => data.mode === value,
                                    onchange: () => (data.mode = value),
                                }),
                                t.label({ htmlFor: "instance_scope_" + value }, label),
                            );
                        }),
                    ),
                    t.div({ className: "col-lg-12" }, () => {
                        const collections = data.allCollections || [];
                        if (!collections.length) {
                            return t.div({ className: "txt-hint" }, "No collections yet.");
                        }
                        return t.div(
                            { className: () => (data.mode === "all" ? "disabled" : "") },
                            collections.map((collection) => {
                                return t.div(
                                    { className: "field m-b-5" },
                                    t.input({
                                        type: "checkbox",
                                        id: "instance_col_" + collection.id,
                                        disabled: () => data.mode === "all" || data.isSaving,
                                        checked: () => data.selected.indexOf(collection.name) >= 0,
                                        onchange: (e) => toggleCollection(collection.name, e.target.checked),
                                    }),
                                    t.label(
                                        { htmlFor: "instance_col_" + collection.id },
                                        collection.name,
                                        t.span({ className: "txt-hint m-l-5" }, collection.type),
                                    ),
                                );
                            }),
                        );
                    }),
                    t.div({ className: "col-lg-12" }, t.hr()),
                    t.div(
                        { className: "col-lg-12" },
                        t.div(
                            { className: "flex" },
                            t.div({ className: "m-r-auto" }),
                            t.button(
                                {
                                    hidden: () => !data.hasChanges,
                                    type: "button",
                                    className: "btn transparent secondary",
                                    onclick: reset,
                                },
                                t.span({ className: "txt" }, "Cancel"),
                            ),
                            t.button(
                                {
                                    className: () => `btn expanded-lg ${data.isSaving ? "loading" : ""}`,
                                    disabled: () => !data.hasChanges || data.isSaving,
                                },
                                t.span({ className: "txt" }, "Save changes"),
                            ),
                        ),
                    ),
                    t.div({ className: "col-lg-12" }, t.h6({ className: "m-t-base" }, "Instances")),
                    t.div(
                        { className: "col-lg-12" },
                        t.div(
                            { className: "table-wrapper" },
                            t.table(
                                { className: "table" },
                                t.thead(
                                    null,
                                    t.tr(
                                        null,
                                        t.th(null, "Id"),
                                        t.th(null, "URL"),
                                        t.th(null, "Mode"),
                                        t.th(null, "Registered"),
                                    ),
                                ),
                                t.tbody(null, () => {
                                    if (!data.instances.length) {
                                        return t.tr(
                                            null,
                                            t.td(
                                                { colspan: 4, className: "txt-hint txt-center" },
                                                "No instances registered.",
                                            ),
                                        );
                                    }
                                    return data.instances.map((instance) => {
                                        return t.tr(
                                            null,
                                            t.td(null, instance.id || instance.instanceId || ""),
                                            t.td(null, instance.url || ""),
                                            t.td(null, instance.scopeMode || ""),
                                            t.td(null, instance.created_at || ""),
                                        );
                                    });
                                }),
                            ),
                        ),
                    ),
                );
            }),
            t.footer({ className: "page-footer" }, app.components.credits()),
        ),
    );
}

function selectedNames(data) {
    return (data.selected || []).slice().sort();
}

function instanceRows(result) {
    if (Array.isArray(result)) {
        return result;
    }
    if (Array.isArray(result?.instances)) {
        return result.instances;
    }
    if (Array.isArray(result?.items)) {
        return result.items;
    }
    return [];
}
