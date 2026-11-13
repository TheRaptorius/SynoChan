#!/bin/sh
# DSM runs this for /webman/3rdparty/synochan/api.cgi. The binary checks the DSM login and
# forwards the call to the SynoChan service.
exec /var/packages/synochan/target/bin/synochan cgi
