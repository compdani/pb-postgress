export function backupsForm(propsArg = {}) {
    const props = store({
        onsave: null,
    });

    const watchers = app.utils.extendStore(props, propsArg);

    const data = store({
        showForm: false,
        isLoading: false,
        isSaving: false,
        formSettings: null,
        initSerialized: "null",
        get hasChanges() {
            return data.initSerialized != JSON.stringify(data.formSettings);
        },
    });

    async function loadSettings() {
        data.isLoading = true;

        try {
            const settings = await app.pb.settings.getAll();
            init(settings);

            data.isLoading = false;
        } catch (err) {
            if (!err.isAbort) {
                app.checkApiError(err);
                // data.isLoading = false; don't reset in case of a server error
            }
        }
    }

    async function save() {
        if (data.isSaving || !data.hasChanges) {
            return;
        }

        data.isSaving = true;

        try {
            const redacted = app.utils.filterRedactedProps(data.formSettings);
            const settings = await app.pb.settings.update(redacted);

            props.onsave?.(settings);

            init(settings);

            app.toasts.success("Successfully saved backups settings.");
        } catch (err) {
            app.checkApiError(err);
        }

        data.isSaving = false;
    }

    function init(settings = {}) {
        // refresh local app settings
        app.store.settings = JSON.parse(JSON.stringify(settings));

        data.formSettings = {
            backups: { s3: settings?.backups?.s3 || {} },
        };

        data.initSerialized = JSON.stringify(data.formSettings);
    }

    function reset() {
        data.formSettings = JSON.parse(data.initSerialized);
    }

    return t.div(
        {
            className: "block backups-settings-form-wrapper",
            onmount: () => {
                loadSettings();
            },
            onunmount: () => {
                watchers.forEach((w) => w?.unwatch());
            },
        },
        t.button(
            {
                type: "button",
                className: () => `btn secondary ${data.isLoading ? "loading" : ""}`,
                disabled: () => data.isLoading || data.hasChanges,
                onclick: () => (data.showForm = !data.showForm),
            },
            t.span({ className: "txt" }, "Backup options"),
            t.i({
                className: () => (data.showForm ? "ri-arrow-up-s-line" : "ri-arrow-down-s-line"),
                ariaHidden: true,
            }),
        ),
        app.components.slide(
            () => data.showForm,
            t.form(
                {
                    pbEvent: "backupsSettingsForm",
                    className: "grid backups-settings-form m-t-base",
                    inert: () => data.isSaving,
                    onsubmit: (e) => {
                        e.preventDefault();
                        save();
                    },
                },
                () => {
                    if (data.isLoading) {
                        return t.div({ className: "col-lg-12 txt-center" }, t.span({ className: "loader lg" }));
                    }

                    return [
                        t.div(
                            { className: "col-lg-12" },
                            t.p(
                                { className: "txt-hint" },
                                "Backups are created on demand. Scheduled backups belong to your PostgreSQL or MySQL server (base backups, point-in-time recovery, binlogs or managed snapshots).",
                            ),
                        ),
                        t.div(
                            { className: "col-lg-12" },
                            app.components.s3ConfigFields({
                                toggleLabel: "Store backups in S3 storage",
                                testFilesystem: "backups",
                                config: () => data.formSettings.backups.s3,
                            }),
                        ),
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
                    ];
                },
            ),
        ),
    );
}
