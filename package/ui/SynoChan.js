// DSM desktop integration: registers SynoChan as a desktop application and shows its UI in a DSM window.
// DSM loads this file because ui/config lists it; the UI itself runs in an iframe inside the window.
// Browsers keep this file cached across package updates (DSM loads it with its own version number), so it
// is kept minimal and stable. Behaviour that may change lives in app/synochan.js, which is never cached.
Ext.namespace("SYNO.SDS.SynoChan.Utils");

Ext.apply(SYNO.SDS.SynoChan.Utils, function () {
    return {
        ApplicationWindow: null,

        getSynoToken: function () {
            try {
                if (SYNO.SDS.Session && SYNO.SDS.Session.SynoToken) {
                    return SYNO.SDS.Session.SynoToken;
                }
            } catch (e) { /* Not available in this DSM version. */ }
            try {
                if (typeof _S === "function") {
                    return _S("SynoToken") || "";
                }
            } catch (e) { /* Not available in this DSM version. */ }
            return "";
        },

        getMainURL: function () {
            // The timestamp keeps DSM and the browser from serving a cached page after an update.
            var src = "/webman/3rdparty/synochan/app/index.html?_ts=" + new Date().getTime();
            var token = SYNO.SDS.SynoChan.Utils.getSynoToken();
            if (token) {
                src += "&SynoToken=" + encodeURIComponent(token);
            }
            return src;
        },

        // The iframe is a component, so the window's fit layout sets its pixel size on every resize and
        // maximize. Plain html content would keep the size it had when the window opened.
        createFrame: function () {
            return new Ext.BoxComponent({
                autoEl: {
                    tag: "iframe",
                    src: SYNO.SDS.SynoChan.Utils.getMainURL(),
                    title: "SynoChan",
                    frameborder: "0",
                    style: "display: block; border: 0; margin: 0;"
                }
            });
        }
    };
}());

Ext.define("SYNO.SDS.SynoChan.Application", {
    extend: "SYNO.SDS.AppInstance",
    appWindowName: "SYNO.SDS.SynoChan.MainWindow",
    constructor: function () {
        this.callParent(arguments);
    }
});

Ext.define("SYNO.SDS.SynoChan.MainWindow", {
    extend: "SYNO.SDS.AppWindow",

    constructor: function (config) {
        var utils = SYNO.SDS.SynoChan.Utils;
        this.appInstance = config.appInstance;
        config = Ext.apply({
            layout: "fit",
            resizable: true,
            maximizable: true,
            minimizable: true,
            width: 1040,
            height: 660,
            // DSM's "?" button opens an empty help page for packages without a help entry.
            showHelp: false,
            items: [utils.createFrame()]
        }, config);
        // callParent runs SYNO.SDS.AppWindow's own constructor. Calling "superclass.constructor" directly can
        // skip it, leaving the window without parts DSM's maximize and resize code relies on.
        this.callParent([config]);
        utils.ApplicationWindow = this;
        this.on("destroy", function () {
            if (utils.ApplicationWindow === this) {
                utils.ApplicationWindow = null;
            }
        }, this);
    }
});
