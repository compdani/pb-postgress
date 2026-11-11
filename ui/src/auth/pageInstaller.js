// The route token is the one-time installer token printed by the server.
// Every installer request sends it in the X-Installer-Token header.
import { joinStep } from "./installerJoin";

let installerToken = "";

export function pageInstaller(route) {
    installerToken = route.params?.token || "";

    if (!installerToken) {
        app.toasts.error("The installer link is missing its token.");
        window.location.hash = "#/";
        return;
    }

    app.store.title = "Setup your PgBase instance";

    const data = store({
        ready: false,
        step: 1,
        driver: "postgres",
        dsn: "",
        email: "",
        password: "",
        passwordConfirm: "",
        showPassword: false,
        showPasswordConfirm: false,
        fileMode: "local",
        localURL: window.location.origin,
        remoteURL: "",
        s3: {
            endpoint: "",
            bucket: "",
            region: "",
            accessKey: "",
            secret: "",
            forcePathStyle: false,
        },
        instances: [],
        filesPending: false,
        isTestingDb: false,
        isConnecting: false,
        isTestingS3: false,
        isSubmitting: false,
        dbTestMessage: "",
        s3TestMessage: "",
        formError: "",
        get isBusy() {
            return data.isTestingDb || data.isConnecting || data.isTestingS3 || data.isSubmitting;
        },
    });

    loadStatus();

    async function loadStatus() {
        try {
            const status = await app.pb.send("/api/installer/status", {
                method: "GET",
                headers: { "X-Installer-Token": installerToken },
                requestKey: "installer_status",
            });
            if (status?.installed) {
                app.toasts.info("This instance is already set up.");
                window.location.hash = "#/login";
                return;
            }
            if (status?.joinPending) {
                data.instances = status.instances || [];
                data.step = "join";
                data.filesPending = !!status.filesPending;
            } else if (status?.dbConnected) {
                data.step = 2;
            }
            if (status?.fileMode === "local" || status?.fileMode === "fileServer" || status?.fileMode === "s3") {
                data.fileMode = status.fileMode;
            }
            const serverURL = status?.fileServer?.url || "";
            if (serverURL && status?.fileMode === "fileServer") {
                data.remoteURL = serverURL;
            } else if (serverURL && data.fileMode === "local") {
                data.localURL = serverURL;
            }
        } catch (err) {
            if (!err?.isAbort && err?.status && err.status !== 404) {
                data.formError = apiErrorMessage(err, "Could not load installer status.");
            }
        }
        data.ready = true;
    }

    async function testDb() {
        if (data.isBusy) {
            return;
        }
        data.formError = "";
        data.dbTestMessage = "";
        data.isTestingDb = true;
        try {
            const result = await installerPost("/api/installer/test-db", {
                driver: data.driver,
                dsn: data.dsn,
            });
            data.dbTestMessage = result?.error && result.ok === false
                ? result.error
                : "Connection succeeded.";
        } catch (err) {
            data.formError = apiErrorMessage(err, "Database connection failed.");
        }
        data.isTestingDb = false;
    }

    async function connectDb(e) {
        e?.preventDefault();
        if (data.isBusy) {
            return;
        }
        data.formError = "";
        data.isConnecting = true;
        try {
            const result = await installerPost("/api/installer/connect", {
                driver: data.driver,
                dsn: data.dsn,
            });
            if (result?.existing) {
                // Another instance already set this database up: pick this
                // instance's role instead of creating a superuser.
                const status = await app.pb.send("/api/installer/status", {
                    method: "GET",
                    headers: { "X-Installer-Token": installerToken },
                    requestKey: "installer_status",
                });
                data.instances = status?.instances || [];
                data.step = "join";
                data.isConnecting = false;
                return;
            }
            data.step = 2;
        } catch (err) {
            data.formError = apiErrorMessage(err, "Database connection failed.");
        }
        data.isConnecting = false;
    }

    function continueToFiles(e) {
        e?.preventDefault();
        data.formError = "";
        if (!data.email.trim()) {
            data.formError = "Email is required.";
            return;
        }
        if ((data.password || "").length < 10) {
            data.formError = "Password must be at least 10 characters.";
            return;
        }
        if (data.password !== data.passwordConfirm) {
            data.formError = "Password confirmation doesn't match.";
            return;
        }
        data.step = 3;
    }

    async function testS3() {
        if (data.isBusy) {
            return;
        }
        data.formError = "";
        data.s3TestMessage = "";
        data.isTestingS3 = true;
        try {
            await installerPost("/api/installer/test-s3", s3Payload());
            data.s3TestMessage = "S3 connection succeeded.";
        } catch (err) {
            data.formError = apiErrorMessage(err, "S3 connection failed.");
        }
        data.isTestingS3 = false;
    }

    async function complete(e) {
        e?.preventDefault();
        if (data.isBusy) {
            return;
        }
        data.formError = "";
        const fileServerUrl = data.fileMode === "local"
            ? data.localURL.trim()
            : data.fileMode === "fileServer"
            ? data.remoteURL.trim()
            : "";
        if (data.fileMode !== "s3" && !fileServerUrl) {
            data.formError = "A file server URL is required.";
            return;
        }
        if (data.fileMode === "s3" && (!data.s3.endpoint.trim() || !data.s3.bucket.trim())) {
            data.formError = "S3 endpoint and bucket are required.";
            return;
        }

        data.isSubmitting = true;
        try {
            await installerPost("/api/installer/complete", {
                email: data.email.trim(),
                password: data.password,
                fileMode: data.fileMode,
                isFileServer: data.fileMode === "local",
                fileServerUrl: fileServerUrl,
                s3: s3Payload(),
            });
            await reloadIntoLogin();
        } catch (err) {
            data.formError = apiErrorMessage(err, "Could not finish setup.");
        }
        data.isSubmitting = false;
    }

    function s3Payload() {
        return {
            endpoint: data.s3.endpoint.trim(),
            bucket: data.s3.bucket.trim(),
            region: data.s3.region.trim(),
            accessKey: data.s3.accessKey.trim(),
            secret: data.s3.secret,
            forcePathStyle: !!data.s3.forcePathStyle,
        };
    }

    return t.div(
        {
            pbEvent: "pageInstaller",
            className: "wrapper sm m-auto p-b-base",
        },
        t.header(
            { className: "txt-center m-b-base" },
            t.img({ className: "main-logo", src: () => app.store.mainLogo, ariaHidden: true, alt: "App logo" }),
            t.h5({ className: "m-t-10" }, () => app.store.title),
            t.div(
                { className: "txt-hint m-t-5" },
                () => data.step === "join" ? "Join an existing database" : `Step ${data.step} of 3`,
            ),
        ),
        () => {
            if (!data.ready) {
                return t.div({ className: "block txt-center" }, t.span({ className: "loader lg" }));
            }
            if (data.step === 1) {
                return databaseStep(data, testDb, connectDb);
            }
            if (data.step === "join") {
                return joinStep(data.instances, {
                    post: (path, body) => installerPost(path, body),
                    get: (path) =>
                        app.pb.send(path, {
                            method: "GET",
                            headers: { "X-Installer-Token": installerToken },
                            requestKey: "installer_" + path,
                        }),
                    upload: (file) => {
                        const form = new FormData();
                        form.append("file", file);
                        return app.pb.send("/api/installer/backup/upload", {
                            method: "POST",
                            headers: { "X-Installer-Token": installerToken },
                            body: form,
                            requestKey: null,
                        });
                    },
                    error: apiErrorMessage,
                    done: reloadIntoLogin,
                }, data.filesPending ? "files" : "role");
            }
            if (data.step === 2) {
                return superuserStep(data, continueToFiles);
            }
            return fileStep(data, testS3, complete);
        },
    );
}

// After setup the server swaps its installer listener for the full API
// (which may move to HTTPS when serving a domain). Wait until it answers, then
// do a full page load of the login page so the browser follows any redirect
// to the new origin. The query string forces a reload rather than a hash change.
async function reloadIntoLogin() {
    for (let attempt = 0; attempt < 40; attempt++) {
        try {
            await app.pb.health.check({ requestKey: null });
            break;
        } catch (_) {
            await new Promise((resolve) => setTimeout(resolve, 250));
        }
    }
    window.location.replace(window.location.pathname + "?setup=done#/login");
}

async function installerPost(path, body) {
    const result = await app.pb.send(path, {
        method: "POST",
        headers: { "X-Installer-Token": installerToken },
        body,
        requestKey: "installer_" + path,
    });
    if (result && result.ok === false) {
        throw new Error(result.error || "Request failed.");
    }
    return result;
}

function apiErrorMessage(err, fallback) {
    const body = err?.response || {};
    return body.error || body.message || err?.message || fallback;
}

function errorAlert(data) {
    return () => {
        if (!data.formError) {
            return;
        }
        return t.div({ className: "col-12" }, t.div({ className: "alert danger" }, data.formError));
    };
}

function databaseStep(data, testDb, connectDb) {
    return t.form(
        {
            pbEvent: "installerDatabaseForm",
            className: "grid installer-form",
            onsubmit: connectDb,
        },
        t.div({ className: "col-12 txt-center" }, "Connect this instance to PostgreSQL or MySQL."),
        errorAlert(data),
        t.div(
            { className: "col-12" },
            t.div(
                { className: "field" },
                t.label({ htmlFor: "installer_driver" }, "Database driver"),
                t.select(
                    {
                        id: "installer_driver",
                        name: "driver",
                        required: true,
                        disabled: () => data.isBusy,
                        value: () => data.driver,
                        onchange: (e) => (data.driver = e.target.value),
                    },
                    t.option({ value: "postgres" }, "PostgreSQL"),
                    t.option({ value: "mysql" }, "MySQL"),
                ),
            ),
        ),
        t.div(
            { className: "col-12" },
            t.div(
                { className: "field" },
                t.label({ htmlFor: "installer_dsn" }, "DSN"),
                t.input({
                    id: "installer_dsn",
                    name: "dsn",
                    type: "text",
                    required: true,
                    autocomplete: "off",
                    disabled: () => data.isBusy,
                    placeholder: () =>
                        data.driver === "mysql"
                            ? "user:password@tcp(127.0.0.1:3306)/pgbase"
                            : "postgres://user:password@127.0.0.1:5432/pgbase?sslmode=disable",
                    value: () => data.dsn,
                    oninput: (e) => (data.dsn = e.target.value),
                }),
            ),
        ),
        () => {
            if (!data.dbTestMessage) {
                return;
            }
            return t.div({ className: "col-12" }, t.div({ className: "alert info" }, data.dbTestMessage));
        },
        t.div(
            { className: "col-12" },
            t.div(
                { className: "flex" },
                t.button(
                    {
                        type: "button",
                        className: () => `btn secondary ${data.isTestingDb ? "loading" : ""}`,
                        disabled: () => data.isBusy,
                        onclick: testDb,
                    },
                    t.span({ className: "txt" }, "Test connection"),
                ),
                t.button(
                    {
                        className: () => `btn lg next ${data.isConnecting ? "loading" : ""}`,
                        disabled: () => data.isBusy,
                    },
                    t.span({ className: "txt" }, "Continue"),
                    t.i({ className: "ri-arrow-right-line", ariaHidden: true }),
                ),
            ),
        ),
    );
}

function superuserStep(data, onsubmit) {
    return t.form(
        {
            pbEvent: "installerForm",
            className: "grid installer-form",
            onsubmit,
        },
        t.div({ className: "col-12 txt-center" }, "Create your first superuser account."),
        errorAlert(data),
        t.div(
            { className: "col-12" },
            t.div(
                { className: "field" },
                t.label({ htmlFor: "superuser_email" }, "Email"),
                t.input({
                    id: "superuser_email",
                    name: "email",
                    type: "email",
                    required: true,
                    autofocus: true,
                    autocomplete: "off",
                    disabled: () => data.isBusy,
                    value: () => data.email,
                    oninput: (e) => (data.email = e.target.value),
                }),
            ),
        ),
        passwordField(data, {
            id: "superuser_password",
            label: "Password",
            showKey: "showPassword",
            valueKey: "password",
        }),
        t.div({ className: "col-12 field-help" }, "Recommended at least 10 characters."),
        passwordField(data, {
            id: "superuser_password_confirm",
            label: "Password confirm",
            showKey: "showPasswordConfirm",
            valueKey: "passwordConfirm",
        }),
        t.div(
            { className: "col-12" },
            t.div(
                { className: "flex" },
                t.button(
                    {
                        type: "button",
                        className: "btn secondary",
                        disabled: () => data.isBusy,
                        onclick: () => {
                            data.formError = "";
                            data.step = 1;
                        },
                    },
                    t.span({ className: "txt" }, "Back"),
                ),
                t.button(
                    { className: "btn lg next", disabled: () => data.isBusy },
                    t.span({ className: "txt" }, "Continue"),
                    t.i({ className: "ri-arrow-right-line", ariaHidden: true }),
                ),
            ),
        ),
    );
}

function passwordField(data, opts) {
    return t.div(
        { className: "col-12" },
        t.div(
            { className: "fields" },
            t.div(
                { className: "field" },
                t.label({ htmlFor: opts.id }, opts.label),
                t.input({
                    id: opts.id,
                    name: opts.valueKey,
                    required: true,
                    disabled: () => data.isBusy,
                    type: () => (data[opts.showKey] ? "text" : "password"),
                    value: () => data[opts.valueKey],
                    oninput: (e) => (data[opts.valueKey] = e.target.value),
                }),
            ),
            t.div(
                { className: "field addon" },
                t.button(
                    {
                        type: "button",
                        tabIndex: -1,
                        className: "btn sm transparent secondary circle tooltip-right",
                        ariaLabel: app.attrs.tooltip(() => (data[opts.showKey] ? "Hide password" : "Show password")),
                        onclick: () => (data[opts.showKey] = !data[opts.showKey]),
                    },
                    t.i({
                        className: () => (data[opts.showKey] ? "ri-eye-off-line" : "ri-eye-line"),
                        ariaHidden: true,
                    }),
                ),
            ),
        ),
    );
}

function fileStep(data, testS3, onsubmit) {
    const modes = [
        {
            value: "local",
            label: "Store files on this instance (this is the file serving instance)",
        },
        {
            value: "fileServer",
            label: "Files are served by another instance/bucket",
        },
        {
            value: "s3",
            label: "Use S3",
        },
    ];

    return t.form(
        {
            pbEvent: "installerFilesForm",
            className: "grid installer-form",
            onsubmit,
        },
        t.div(
            { className: "col-12" },
            t.div(
                { className: "alert info" },
                "One file storage mode applies to the whole database.",
            ),
        ),
        errorAlert(data),
        t.div(
            { className: "col-12" },
            modes.map((mode) => {
                return t.div(
                    { className: "field m-b-10" },
                    t.input({
                        type: "radio",
                        id: "installer_file_mode_" + mode.value,
                        name: "fileMode",
                        disabled: () => data.isBusy,
                        checked: () => data.fileMode === mode.value,
                        onchange: () => (data.fileMode = mode.value),
                    }),
                    t.label({ htmlFor: "installer_file_mode_" + mode.value }, mode.label),
                );
            }),
        ),
        () => {
            if (data.fileMode === "local") {
                return urlField(data, "installer_local_url", "Public URL", "localURL");
            }
            if (data.fileMode === "fileServer") {
                return urlField(data, "installer_remote_url", "File server URL", "remoteURL");
            }
            return s3Fields(data, testS3);
        },
        t.div(
            { className: "col-12" },
            t.div(
                { className: "flex" },
                t.button(
                    {
                        type: "button",
                        className: "btn secondary",
                        disabled: () => data.isBusy,
                        onclick: () => {
                            data.formError = "";
                            data.step = 2;
                        },
                    },
                    t.span({ className: "txt" }, "Back"),
                ),
                t.button(
                    {
                        className: () => `btn lg next ${data.isSubmitting ? "loading" : ""}`,
                        disabled: () => data.isBusy,
                    },
                    t.span({ className: "txt" }, "Create superuser and finish"),
                    t.i({ className: "ri-arrow-right-line", ariaHidden: true }),
                ),
            ),
        ),
    );
}

function urlField(data, id, label, key) {
    return t.div(
        { className: "col-12" },
        t.div(
            { className: "field" },
            t.label({ htmlFor: id }, label),
            t.input({
                id,
                type: "url",
                required: true,
                disabled: () => data.isBusy,
                value: () => data[key],
                oninput: (e) => (data[key] = e.target.value),
            }),
        ),
    );
}

function s3Fields(data, testS3) {
    const fields = [
        ["endpoint", "Endpoint", "text", "installer_s3_endpoint"],
        ["bucket", "Bucket", "text", "installer_s3_bucket"],
        ["region", "Region", "text", "installer_s3_region"],
        ["accessKey", "Access key", "text", "installer_s3_access"],
        ["secret", "Secret", "password", "installer_s3_secret"],
    ];

    return t.div(
        { className: "col-12 grid" },
        fields.map(([key, label, type, id]) => {
            return t.div(
                { className: "col-12" },
                t.div(
                    { className: "field" },
                    t.label({ htmlFor: id }, label),
                    t.input({
                        id,
                        type,
                        autocomplete: "off",
                        required: key === "endpoint" || key === "bucket",
                        disabled: () => data.isBusy,
                        value: () => data.s3[key],
                        oninput: (e) => (data.s3[key] = e.target.value),
                    }),
                ),
            );
        }),
        t.div(
            { className: "col-12" },
            t.div(
                { className: "field" },
                t.input({
                    id: "installer_s3_path_style",
                    type: "checkbox",
                    disabled: () => data.isBusy,
                    checked: () => !!data.s3.forcePathStyle,
                    onchange: (e) => (data.s3.forcePathStyle = e.target.checked),
                }),
                t.label({ htmlFor: "installer_s3_path_style" }, "Force path-style addressing"),
            ),
        ),
        () => {
            if (!data.s3TestMessage) {
                return;
            }
            return t.div({ className: "col-12" }, t.div({ className: "alert info" }, data.s3TestMessage));
        },
        t.div(
            { className: "col-12" },
            t.button(
                {
                    type: "button",
                    className: () => `btn secondary ${data.isTestingS3 ? "loading" : ""}`,
                    disabled: () => data.isBusy,
                    onclick: testS3,
                },
                t.span({ className: "txt" }, "Test S3"),
            ),
        ),
    );
}
