// Uni VPN for Windows: notification area icon plus the app window around the daemon's page.
// Role models: Cisco Secure Client and Mullvad VPN (left click on the icon opens the small
// fixed window, right click shows the menu, closing the window keeps the icon), Tailscale
// (menu with the state on top and Exit at the bottom).
// Built by "uni-vpn setup" with the csc.exe of the .NET Framework (C# 5) against the WebView2 SDK.
// Arguments: --port N, --hidden (sign-in: icon only), --page P ("#settings", "&user=ab123"),
// --after PID (the copy this one replaces after an update, which quits first).

using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.Drawing;
using System.Globalization;
using System.Drawing.Drawing2D;
using System.IO;
using System.IO.Pipes;
using System.Net;
using System.Security.AccessControl;
using System.Security.Principal;
using System.Text;
using System.Threading;
using System.Web.Script.Serialization;
using System.Windows.Forms;
using Microsoft.Web.WebView2.Core;
using Microsoft.Web.WebView2.WinForms;
using Microsoft.Win32;

static class Program
{
    // Taken at the start: an update moves the running file aside and puts the new one here.
    public static readonly string ExePath = Application.ExecutablePath;

    [System.Runtime.InteropServices.DllImport("user32.dll")]
    static extern bool SetProcessDPIAware();

    [System.Runtime.InteropServices.DllImport("kernel32.dll")]
    static extern bool GetNamedPipeServerSessionId(Microsoft.Win32.SafeHandles.SafePipeHandle pipe, out uint session);

    [System.Runtime.InteropServices.DllImport("kernel32.dll")]
    static extern bool GetNamedPipeServerProcessId(Microsoft.Win32.SafeHandles.SafePipeHandle pipe, out uint process);

    [System.Runtime.InteropServices.DllImport("user32.dll")]
    static extern bool AllowSetForegroundWindow(uint process);

    [STAThread]
    static void Main(string[] args)
    {
        // Sharp window and icon at 125 % and 150 % scaling instead of a stretched 96 dpi picture.
        try { SetProcessDPIAware(); } catch (Exception) { }
        int after;
        if (int.TryParse(Options.Value(args, "--after") ?? "", out after))
        {
            try { using (Process old = Process.GetProcessById(after)) old.WaitForExit(10000); }
            catch (Exception) { }
        }
        bool first;
        // Every account on the computer sees the same pipe names (fast user switching): this
        // one is this account's in this sign-in session.
        int session = Process.GetCurrentProcess().SessionId;
        string pipe;
        using (WindowsIdentity me = WindowsIdentity.GetCurrent()) pipe = "uni-vpn-app-" + me.User.Value + "-" + session;
        using (Mutex mutex = new Mutex(true, "Local\\uni-vpn-app", out first))
        {
            if (!first)
            {
                // Already running: hand over what to show and quit, like a second click on the icon.
                string page = Options.Page(args);
                if (page == null) return;
                // This version's name, then an earlier version's: that one runs until it restarts after an update.
                foreach (string name in new string[] { pipe, "uni-vpn-app-" + Environment.UserName })
                {
                    try
                    {
                        using (NamedPipeClientStream client = new NamedPipeClientStream(".", name, PipeDirection.Out))
                        {
                            client.Connect(name == pipe ? 3000 : 1000);
                            // Another account can open a pipe of this name first: it must not get the
                            // page (an otpauth link carries the key). Its programs run in its own session.
                            uint server;
                            if (GetNamedPipeServerSessionId(client.SafePipeHandle, out server) && server == (uint)session)
                            {
                                // This copy was started by a click: it may hand the running one the
                                // right to bring its window to the front (else it opens behind the browser).
                                uint owner;
                                if (GetNamedPipeServerProcessId(client.SafePipeHandle, out owner)) AllowSetForegroundWindow(owner);
                                byte[] data = Encoding.UTF8.GetBytes(page);
                                client.Write(data, 0, data.Length);
                            }
                            return;
                        }
                    }
                    catch (Exception) { }
                }
                return;
            }
            Application.EnableVisualStyles();
            Application.SetCompatibleTextRenderingDefault(false);
            Application.Run(new TrayApp(args, pipe));
        }
    }
}

static class Options
{
    public static string Value(string[] args, string name)
    {
        for (int i = 0; i + 1 < args.Length; i++)
        {
            if (args[i] == name) return args[i + 1];
        }
        return null;
    }

    // What to show: --page, or nothing at all with --hidden.
    public static string Page(string[] args)
    {
        // An otpauth:// link (the MFA portal's "follow this link"): the assistant reads its key.
        foreach (string arg in args)
            if (arg.StartsWith("otpauth:", StringComparison.OrdinalIgnoreCase)) return "#otpauth=" + Escape(arg);
        string page = Value(args, "--page");
        if (page != null) return page;
        return Array.IndexOf(args, "--hidden") >= 0 ? null : "";
    }

    // Percent-encodes everything but letters and digits (Uri.EscapeDataString keeps ' ( ) on
    // the .NET Framework's compatibility setting).
    static string Escape(string text)
    {
        StringBuilder result = new StringBuilder();
        foreach (byte b in Encoding.UTF8.GetBytes(text))
        {
            if ((b >= '0' && b <= '9') || (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')) result.Append((char)b);
            else result.Append('%').Append(b.ToString("X2"));
        }
        return result.ToString();
    }
}

class TrayApp : ApplicationContext
{
    readonly string baseUrl;
    readonly int port;
    readonly NotifyIcon icon = new NotifyIcon();
    readonly ToolStripMenuItem stateItem = new ToolStripMenuItem();
    readonly ToolStripMenuItem toggleItem = new ToolStripMenuItem();
    readonly ToolStripMenuItem openItem = new ToolStripMenuItem();
    readonly ToolStripMenuItem settingsItem = new ToolStripMenuItem();
    readonly ToolStripMenuItem exitItem = new ToolStripMenuItem();
    // English until the service answers with the menu in the user's language (status.json?menu=).
    static readonly Dictionary<string, string> texts = new Dictionary<string, string> {
        {"look.connected", "Connected"}, {"look.connecting", "Connecting"}, {"look.disconnecting", "Disconnecting"},
        {"look.idle", "Not connected"}, {"look.offline", "No network"}, {"look.blocked", "Paused"},
        {"look.auth_failed", "Sign-in failed"}, {"look.keyring", "Action needed"}, {"look.error", "Error"},
        {"menu.not_running", "Not running"}, {"menu.connect", "Connect"}, {"menu.disconnect", "Disconnect"},
        {"menu.open", "Open Uni VPN"}, {"menu.settings", "Settings"}, {"menu.exit", "Exit"},
    };
    readonly System.Windows.Forms.Timer timer = new System.Windows.Forms.Timer();
    readonly SynchronizationContext ui;
    MainWindow window;
    string state = "unknown";
    bool busy;
    bool reachable;
    bool polling;
    int misses;
    DateTime lastStart = DateTime.MinValue;
    string program = Stamp();
    string seen;
    bool restarting;

    public TrayApp(string[] args, string pipe)
    {
        if (!int.TryParse(Options.Value(args, "--port") ?? "", out port)) port = 1081;
        baseUrl = "http://127.0.0.1:" + port + "/";
        // Not SynchronizationContext.Current: the menu items above made it a plain context
        // (ToolStrip subscribes to SystemEvents), which runs Post on a thread pool thread, where
        // the window's WebView2 cannot start (RPC_E_CHANGED_MODE). This one posts to this thread.
        ui = new WindowsFormsSynchronizationContext();

        ContextMenuStrip menu = new ContextMenuStrip();
        stateItem.Enabled = false;
        toggleItem.Click += delegate { Toggle(); };
        menu.Items.Add(stateItem);
        menu.Items.Add(toggleItem);
        menu.Items.Add(new ToolStripSeparator());
        openItem.Font = new Font(openItem.Font, FontStyle.Bold);  // the default action, as on a double click
        openItem.Click += delegate { Show("#main"); };
        menu.Items.Add(openItem);
        settingsItem.Click += delegate { Show("#settings"); };
        menu.Items.Add(settingsItem);
        menu.Items.Add(new ToolStripSeparator());
        exitItem.Click += delegate { Exit(); };
        menu.Items.Add(exitItem);
        menu.Opening += delegate { Poll(); };
        icon.ContextMenuStrip = menu;
        icon.MouseClick += delegate(object sender, MouseEventArgs e) { if (e.Button == MouseButtons.Left) Show(""); };
        icon.Visible = true;
        Render();

        timer.Interval = 3000;
        timer.Tick += delegate { Poll(); };
        timer.Start();
        Poll();

        Thread listener = new Thread(delegate() { Listen(pipe); });
        listener.IsBackground = true;
        listener.Start();

        string page = Options.Page(args);
        if (page != null) Show(page);
    }

    void Listen(string pipe)
    {
        while (true)
        {
            try
            {
                // Only this account may open it or make another instance of it (by default every
                // account may read).
                PipeSecurity security = new PipeSecurity();
                using (WindowsIdentity me = WindowsIdentity.GetCurrent())
                    security.AddAccessRule(new PipeAccessRule(me.User, PipeAccessRights.FullControl, AccessControlType.Allow));
                using (NamedPipeServerStream server = new NamedPipeServerStream(pipe, PipeDirection.In, 1,
                    PipeTransmissionMode.Byte, PipeOptions.None, 0, 0, security))
                {
                    server.WaitForConnection();
                    using (StreamReader reader = new StreamReader(server, Encoding.UTF8))
                    {
                        string page = reader.ReadToEnd();
                        ui.Post(delegate { Show(page); }, null);
                    }
                }
            }
            catch (Exception)
            {
                Thread.Sleep(1000);
            }
        }
    }

    public void Show(string page)
    {
        if (window == null || window.IsDisposed) window = new MainWindow(baseUrl, page);
        // No WebView2 runtime: the window closed itself at once and the page is open in the browser.
        if (window.IsDisposed) return;
        window.Open(page);
    }

    void Exit()
    {
        icon.Visible = false;
        if (window != null) window.Quit();
        ExitThread();
    }

    WebClient Client()
    {
        WebClient client = new WebClient();
        client.Proxy = null;  // the system proxy is our own rule; loopback needs none
        client.Encoding = Encoding.UTF8;
        return client;
    }

    void Poll()
    {
        if (polling) return;
        polling = true;
        WebClient client = Client();
        client.DownloadStringCompleted += delegate(object sender, DownloadStringCompletedEventArgs e)
        {
            polling = false;
            reachable = e.Error == null && !e.Cancelled;
            if (reachable)
            {
                try
                {
                    Dictionary<string, object> json = new JavaScriptSerializer().Deserialize<Dictionary<string, object>>(e.Result);
                    object value;
                    state = json.TryGetValue("state", out value) ? Convert.ToString(value) : "unknown";
                    busy = json.TryGetValue("busy", out value) && value is bool && (bool)value;
                    if (json.TryGetValue("menu", out value) && value is Dictionary<string, object>)
                        foreach (KeyValuePair<string, object> entry in (Dictionary<string, object>)value)
                            texts[entry.Key] = Convert.ToString(entry.Value);
                }
                catch (Exception)
                {
                    reachable = false;
                }
            }
            client.Dispose();
            Render();
            StartServiceIfDown();
            RestartIfUpdated();
        };
        string languages = CultureInfo.CurrentUICulture.Name + "," + CultureInfo.InstalledUICulture.Name;
        // shell=1: this app restarts itself after updates (RestartIfUpdated).
        client.DownloadStringAsync(new Uri(baseUrl + "status.json?menu=" + Uri.EscapeDataString(languages) + "&shell=1"));
    }

    // Identifies the program file, which an update replaces.
    static string Stamp()
    {
        try
        {
            FileInfo file = new FileInfo(Program.ExePath);
            return file.Exists ? file.LastWriteTimeUtc.Ticks + "|" + file.Length : null;
        }
        catch (Exception) { return null; }
    }

    // After an update the new version takes over without a window, once this one's window is
    // closed (Chrome, Tailscale). The same file twice in a row: the update is complete.
    void RestartIfUpdated()
    {
        string now = Stamp();
        bool changed = now != null && now != program && now == seen;
        seen = now;
        if (!changed || restarting || (window != null && !window.IsDisposed && window.Visible)) return;
        restarting = true;
        try
        {
            ProcessStartInfo info = new ProcessStartInfo(Program.ExePath,
                "--port " + port + " --hidden --after " + Process.GetCurrentProcess().Id);
            info.UseShellExecute = false;
            Process.Start(info).Dispose();
        }
        catch (Exception)
        {
            // Stays as it is; tried again after the next update.
            program = now;
            restarting = false;
            return;
        }
        Exit();
    }

    // Nobody should need a terminal: a stopped service is started again from here.
    void StartServiceIfDown()
    {
        misses = reachable ? 0 : misses + 1;
        if (misses < 2 || (DateTime.Now - lastStart).TotalSeconds < 60) return;
        lastStart = DateTime.Now;
        try
        {
            ProcessStartInfo info = new ProcessStartInfo("schtasks.exe", "/Run /TN uni-vpn");
            info.CreateNoWindow = true;
            info.UseShellExecute = false;
            Process.Start(info).Dispose();
        }
        catch (Exception) { }
    }

    bool On { get { return state == "connected" || (busy && state != "disconnecting"); } }

    void Toggle()
    {
        WebClient client = Client();
        client.Headers.Add("X-Uni-VPN", "1");
        client.Headers.Add("Content-Type", "application/json");
        client.UploadStringCompleted += delegate { client.Dispose(); Poll(); };
        client.UploadStringAsync(new Uri(baseUrl + (On ? "api/disconnect" : "api/connect")), "POST", "{}");
    }

    static string T(string key)
    {
        string text;
        return texts.TryGetValue(key, out text) ? text : key;
    }

    static string Label(string state, bool reachable)
    {
        if (!reachable) return T("menu.not_running");
        string text;
        return texts.TryGetValue("look." + state, out text) ? text : state;
    }

    void Render()
    {
        string label = Label(state, reachable);
        stateItem.Text = label;
        toggleItem.Text = T(On ? "menu.disconnect" : "menu.connect");
        openItem.Text = T("menu.open");
        settingsItem.Text = T("menu.settings");
        exitItem.Text = T("menu.exit");
        toggleItem.Enabled = reachable && state != "disconnecting";
        icon.Text = "Uni VPN: " + label;
        Icon old = icon.Icon;
        icon.Icon = TrayIcon.Draw(state == "connected", reachable);
        if (old != null) TrayIcon.Destroy(old);
    }
}

// A shield like the app icon: filled when connected, outlined otherwise, white on a dark
// taskbar and black on a light one, like the network and volume icons next to it.
static class TrayIcon
{
    [System.Runtime.InteropServices.DllImport("user32.dll")]
    static extern bool DestroyIcon(IntPtr handle);

    static bool LightTaskbar()
    {
        using (RegistryKey key = Registry.CurrentUser.OpenSubKey(@"Software\Microsoft\Windows\CurrentVersion\Themes\Personalize"))
        {
            object value = key == null ? null : key.GetValue("SystemUsesLightTheme");
            return value is int && (int)value == 1;
        }
    }

    public static Icon Draw(bool filled, bool reachable)
    {
        int size = SystemInformation.SmallIconSize.Width;
        using (Bitmap bitmap = new Bitmap(size, size))
        using (Graphics g = Graphics.FromImage(bitmap))
        {
            g.SmoothingMode = SmoothingMode.AntiAlias;
            Color color = LightTaskbar() ? Color.Black : Color.White;
            if (!reachable) color = Color.FromArgb(128, color);
            float s = size / 16f;
            using (GraphicsPath path = new GraphicsPath())
            {
                path.AddLine(8 * s, 1.5f * s, 14 * s, 3.8f * s);
                path.AddLine(14 * s, 3.8f * s, 14 * s, 8 * s);
                path.AddBezier(14 * s, 8 * s, 14 * s, 11.5f * s, 11.5f * s, 13.6f * s, 8 * s, 15 * s);
                path.AddBezier(8 * s, 15 * s, 4.5f * s, 13.6f * s, 2 * s, 11.5f * s, 2 * s, 8 * s);
                path.AddLine(2 * s, 8 * s, 2 * s, 3.8f * s);
                path.CloseFigure();
                if (filled)
                {
                    using (Brush brush = new SolidBrush(color)) g.FillPath(brush, path);
                }
                else
                {
                    using (Pen pen = new Pen(color, 1.5f * s)) g.DrawPath(pen, path);
                }
            }
            return Icon.FromHandle(bitmap.GetHicon());
        }
    }

    public static void Destroy(Icon icon)
    {
        DestroyIcon(icon.Handle);
        icon.Dispose();
    }
}

class MainWindow : Form
{
    readonly string baseUrl;
    readonly WebView2 view = new WebView2();
    bool ready;
    bool loaded;
    bool quitting;
    string pending;
    string target;

    public MainWindow(string baseUrl, string page)
    {
        this.baseUrl = baseUrl;
        pending = page;
        Text = "Uni VPN";
        Icon = Icon.ExtractAssociatedIcon(Application.ExecutablePath);
        AutoScaleMode = AutoScaleMode.None;
        float scale;
        using (Graphics g = Graphics.FromHwnd(IntPtr.Zero)) scale = g.DpiX / 96F;
        ClientSize = new Size((int)(420 * scale), (int)(660 * scale));
        FormBorderStyle = FormBorderStyle.FixedSingle;
        MaximizeBox = false;
        StartPosition = FormStartPosition.CenterScreen;
        view.Dock = DockStyle.Fill;
        view.DefaultBackgroundColor = Color.Transparent;
        // Program Files is read only for the user: the browser profile goes next to the config.
        string data = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData), "uni-vpn", "WebView2");
        view.CreationProperties = new CoreWebView2CreationProperties();
        view.CreationProperties.UserDataFolder = data;
        Controls.Add(view);
        Init();
    }

    async void Init()
    {
        try
        {
            await view.EnsureCoreWebView2Async(null);
        }
        catch (Exception ex)
        {
            // No WebView2 runtime (Windows 10 without a current Edge): the page asked for in the
            // browser, but not an otpauth link's key (the browser's history would keep it). This
            // can happen before the constructor returns; Close then disposes the window.
            Note("WebView2 did not start (" + Thread.CurrentThread.GetApartmentState() + " thread): " + ex.GetType().FullName + " (0x" + ex.HResult.ToString("X8") + "): " + ex.Message);
            string page = pending ?? "";
            if (page.StartsWith("#otpauth=")) page = "";
            try { Process.Start(baseUrl + (page.StartsWith("&") ? "?" + page.Substring(1) : page)); }
            catch (Exception) { }
            quitting = true;
            Close();
            return;
        }
        CoreWebView2Settings settings = view.CoreWebView2.Settings;
        // On: Cut, Copy and Paste in text fields; the page blocks the menu everywhere else.
        settings.AreDefaultContextMenusEnabled = true;
        settings.AreDevToolsEnabled = false;
        settings.IsStatusBarEnabled = false;
        settings.IsZoomControlEnabled = false;
        settings.AreBrowserAcceleratorKeysEnabled = false;
        settings.IsPasswordAutosaveEnabled = false;
        settings.IsGeneralAutofillEnabled = false;
        settings.UserAgent = settings.UserAgent + " UniVPN";
        view.CoreWebView2.NavigationStarting += delegate(object sender, CoreWebView2NavigationStartingEventArgs e)
        {
            // Only web links: another scheme (a file, an app's own) would run without the browser's question.
            if (!Local(e.Uri)) { e.Cancel = true; if (e.Uri.StartsWith("http://") || e.Uri.StartsWith("https://")) Process.Start(e.Uri); }
        };
        view.CoreWebView2.NewWindowRequested += delegate(object sender, CoreWebView2NewWindowRequestedEventArgs e)
        {
            e.Handled = true;
            if (e.Uri.StartsWith("http://") || e.Uri.StartsWith("https://")) Process.Start(e.Uri);
        };
        // The service is still starting (sign-in) or restarting (update): try again shortly.
        // A link opened in the browser instead (NavigationStarting) leaves the page as it is.
        view.CoreWebView2.NavigationCompleted += delegate(object sender, CoreWebView2NavigationCompletedEventArgs e)
        {
            if (!e.IsSuccess && e.WebErrorStatus == CoreWebView2WebErrorStatus.OperationCanceled) return;
            // A page the service answered with an error (403: another account's service has the
            // port) reloads itself; retrying here as well would make it flicker.
            int status = 0;
            try { status = e.HttpStatusCode; } catch (Exception) { }  // older runtimes cannot say
            loaded = e.IsSuccess || status != 0;
            if (loaded || quitting) return;
            System.Windows.Forms.Timer retry = new System.Windows.Forms.Timer();
            retry.Interval = 2000;
            // Hidden, it stops; opening the window again loads the page (ShowPage, !loaded).
            retry.Tick += delegate { retry.Dispose(); if (Visible && !loaded && !quitting) view.CoreWebView2.Navigate(target); };
            retry.Start();
        };
        ready = true;
        ShowPage(pending ?? "");
    }

    // Why the window did not open, for a bug report: the latest line only, next to the service's log.
    static void Note(string text)
    {
        try
        {
            string dir = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData), "uni-vpn", "logs");
            Directory.CreateDirectory(dir);
            File.WriteAllText(Path.Combine(dir, "app.log"), DateTime.Now.ToString("yyyy-MM-dd HH:mm:ss", CultureInfo.InvariantCulture) + " " + text + Environment.NewLine);
        }
        catch (Exception) { }
    }

    bool Local(string uri)
    {
        return uri.StartsWith(baseUrl) || uri.StartsWith("about:") || uri.StartsWith("data:");
    }

    // page: "" (as it is), "#main", "#settings", or "&user=ab123&university=ethz" from setup.
    void ShowPage(string page)
    {
        if (!ready) { pending = page; return; }
        string current = view.Source == null ? "" : view.Source.ToString();
        if (loaded && current.StartsWith(baseUrl) && (page == "" || page.StartsWith("#")))
        {
            if (page != "") view.CoreWebView2.ExecuteScriptAsync("location.hash = " + new JavaScriptSerializer().Serialize(page));
            return;
        }
        // A failed page loads again as the page it was (not "?app=1" alone).
        if (!loaded && target != null && page == "") { view.CoreWebView2.Navigate(target); return; }
        target = baseUrl + "?app=1" + page;
        view.CoreWebView2.Navigate(target);
    }

    public void Open(string page)
    {
        ShowPage(page);
        if (!Visible) Show();
        if (WindowState == FormWindowState.Minimized) WindowState = FormWindowState.Normal;
        Activate();
    }

    public void Quit()
    {
        quitting = true;
        Close();
    }

    // Closing keeps the notification area icon, like Cisco Secure Client.
    protected override void OnFormClosing(FormClosingEventArgs e)
    {
        if (!quitting && e.CloseReason == CloseReason.UserClosing)
        {
            e.Cancel = true;
            Hide();
            return;
        }
        base.OnFormClosing(e);
    }
}
