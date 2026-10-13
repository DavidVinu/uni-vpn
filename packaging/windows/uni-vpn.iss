; One-click Windows installer (role model: Signal and Zoom: no pages, one administrator
; prompt, progress bar, the app opens at the end). Built by .github/workflows/ci.yml:
;   iscc /DAppVersion=0.1.42 /DSourceDir=stage /DOutputDir=out packaging\windows\uni-vpn.iss
; The Go core (bin\uni-vpn-core.exe), OpenConnect and the app window go to Program Files; then
; "uni-vpn-core.exe setup" sets uni-vpn up for the signed-in user. No Python.
; Saved as UTF-8 with a byte order mark: [CustomMessages] has the texts in every language.

#ifndef AppVersion
  #define AppVersion "0.1.0"
#endif
#ifndef SourceDir
  #define SourceDir "stage"
#endif
#ifndef OutputDir
  #define OutputDir "out"
#endif

[Setup]
AppId={{6F1D2C8A-3B5E-4C47-9A0E-2D7B8E4F1A63}
AppName=Uni VPN
AppVersion={#AppVersion}
AppVerName=Uni VPN
AppPublisher=uni-vpn
AppPublisherURL=https://github.com/DavidVinu/uni-vpn
DefaultDirName={commonpf64}\uni-vpn
UninstallDisplayName=Uni VPN
OutputDir={#OutputDir}
OutputBaseFilename=uni-vpn-setup
PrivilegesRequired=admin
; OpenConnect has no build for ARM processors.
ArchitecturesAllowed=x64os
ArchitecturesInstallIn64BitMode=x64os
DisableWelcomePage=yes
DisableDirPage=yes
DisableProgramGroupPage=yes
DisableReadyPage=yes
DisableFinishedPage=yes
ShowLanguageDialog=no
LanguageDetectionMethod=uilanguage
CloseApplications=no
WizardStyle=modern
Compression=lzma2
SolidCompression=yes

[Languages]
Name: "en"; MessagesFile: "compiler:Default.isl"
Name: "de"; MessagesFile: "compiler:Languages\German.isl"
Name: "fr"; MessagesFile: "compiler:Languages\French.isl"
Name: "es"; MessagesFile: "compiler:Languages\Spanish.isl"
Name: "ja"; MessagesFile: "compiler:Languages\Japanese.isl"
#ifexist AddBackslash(CompilerPath) + "Languages\ChineseSimplified.isl"
Name: "zh"; MessagesFile: "compiler:Languages\ChineseSimplified.isl"
#endif
#ifexist AddBackslash(CompilerPath) + "Languages\ChineseTraditional.isl"
Name: "zhtw"; MessagesFile: "compiler:Languages\ChineseTraditional.isl"
#endif

[CustomMessages]
; In plain words, like the app's own texts (uni_vpn/locales).
OtherAccount=Uni VPN has to be installed from the account that will use it. Sign in to Windows with an account that is allowed to install apps, then open the installer again.
SetupFailed=Uni VPN could not be set up. Restart the computer, then open the installer again.
de.OtherAccount=Uni VPN muss mit dem Konto installiert werden, das es benutzt. Melde dich bei Windows mit einem Konto an, das Apps installieren darf, und öffne das Installationsprogramm dann noch einmal.
de.SetupFailed=Uni VPN konnte nicht eingerichtet werden. Starte den Computer neu und öffne das Installationsprogramm dann noch einmal.
fr.OtherAccount=Uni VPN doit être installé depuis le compte qui va l’utiliser. Connectez-vous à Windows avec un compte autorisé à installer des applications, puis rouvrez le programme d’installation.
fr.SetupFailed=Uni VPN n’a pas pu être configuré. Redémarrez l’ordinateur, puis rouvrez le programme d’installation.
es.OtherAccount=Uni VPN debe instalarse desde la cuenta que lo va a usar. Inicia sesión en Windows con una cuenta que pueda instalar aplicaciones y vuelve a abrir el instalador.
es.SetupFailed=No se pudo configurar Uni VPN. Reinicia el ordenador y vuelve a abrir el instalador.
ja.OtherAccount=Uni VPN は、使うアカウントからインストールする必要があります。アプリをインストールできるアカウントで Windows にサインインしてから、もう一度インストーラーを開いてください。
ja.SetupFailed=Uni VPN を設定できませんでした。コンピューターを再起動してから、もう一度インストーラーを開いてください。
#ifexist AddBackslash(CompilerPath) + "Languages\ChineseSimplified.isl"
zh.OtherAccount=Uni VPN 必须从要使用它的账户安装。请用可以安装应用的账户登录 Windows，然后重新打开安装程序。
zh.SetupFailed=无法设置 Uni VPN。请重启电脑，然后重新打开安装程序。
#endif
#ifexist AddBackslash(CompilerPath) + "Languages\ChineseTraditional.isl"
zhtw.OtherAccount=Uni VPN 必須從要使用它的帳戶安裝。請用可以安裝應用程式的帳戶登入 Windows，然後重新開啟安裝程式。
zhtw.SetupFailed=無法設定 Uni VPN。請重新啟動電腦，然後重新開啟安裝程式。
#endif

[Files]
Source: "{#SourceDir}\*"; DestDir: "{app}"; Flags: recursesubdirs createallsubdirs ignoreversion

[InstallDelete]
; What the Python version had here: Python, its program and the install scripts.
Type: filesandordirs; Name: "{app}\python"
Type: filesandordirs; Name: "{app}\uni_vpn"
Type: filesandordirs; Name: "{app}\launchd"
Type: filesandordirs; Name: "{app}\systemd"
Type: files; Name: "{app}\bin\uni-vpn"
Type: files; Name: "{app}\install.ps1"
Type: files; Name: "{app}\install.cmd"
Type: files; Name: "{app}\install.sh"

[UninstallRun]
; --yes: nobody could answer the question about the keyring entries.
Filename: "{app}\bin\uni-vpn-core.exe"; Parameters: "uninstall --yes"; Flags: runhidden waituntilterminated; RunOnceId: "uninstall"

[UninstallDelete]
; Files the automatic updates added after the installation.
Type: filesandordirs; Name: "{app}"
; An update that waited for the VPN to go idle (the uninstall ends the service without a clean stop).
Type: filesandordirs; Name: "{commonpf64}\.uni-vpn-core-update-*"

[Code]
function SameAccount(): Boolean;
var
  ResultCode: Integer;
begin
  { With a standard account, Windows asks for an administrator's password and this runs as that
    administrator, but uni-vpn has to be set up for the account that will use it. cmd of the
    signed-in account compares the names itself: a name it writes to a file comes back here in
    another code page, and a name with an umlaut or accent would never match. Exit code 1 means
    another account; anything else (cmd did not run) lets the setup go ahead, as before. }
  Result := True;
  if ExecAsOriginalUser(ExpandConstant('{cmd}'), '/c if /i "%USERNAME%"=="' + GetUserNameString() + '" (exit 0) else (exit 1)',
                        '', SW_HIDE, ewWaitUntilTerminated, ResultCode) then
    Result := ResultCode <> 1;
end;

function HttpPort(): String;
var
  Lines: TArrayOfString;
  I, P, Port: Integer;
  Line, Value: String;
begin
  Result := '1081';
  if LoadStringsFromFile(ExpandConstant('{localappdata}\uni-vpn\config.toml'), Lines) then
    for I := 0 to GetArrayLength(Lines) - 1 do
    begin
      Line := Trim(Lines[I]);
      if Pos('http_port', Line) = 1 then
      begin
        P := Pos('=', Line);
        if P > 0 then
        begin
          { The file setup writes has a comment after the number: "http_port = 1081   # status page ..." }
          Value := Copy(Line, P + 1, Length(Line));
          P := Pos('#', Value);
          if P > 0 then
            Value := Copy(Value, 1, P - 1);
          Port := StrToIntDef(Trim(Value), 0);
          if (Port >= 1) and (Port <= 65535) then
            Result := IntToStr(Port);
        end;
      end;
    end;
end;

procedure StopRunning();
var
  ResultCode: Integer;
begin
  { An earlier version keeps its files open while it runs: the service (also the Python one),
    openconnect and the app window. Ending the task first keeps Task Scheduler from starting
    it again. }
  Exec(ExpandConstant('{sys}\schtasks.exe'), '/End /TN uni-vpn', '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
  Exec(ExpandConstant('{sys}\WindowsPowerShell\v1.0\powershell.exe'), '-NoProfile -NonInteractive -Command "Get-Process | Where-Object { $_.Path -like ''' +
       ExpandConstant('{app}') + '\*'' } | Stop-Process -Force"', '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
end;

procedure CurStepChanged(CurStep: TSetupStep);
var
  ResultCode: Integer;
  LogDir, LogFile, App: String;
  Log: AnsiString;
  Start: Integer;
begin
  if (CurStep = ssInstall) and DirExists(ExpandConstant('{app}')) then
    StopRunning();
  if CurStep <> ssPostInstall then
    Exit;
  WizardForm.StatusLabel.Caption := SetupMessage(msgStatusRunProgram);
  LogDir := ExpandConstant('{localappdata}\uni-vpn\logs');
  ForceDirectories(LogDir);
  LogFile := LogDir + '\install.log';
  { Setup registers the service and the proxy setting for the account it runs as, like
    install.ps1 -ForUser did. }
  if not SameAccount() then
  begin
    SaveStringToFile(LogFile, 'Installed with the account ' + GetUserNameString() + ', not the one signed in to Windows.' + #13#10, False);
    SuppressibleMsgBox(SetupMessage(msgErrorTitle) + #13#10#13#10 + CustomMessage('OtherAccount'), mbError, MB_OK, IDOK);
    Exit;
  end;
  { "< nul": nobody can answer a question here. cmd /S /C drops the outer quotes. }
  if not Exec(ExpandConstant('{cmd}'),
              '/S /C ""' + ExpandConstant('{app}\bin\uni-vpn-core.exe') + '" setup --no-browser < nul > "' +
              LogFile + '" 2>&1"',
              '', SW_HIDE, ewWaitUntilTerminated, ResultCode) or (ResultCode <> 0) then
  begin
    LoadStringFromFile(LogFile, Log);
    Start := Length(Log) - 600;
    if Start < 1 then
      Start := 1;
    SuppressibleMsgBox(SetupMessage(msgErrorTitle) + #13#10#13#10 + CustomMessage('SetupFailed') + #13#10#13#10 +
                       Copy(String(Log), Start, 600), mbError, MB_OK, IDOK);
    Exit;
  end;
  { The app window, as the signed-in user (this runs as administrator). Setup has already put a
    copy in the notification area; this second start hands it the window, as a click on the
    Start menu entry does. }
  App := ExpandConstant('{app}\desktop\Uni VPN.exe');
  ShellExecAsOriginalUser('open', App, '--port ' + HttpPort(), ExtractFileDir(App), SW_SHOWNORMAL, ewNoWait, ResultCode);
end;
