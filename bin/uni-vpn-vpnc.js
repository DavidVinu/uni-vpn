// openconnect runs this on Windows as: cscript.exe "uni-vpn-vpnc.js" (9.12; later versions add /e:JScript)
// It configures the Wintun adapter so that only sockets bound to its address use it:
// address without gateway, no DNS, a default route with a metric no other route loses to.
// Address and DNS servers go to %UNI_VPN_STATE% for the uni-vpn SOCKS server, or ERROR=...
// when the adapter could not be set up (openconnect ignores the exit code and our output).
// UNI_VPN_DRY=1 prints the commands instead of running them (tests).
var shell = WScript.CreateObject("WScript.Shell");
var procEnv = shell.Environment("Process");
var fso = WScript.CreateObject("Scripting.FileSystemObject");
var dry = procEnv("UNI_VPN_DRY") == "1";
var ROUTE_METRIC = 9000;

function env(name) {
    return procEnv(name);
}

var failure = "";

function run(cmd, quiet) {
    if (dry) {
        WScript.Echo("RUN " + cmd);
        return procEnv("UNI_VPN_DRY_FAIL") && cmd.indexOf(procEnv("UNI_VPN_DRY_FAIL")) >= 0 ? 1 : 0;
    }
    var exec = shell.Exec(procEnv("ComSpec") + " /C \"" + cmd + "\" 2>&1");
    exec.StdIn.Close();
    var output = exec.StdOut.ReadAll();
    while (exec.Status == 0) {
        WScript.Sleep(20);
    }
    if (exec.ExitCode != 0 && !quiet) {
        WScript.Echo("uni-vpn-vpnc: \"" + cmd + "\" failed (" + exec.ExitCode + "): " + output);
    }
    return exec.ExitCode;
}

// The commands without which the SOCKS server would have no working address.
function must(cmd) {
    if (run(cmd) != 0 && !failure) {
        failure = cmd + " failed";
    }
}

function writeState(path) {
    var keys = ["INTERNAL_IP4_ADDRESS", "INTERNAL_IP4_NETMASK", "INTERNAL_IP4_DNS", "INTERNAL_IP4_MTU",
                "CISCO_DEF_DOMAIN", "TUNDEV", "TUNIDX", "VPNGATEWAY"];
    var tmp = path + ".tmp";
    var file = fso.CreateTextFile(tmp, true, false);
    if (failure) {
        file.WriteLine("ERROR=" + failure);
    }
    for (var i = 0; i < keys.length; i++) {
        file.WriteLine(keys[i] + "=" + env(keys[i]));
    }
    file.Close();
    if (fso.FileExists(path)) {
        fso.DeleteFile(path, true);
    }
    fso.MoveFile(tmp, path);
}

var idx = env("TUNIDX");
var state = env("UNI_VPN_STATE");

switch (env("reason")) {
case "connect":
case "reconnect":
    if (!idx || !env("INTERNAL_IP4_ADDRESS")) {
        failure = "openconnect reported no interface index or IPv4 address";
        if (state) {
            writeState(state);
        }
        WScript.Quit(1);
    }
    var mask = env("INTERNAL_IP4_NETMASK") || "255.255.255.255";
    if (env("INTERNAL_IP4_MTU")) {
        run("netsh interface ipv4 set subinterface " + idx + " mtu=" + env("INTERNAL_IP4_MTU") + " store=active");
    }
    must("netsh interface ipv4 set interface " + idx + " metric=" + ROUTE_METRIC + " store=active");
    // No duplicate address detection: a tentative address cannot be bound for a moment.
    run("netsh interface ipv4 set interface " + idx + " dadtransmits=0 store=active", true);
    must("netsh interface ipv4 set address " + idx + " static " + env("INTERNAL_IP4_ADDRESS") + " " + mask + " store=active");
    run("netsh interface ipv4 delete dnsservers " + idx + " all", true);
    run("netsh interface ipv4 delete route 0.0.0.0/0 " + idx + " store=active", true);
    must("netsh interface ipv4 add route 0.0.0.0/0 " + idx + " metric=" + ROUTE_METRIC + " store=active");
    if (state) {
        writeState(state);
    }
    WScript.Quit(failure ? 1 : 0);
    break;
case "disconnect":
    if (state && fso.FileExists(state)) {
        fso.DeleteFile(state, true);
    }
    break;
default:
    // pre-init, attempt-reconnect: nothing to do
    break;
}
WScript.Quit(0);
