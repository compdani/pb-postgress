// Setup flow for an instance that connected to a database another instance
// already set up: replica of an instance, or a new instance (clean or
// restored from a backup, with per-collection conflict resolution).
//
// api: { post(path, body), get(path), upload(file), error(err, fallback), done() }
export function joinStep(initialInstances, api, initialStage) {
    const data = store({
        stage: initialStage || "role", // role | backup | resolve | files
        filesError: initialStage === "files" ? "Copying the restored files did not finish." : "",
        mode: "new", // replica | new
        start: "clean", // clean | restore
        copyFrom: initialInstances?.[0]?.id || "",
        name: "",
        url: window.location.origin,
        backups: [],
        backupKey: "",
        analysis: null,
        resolutions: {},
        settings: "instance",
        busy: false,
        error: "",
    });
    const instances = initialInstances || [];

    function fail(err, fallback) {
        data.error = api.error(err, fallback);
        data.busy = false;
    }

    async function loadBackups() {
        try {
            data.backups = (await api.get("/api/installer/backups")) || [];
            if (!data.backupKey && data.backups.length) {
                data.backupKey = data.backups[0].key;
            }
        } catch (err) {
            fail(err, "Could not list the backups.");
        }
    }

    async function next(e) {
        e?.preventDefault();
        if (data.busy) {
            return;
        }
        data.error = "";
        if (data.mode === "new" && data.start === "restore") {
            data.stage = "backup";
            data.busy = true;
            await loadBackups();
            data.busy = false;
            return;
        }
        await submit();
    }

    async function upload(e) {
        const file = e.target.files?.[0];
        if (!file || data.busy) {
            return;
        }
        data.error = "";
        data.busy = true;
        try {
            const result = await api.upload(file);
            await loadBackups();
            data.backupKey = result?.key || data.backupKey;
        } catch (err) {
            fail(err, "Upload failed.");
        }
        data.busy = false;
    }

    function defaultResolution(c) {
        return c.conflict ? { action: "skip", newName: "", shared: false } : { action: "import", newName: "", shared: false };
    }

    async function analyze(e) {
        e?.preventDefault();
        if (data.busy || !data.backupKey) {
            return;
        }
        data.error = "";
        data.busy = true;
        try {
            const analysis = await api.post("/api/installer/backup/analyze", { key: data.backupKey });
            const resolutions = {};
            for (const c of analysis.collections) {
                resolutions[c.id] = defaultResolution(c);
            }
            data.resolutions = resolutions;
            data.analysis = analysis;
            data.stage = "resolve";
        } catch (err) {
            fail(err, "Could not read the backup.");
        }
        data.busy = false;
    }

    function resolutionsPayload() {
        const result = {};
        for (const c of data.analysis.collections) {
            const r = data.resolutions[c.id];
            if (!r) {
                continue;
            }
            if (!c.conflict && r.action === "import") {
                continue;
            }
            result[c.id] = {
                action: r.action === "import" ? "skip" : r.action,
                newName: r.action === "rename" ? r.newName.trim() : "",
                shared: r.action === "skip" && !!r.shared,
            };
        }
        return result;
    }

    function validateResolutions() {
        for (const c of data.analysis.collections) {
            const r = data.resolutions[c.id];
            if (r?.action === "rename" && !r.newName.trim()) {
                return `Enter a new name for "${c.name}".`;
            }
        }
        return "";
    }

    async function filesAction(discard) {
        if (data.busy) {
            return;
        }
        data.error = "";
        data.busy = true;
        try {
            await api.post("/api/installer/join/files", { discard: discard });
            await api.done();
        } catch (err) {
            fail(err, "Copying the files failed.");
        }
        data.busy = false;
    }

    async function submit(e) {
        e?.preventDefault();
        if (data.busy) {
            return;
        }
        data.error = "";
        if (data.stage === "resolve") {
            data.error = validateResolutions();
            if (data.error) {
                return;
            }
            if (
                Object.values(data.resolutions).some((r) => r.action === "replace") &&
                !window.confirm(
                    "Replace deletes the existing collection's records for every instance that uses it. Continue?",
                )
            ) {
                return;
            }
        }
        data.busy = true;
        try {
            const body = {
                mode: data.mode,
                name: data.name.trim(),
                url: data.url.trim(),
            };
            if (data.mode === "replica") {
                body.copyFrom = data.copyFrom;
            } else {
                body.start = data.start;
                if (data.start === "restore") {
                    body.backup = data.backupKey;
                    body.settings = data.settings;
                    body.resolutions = resolutionsPayload();
                }
            }
            const result = await api.post("/api/installer/join", body);
            if (result?.filesPending) {
                data.filesError = result.filesError || "Copying the files failed.";
                data.stage = "files";
                data.busy = false;
                return;
            }
            await api.done();
        } catch (err) {
            fail(err, "Could not finish setup.");
        }
        data.busy = false;
    }

    return t.div(
        { className: "grid installer-form" },
        () => (data.error ? t.div({ className: "col-12" }, t.div({ className: "alert danger" }, data.error)) : null),
        () => {
            if (data.stage === "role") {
                return roleStage(data, instances, next);
            }
            if (data.stage === "backup") {
                return backupStage(data, upload, analyze);
            }
            if (data.stage === "files") {
                return filesStage(data, filesAction);
            }
            return resolveStage(data, submit);
        },
    );
}

function radio(data, key, value, label, hint) {
    return t.label(
        { className: "field m-b-sm", style: "display:block;cursor:pointer" },
        t.input({
            type: "radio",
            name: "installer_" + key,
            checked: () => data[key] === value,
            disabled: () => data.busy,
            onchange: () => (data[key] = value),
        }),
        " ",
        t.strong(null, label),
        hint ? t.div({ className: "txt-hint" }, hint) : null,
    );
}

function textField(data, key, label, placeholder) {
    return t.div(
        { className: "field" },
        t.label(null, label),
        t.input({
            type: "text",
            placeholder: placeholder || "",
            disabled: () => data.busy,
            value: () => data[key],
            oninput: (e) => (data[key] = e.target.value),
        }),
    );
}

function roleStage(data, instances, next) {
    return t.form(
        { className: "col-12 grid", onsubmit: next },
        t.div(
            { className: "col-12" },
            t.div(
                { className: "alert info" },
                "This database is already set up. Superusers are shared by every instance. Choose how this instance joins it.",
            ),
        ),
        t.div(
            { className: "col-12" },
            radio(
                data,
                "mode",
                "replica",
                "Replica of an existing instance",
                "Same collections as the instance you pick, with its own copy of the settings.",
            ),
            () =>
                data.mode === "replica"
                    ? instances.length
                        ? t.div(
                            { className: "field m-b-sm" },
                            t.select(
                                { value: () => data.copyFrom, onchange: (e) => (data.copyFrom = e.target.value) },
                                instances.map((i) => t.option({ value: i.id }, i.name + (i.url ? " (" + i.url + ")" : ""))),
                            ),
                        )
                        : t.div({ className: "alert warning" }, "There is no other instance to replicate.")
                    : null,
            radio(
                data,
                "mode",
                "new",
                "New instance",
                "Its own settings and collections. Other instances' collections stay hidden from it.",
            ),
            () =>
                data.mode === "new"
                    ? t.div(
                        { className: "m-l-base" },
                        radio(data, "start", "clean", "Start clean", "No collections yet."),
                        radio(
                            data,
                            "start",
                            "restore",
                            "Restore from a backup",
                            "Pick the collections to import and how to handle conflicts.",
                        ),
                    )
                    : null,
        ),
        t.div({ className: "col-12" }, textField(data, "name", "Instance name", "e.g. Events")),
        t.div({ className: "col-12" }, textField(data, "url", "Public URL", "https://events.example.com")),
        t.div(
            { className: "col-12" },
            t.button(
                {
                    type: "submit",
                    className: () => `btn block ${data.busy ? "loading" : ""}`,
                    disabled: () => data.busy || (data.mode === "replica" && !data.copyFrom),
                },
                t.span({ className: "txt" }, () =>
                    data.mode === "new" && data.start === "restore" ? "Next" : "Finish setup"),
            ),
        ),
    );
}

function backupStage(data, upload, analyze) {
    return t.form(
        { className: "col-12 grid", onsubmit: analyze },
        t.div(
            { className: "col-12" },
            t.div({ className: "txt-hint" }, "Choose a backup from the backups folder, or upload one."),
        ),
        t.div(
            { className: "col-12" },
            () =>
                data.backups.length
                    ? t.div(
                        { className: "field" },
                        t.label(null, "Backup"),
                        t.select(
                            { value: () => data.backupKey, onchange: (e) => (data.backupKey = e.target.value) },
                            data.backups.map((b) => t.option({ value: b.key }, b.key)),
                        ),
                    )
                    : t.div({ className: "txt-hint" }, "No backups found."),
        ),
        t.div(
            { className: "col-12" },
            t.div(
                { className: "field" },
                t.label(null, "Upload a backup (.zip)"),
                t.input({ type: "file", accept: ".zip", disabled: () => data.busy, onchange: upload }),
            ),
        ),
        t.div(
            { className: "col-12 flex" },
            t.button(
                {
                    type: "button",
                    className: "btn transparent",
                    disabled: () => data.busy,
                    onclick: () => (data.stage = "role"),
                },
                t.span({ className: "txt" }, "Back"),
            ),
            t.button(
                {
                    type: "submit",
                    className: () => `btn m-l-auto ${data.busy ? "loading" : ""}`,
                    disabled: () => data.busy || !data.backupKey,
                },
                t.span({ className: "txt" }, "Analyze backup"),
            ),
        ),
    );
}

function refs(list) {
    return (list || []).map((r) => r.collection + "." + r.field).join(", ");
}

function collectionRow(data, c) {
    const r = data.resolutions[c.id];
    const conflict = c.conflict;
    const select = t.select(
        {
            value: () => r.action,
            disabled: () => data.busy,
            onchange: (e) => (r.action = e.target.value),
        },
        conflict ? null : t.option({ value: "import" }, "Import"),
        conflict ? t.option({ value: "skip" }, "Skip (keep existing)") : t.option({ value: "skip" }, "Don't import"),
        conflict && !c.system ? t.option({ value: "replace" }, "Replace existing") : null,
        conflict && !conflict.localSystem && !c.system ? t.option({ value: "rename" }, "Import under a new name") : null,
    );
    return t.div(
        { className: "block m-b-sm p-sm", style: "border:1px solid var(--baseAlt2Color, #ddd);border-radius:6px" },
        t.div(
            null,
            t.strong(null, c.name),
            " ",
            t.small({ className: "txt-hint" }, c.system ? c.type + ", system" : c.type),
        ),
        conflict
            ? t.div(
                { className: "txt-hint" },
                `Conflicts with an existing ${conflict.localType} collection "${conflict.localName}" ` +
                    `(${conflict.localRecords} records)` +
                    (conflict.localReferencedBy?.length
                        ? `, used by ${refs(conflict.localReferencedBy)}`
                        : ""),
            )
            : null,
        c.referencedBy?.length
            ? t.div({ className: "txt-hint" }, "Referenced by " + refs(c.referencedBy))
            : null,
        t.div({ className: "field m-t-5" }, select),
        () =>
            r.action === "rename"
                ? t.div(
                    { className: "field" },
                    t.input({
                        type: "text",
                        placeholder: "New collection name",
                        value: () => r.newName,
                        oninput: (e) => (r.newName = e.target.value),
                    }),
                )
                : null,
        () =>
            conflict && r.action === "skip" && !c.system
                ? t.label(
                    { className: "txt-hint", style: "display:block" },
                    t.input({
                        type: "checkbox",
                        checked: () => !!r.shared,
                        onchange: (e) => (r.shared = e.target.checked),
                    }),
                    " Keep the existing collection visible to this instance (shared)",
                )
                : null,
    );
}

function resolveStage(data, submit) {
    return t.form(
        { className: "col-12 grid", onsubmit: submit },
        t.div(
            { className: "col-12" },
            t.div(
                { className: "alert info" },
                "Collections you import (or share) become the only ones this instance can see. Superusers and system collections are shared and stay as they are.",
            ),
        ),
        t.div(
            { className: "col-12" },
            () => data.analysis.collections.map((c) => collectionRow(data, c)),
        ),
        data.analysis.hasSettings
            ? t.div(
                { className: "col-12" },
                t.label(
                    null,
                    t.input({
                        type: "checkbox",
                        checked: () => data.settings === "instance",
                        onchange: (e) => (data.settings = e.target.checked ? "instance" : "none"),
                    }),
                    " Restore the backup's settings for this instance only",
                ),
                t.div(
                    { className: "txt-hint" },
                    "Other instances' settings, S3 and backups storage are not changed.",
                ),
            )
            : null,
        t.div(
            { className: "col-12 flex" },
            t.button(
                {
                    type: "button",
                    className: "btn transparent",
                    disabled: () => data.busy,
                    onclick: () => (data.stage = "backup"),
                },
                t.span({ className: "txt" }, "Back"),
            ),
            t.button(
                {
                    type: "submit",
                    className: () => `btn m-l-auto ${data.busy ? "loading" : ""}`,
                    disabled: () => data.busy,
                },
                t.span({ className: "txt" }, "Restore and finish"),
            ),
        ),
    );
}

function filesStage(data, filesAction) {
    return t.div(
        { className: "col-12 grid" },
        t.div(
            { className: "col-12" },
            t.div(
                { className: "alert warning" },
                t.p({ className: "txt-bold" }, "The collections and records were restored, but the files were not all copied."),
                t.p(null, () => data.filesError),
            ),
        ),
        t.div(
            { className: "col-12 flex" },
            t.button(
                {
                    type: "button",
                    className: "btn transparent",
                    disabled: () => data.busy,
                    onclick: () => {
                        if (window.confirm("Finish setup without the missing files?")) {
                            filesAction(true);
                        }
                    },
                },
                t.span({ className: "txt" }, "Finish without the files"),
            ),
            t.button(
                {
                    type: "button",
                    className: () => `btn m-l-auto ${data.busy ? "loading" : ""}`,
                    disabled: () => data.busy,
                    onclick: () => filesAction(false),
                },
                t.span({ className: "txt" }, "Retry copying files"),
            ),
        ),
    );
}
