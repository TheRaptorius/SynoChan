// DSM desktop integration: registers SynoChan as a desktop application and shows its UI in a DSM window.
// DSM loads this file because ui/config lists it; the UI itself runs in an iframe inside the window.
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
        var ns = SYNO.SDS.SynoChan;
        this.appInstance = config.appInstance;
        // Window options match packages known to work on DSM 7.2+ ("syno-app-win" is the class DSM's own app
        // windows use).
        ns.MainWindow.superclass.constructor.call(this, Ext.apply({
            layout: "fit",
            resizable: true,
            cls: "syno-app-win",
            maximizable: true,
            minimizable: true,
            width: 1040,
            height: 660,
            items: [ns.Utils.createFrame()]
        }, config));
        ns.Utils.ApplicationWindow = this;
    },

    onOpen: function () {
        SYNO.SDS.SynoChan.MainWindow.superclass.onOpen.apply(this, arguments);
    },

    onRequest: function (request) {
        SYNO.SDS.SynoChan.MainWindow.superclass.onRequest.call(this, request);
    },

    onClose: function () {
        SYNO.SDS.SynoChan.Utils.ApplicationWindow = null;
        SYNO.SDS.SynoChan.MainWindow.superclass.onClose.apply(this, arguments);
        this.doClose();
        return true;
    }
});
