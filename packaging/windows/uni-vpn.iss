; One-click Windows installer (role model: Signal and Zoom: no pages, one administrator
; prompt, progress bar, the app opens at the end). Built by .github/workflows/ci.yml:
;   iscc /DAppVersion=0.1.42 /DSourceDir=stage /DOutputDir=out packaging\windows\uni-vpn.iss
; The Go core (bin\uni-vpn-core.exe), OpenConnect and the app window go to Program Files; then
; "uni-vpn-core.exe setup" sets uni-vpn up for the signed-in user. No Python.

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

[Code]
function OriginalUser(): String;
var
  ResultCode: Integer;
  Name: AnsiString;
  NameFile: String;
begin
  { With a standard account, Windows asks for an administrator's password and this runs as that
    administrator, but uni-vpn has to be set up for the account that will use it. }
  Result := GetUserNameString();
  NameFile := ExpandConstant('{tmp}\user.txt');
  if ExecAsOriginalUser(ExpandConstant('{cmd}'), '/c echo %USERNAME%>"' + NameFile + '"', '', SW_HIDE,
                        ewWaitUntilTerminated, ResultCode) and LoadStringFromFile(NameFile, Name) then
    if Trim(String(Name)) <> '' then
      Result := Trim(String(Name));
end;

function StatusUrl(): String;
var
  Lines: TArrayOfString;
  I, P: Integer;
  Line, Port: String;
begin
  Port := '1081';
  if LoadStringsFromFile(ExpandConstant('{localappdata}\uni-vpn\config.toml'), Lines) then
    for I := 0 to GetArrayLength(Lines) - 1 do
    begin
      Line := Trim(Lines[I]);
      if Pos('http_port', Line) = 1 then
      begin
        P := Pos('=', Line);
        if P > 0 then
          Port := Trim(Copy(Line, P + 1, Length(Line)));
      end;
    end;
  Result := 'http://127.0.0.1:' + Port + '/';
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
  LogDir, LogFile, SignedIn, Reason: String;
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
  SignedIn := OriginalUser();
  if CompareText(SignedIn, GetUserNameString()) <> 0 then
  begin
    Reason := 'Uni VPN was installed with the account ' + GetUserNameString() + ', but it has to be set up for ' +
               SignedIn + '.' + #13#10 + 'Sign in to Windows with an account that can install apps and open the installer again.';
    SaveStringToFile(LogFile, Reason + #13#10, False);
    SuppressibleMsgBox(SetupMessage(msgErrorTitle) + #13#10#13#10 + Reason, mbError, MB_OK, IDOK);
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
    SuppressibleMsgBox(SetupMessage(msgErrorTitle) + #13#10#13#10 + Copy(String(Log), Start, 600),
                       mbError, MB_OK, IDOK);
    Exit;
  end;
  ShellExecAsOriginalUser('open', StatusUrl(), '', '', SW_SHOWNORMAL, ewNoWait, ResultCode);
end;
