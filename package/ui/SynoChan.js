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

        getMainHtml: function () {
            // The timestamp keeps DSM and the browser from serving a cached page after an update.
            var src = "/webman/3rdparty/synochan/app/index.html?_ts=" + new Date().getTime();
            var token = SYNO.SDS.SynoChan.Utils.getSynoToken();
            if (token) {
                src += "&SynoToken=" + encodeURIComponent(token);
            }
            return '<iframe src="' + src + '" title="SynoChan" ' +
                'style="display: block; width: 100%; height: 100%; border: none; margin: 0;"></iframe>';
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
        ns.MainWindow.superclass.constructor.call(this, Ext.apply({
            layout: "fit",
            resizable: true,
            maximizable: true,
            minimizable: true,
            width: 1040,
            height: 660,
            minWidth: 640,
            minHeight: 400,
            cls: "synochan-window",
            html: ns.Utils.getMainHtml()
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
