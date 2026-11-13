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
            SYNO.SDS.SynoChan.Utils.installDragGuard();
            return new Ext.BoxComponent({
                autoEl: {
                    tag: "iframe",
                    src: SYNO.SDS.SynoChan.Utils.getMainURL(),
                    title: "SynoChan",
                    frameborder: "0",
                    "data-synochan-frame": "1",
                    style: "display: block; border: 0; margin: 0;"
                }
            });
        },

        // An iframe swallows mouse events. When a DSM window is dragged or resized across it, DSM never sees the
        // mouse move or the button release, and the drag gets stuck. So while any mouse button is held down in
        // DSM itself, the iframe ignores the mouse. Clicks inside the iframe never reach this document and
        // are unaffected.
        installDragGuard: function () {
            var utils = SYNO.SDS.SynoChan.Utils;
            if (utils.dragGuardInstalled) {
                return;
            }
            utils.dragGuardInstalled = true;
            var disabled = false;
            var setEnabled = function (enabled) {
                if (disabled === !enabled) {
                    return;
                }
                disabled = !enabled;
                var frames = document.querySelectorAll("iframe[data-synochan-frame]");
                for (var i = 0; i < frames.length; i++) {
                    frames[i].style.pointerEvents = enabled ? "" : "none";
                }
            };
            var disable = function () { setEnabled(false); };
            var enable = function () { setEnabled(true); };
            document.addEventListener("mousedown", disable, true);
            document.addEventListener("touchstart", disable, true);
            document.addEventListener("mouseup", enable, true);
            document.addEventListener("touchend", enable, true);
            document.addEventListener("touchcancel", enable, true);
            window.addEventListener("blur", enable);
            // A button released outside the browser sends no mouseup; the next move without buttons restores it.
            document.addEventListener("mousemove", function (e) {
                if (disabled && e.buttons === 0) {
                    enable();
                }
            }, true);
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
