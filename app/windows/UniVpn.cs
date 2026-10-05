// Uni VPN for Windows: notification area icon plus the app window around the daemon's page.
// Role models: Cisco Secure Client and Mullvad VPN (left click on the icon opens the small
// fixed window, right click shows the menu, closing the window keeps the icon), Tailscale
// (menu with the state on top and Exit at the bottom).
// Built by "uni-vpn setup" with the csc.exe of the .NET Framework (C# 5) against the WebView2 SDK.
// Arguments: --port N, --hidden (sign-in: icon only), --page P ("#settings", "&user=ab123").

using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.Drawing;
using System.Drawing.Drawing2D;
using System.IO;
using System.IO.Pipes;
using System.Net;
using System.Text;
using System.Threading;
using System.Web.Script.Serialization;
using System.Windows.Forms;
using Microsoft.Web.WebView2.Core;
using Microsoft.Web.WebView2.WinForms;
using Microsoft.Win32;

static class Program
{
    [STAThread]
    static void Main(string[] args)
    {
        bool first;
        string pipe = "uni-vpn-app-" + Environment.UserName;
        using (Mutex mutex = new Mutex(true, "Local\\uni-vpn-app", out first))
        {
            if (!first)
            {
                // Already running: hand over what to show and quit, like a second click on the icon.
                string page = Options.Page(args);
                if (page == null) return;
                try
                {
                    using (NamedPipeClientStream client = new NamedPipeClientStream(".", pipe, PipeDirection.Out))
                    {
                        client.Connect(3000);
                        byte[] data = Encoding.UTF8.GetBytes(page);
                        client.Write(data, 0, data.Length);
                    }
                }
                catch (Exception) { }
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
        string page = Value(args, "--page");
        if (page != null) return page;
        return Array.IndexOf(args, "--hidden") >= 0 ? null : "";
    }
}

class TrayApp : ApplicationContext
{
    readonly string baseUrl;
    readonly NotifyIcon icon = new NotifyIcon();
    readonly ToolStripMenuItem stateItem = new ToolStripMenuItem();
    readonly ToolStripMenuItem toggleItem = new ToolStripMenuItem("Connect");
    readonly System.Windows.Forms.Timer timer = new System.Windows.Forms.Timer();
    readonly SynchronizationContext ui;
    MainWindow window;
    string state = "unknown";
    bool busy;
    bool reachable;
    bool polling;
    int misses;
    DateTime lastStart = DateTime.MinValue;

    public TrayApp(string[] args, string pipe)
    {
        int port;
        if (!int.TryParse(Options.Value(args, "--port") ?? "", out port)) port = 1081;
        baseUrl = "http://127.0.0.1:" + port + "/";
        ui = SynchronizationContext.Current ?? new WindowsFormsSynchronizationContext();

        ContextMenuStrip menu = new ContextMenuStrip();
        stateItem.Enabled = false;
        toggleItem.Click += delegate { Toggle(); };
        menu.Items.Add(stateItem);
        menu.Items.Add(toggleItem);
        menu.Items.Add(new ToolStripSeparator());
        ToolStripMenuItem open = new ToolStripMenuItem("Open Uni VPN");
        open.Font = new Font(open.Font, FontStyle.Bold);  // the default action, as on a double click
        open.Click += delegate { Show("#main"); };
        menu.Items.Add(open);
        menu.Items.Add("Settings", null, delegate { Show("#settings"); });
        menu.Items.Add(new ToolStripSeparator());
        menu.Items.Add("Exit", null, delegate { Exit(); });
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
                using (NamedPipeServerStream server = new NamedPipeServerStream(pipe, PipeDirection.In))
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
        if (window == null || window.IsDisposed) window = new MainWindow(baseUrl);
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
                }
                catch (Exception)
                {
                    reachable = false;
                }
            }
            client.Dispose();
            Render();
            StartServiceIfDown();
        };
        client.DownloadStringAsync(new Uri(baseUrl + "status.json"));
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

    static string Label(string state, bool reachable)
    {
        if (!reachable) return "Not running";
        switch (state)
        {
            case "connected": return "Connected";
            case "connecting": return "Connecting";
            case "disconnecting": return "Disconnecting";
            case "idle": return "Not connected";
            case "offline": return "No network";
            case "blocked": return "Paused";
            case "auth_failed": return "Sign-in failed";
            case "keyring": return "Action needed";
            case "error": return "Error";
            default: return state;
        }
    }

    void Render()
    {
        string label = Label(state, reachable);
        stateItem.Text = label;
        toggleItem.Text = On ? "Disconnect" : "Connect";
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
    bool quitting;
    string pending;

    public MainWindow(string baseUrl)
    {
        this.baseUrl = baseUrl;
        Text = "Uni VPN";
        Icon = Icon.ExtractAssociatedIcon(Application.ExecutablePath);
        AutoScaleDimensions = new SizeF(96F, 96F);
        AutoScaleMode = AutoScaleMode.Dpi;
        ClientSize = new Size(420, 660);
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
        catch (Exception)
        {
            // No WebView2 runtime (Windows 10 without a current Edge): the page in the browser.
            Process.Start(baseUrl);
            quitting = true;
            Close();
            return;
        }
        CoreWebView2Settings settings = view.CoreWebView2.Settings;
        settings.AreDefaultContextMenusEnabled = false;
        settings.AreDevToolsEnabled = false;
        settings.IsStatusBarEnabled = false;
        settings.IsZoomControlEnabled = false;
        settings.AreBrowserAcceleratorKeysEnabled = false;
        settings.IsPasswordAutosaveEnabled = false;
        settings.IsGeneralAutofillEnabled = false;
        settings.UserAgent = settings.UserAgent + " UniVPN";
        view.CoreWebView2.NavigationStarting += delegate(object sender, CoreWebView2NavigationStartingEventArgs e)
        {
            if (!Local(e.Uri)) { e.Cancel = true; Process.Start(e.Uri); }
        };
        view.CoreWebView2.NewWindowRequested += delegate(object sender, CoreWebView2NewWindowRequestedEventArgs e)
        {
            e.Handled = true;
            if (e.Uri.StartsWith("http://") || e.Uri.StartsWith("https://")) Process.Start(e.Uri);
        };
        // The service is still starting (sign-in) or restarting (update): try again shortly.
        view.CoreWebView2.NavigationCompleted += delegate(object sender, CoreWebView2NavigationCompletedEventArgs e)
        {
            if (e.IsSuccess || quitting) return;
            System.Windows.Forms.Timer retry = new System.Windows.Forms.Timer();
            retry.Interval = 2000;
            retry.Tick += delegate { retry.Dispose(); if (Visible) view.CoreWebView2.Navigate(baseUrl + "?app=1"); };
            retry.Start();
        };
        ready = true;
        ShowPage(pending ?? "");
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
        if (current.StartsWith(baseUrl) && (page == "" || page.StartsWith("#")))
        {
            if (page != "") view.CoreWebView2.ExecuteScriptAsync("location.hash = '" + page + "'");
            return;
        }
        view.CoreWebView2.Navigate(baseUrl + "?app=1" + page);
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
