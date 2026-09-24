$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
[Console]::InputEncoding = New-Object System.Text.UTF8Encoding($false)
[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false)
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
Add-Type -TypeDefinition @'
using System;
using System.Text;
using System.Runtime.InteropServices;
public static class DesktopNative {
 [DllImport("user32.dll")] public static extern bool SetProcessDPIAware();
 [DllImport("user32.dll")] public static extern IntPtr GetForegroundWindow();
 [DllImport("user32.dll", CharSet=CharSet.Unicode)] public static extern int GetWindowText(IntPtr h, StringBuilder text, int max);
 [DllImport("user32.dll")] public static extern bool SetCursorPos(int x, int y);
 [DllImport("user32.dll")] public static extern bool GetCursorPos(out POINT p);
 [DllImport("user32.dll", SetLastError=true)] public static extern uint SendInput(uint count, INPUT[] inputs, int size);
 [DllImport("user32.dll", SetLastError=true)] public static extern IntPtr OpenInputDesktop(uint flags, bool inherit, uint access);
 [DllImport("user32.dll")] public static extern bool CloseDesktop(IntPtr h);
 [StructLayout(LayoutKind.Sequential)] public struct POINT { public int X,Y; }
 [StructLayout(LayoutKind.Sequential)] public struct MOUSEINPUT {public int dx,dy;public uint data,flags,time; public UIntPtr extra;}
 [StructLayout(LayoutKind.Sequential)] public struct KEYBDINPUT {public ushort vk,scan;public uint flags,time;public UIntPtr extra;}
 [StructLayout(LayoutKind.Explicit)] public struct UNION {[FieldOffset(0)]public MOUSEINPUT mouse;[FieldOffset(0)]public KEYBDINPUT key;}
 [StructLayout(LayoutKind.Sequential)] public struct INPUT {public uint type;public UNION u;}
 static void Send(INPUT[] inputs) {if(SendInput((uint)inputs.Length,inputs,Marshal.SizeOf(typeof(INPUT)))!=inputs.Length)throw new Exception("SendInput incomplete or blocked by UIPI/secure desktop; do not replay");}
 static INPUT Mouse(uint flags,uint data=0){var x=new INPUT();x.type=0;x.u.mouse.flags=flags;x.u.mouse.data=data;return x;}
 static INPUT Key(ushort vk,ushort scan,uint flags){var x=new INPUT();x.type=1;x.u.key.vk=vk;x.u.key.scan=scan;x.u.key.flags=flags|((vk==0x5b||vk==0x5c)?1u:0u);return x;}
 public static void Click(string button){uint d=button=="right"?8u:(button=="middle"?32u:2u);Send(new[]{Mouse(d),Mouse(d*2)});}
 public static void Button(string button,bool down){uint d=button=="right"?8u:(button=="middle"?32u:2u);Send(new[]{Mouse(down?d:d*2)});}
 public static void Scroll(int amount){Send(new[]{Mouse(0x0800,unchecked((uint)(-amount*120)))});}
 public static void Type(string text){foreach(char c in text)Send(new[]{Key(0,c,4),Key(0,c,6)});}
 public static void Chord(ushort[] modifiers,ushort key){var a=new INPUT[modifiers.Length*2+2];int i=0;foreach(var m in modifiers)a[i++]=Key(m,0,0);uint ext=(key>=0x21&&key<=0x28)||key==0x2e?1u:0u;a[i++]=Key(key,0,ext);a[i++]=Key(key,0,ext|2);for(int j=modifiers.Length-1;j>=0;j--)a[i++]=Key(modifiers[j],0,2);Send(a);}
 public static void Release(){Send(new[]{Mouse(4),Mouse(16),Mouse(64),Key(0x10,0,2),Key(0x11,0,2),Key(0x12,0,2),Key(0x5b,0,2)});}
}
'@
[void][DesktopNative]::SetProcessDPIAware()
function Assert-Cursor([int]$x,[int]$y) {
 if (-not [DesktopNative]::SetCursorPos($x,$y)) {
  throw 'SetCursorPos failed. The foreground window is likely running at a higher integrity level (UIPI), so this process cannot move the cursor or click it. Do not write a replacement mouse or keyboard driver. Ask the user before running DeepSentry elevated.'
 }
}
# Invoke-Desktop performs one request and returns a hashtable for status (which
# the caller serializes) or $null for actions. Both single-shot and persistent
# serve mode call this, so the injection logic never diverges between the two.
function Invoke-Desktop($r) {
 $bounds = [System.Windows.Forms.Screen]::PrimaryScreen.Bounds
 $handle = [DesktopNative]::GetForegroundWindow()
 $title = New-Object System.Text.StringBuilder 512
 [void][DesktopNative]::GetWindowText($handle,$title,512)
 $inputDesktop = [DesktopNative]::OpenInputDesktop(0,$false,0x0100)
 $ready = [Environment]::UserInteractive -and $handle -ne [IntPtr]::Zero -and $inputDesktop -ne [IntPtr]::Zero
 if ($inputDesktop -ne [IntPtr]::Zero) {[void][DesktopNative]::CloseDesktop($inputDesktop)}
 if ($r.action -eq 'status') {
  return @{driver='windows-sendinput';ready=$ready;reason=$(if($ready){''}else{'No unlocked interactive desktop. Service/session-0, locked RDP and secure UAC desktop are not supported.'});x=$bounds.X;y=$bounds.Y;width=$bounds.Width;height=$bounds.Height;foreground=($handle.ToInt64().ToString()+':'+$title.ToString());surface='primary display'}
 }
 if (-not $ready) {throw 'Interactive desktop unavailable'}
 switch ($r.action) {
  'capture' {
   $bitmap=New-Object System.Drawing.Bitmap $bounds.Width,$bounds.Height
   $graphics=[System.Drawing.Graphics]::FromImage($bitmap)
   try {$graphics.CopyFromScreen($bounds.Location,[System.Drawing.Point]::Empty,$bounds.Size);$bitmap.Save([string]$r.path,[System.Drawing.Imaging.ImageFormat]::Png)}finally{$graphics.Dispose();$bitmap.Dispose()}
  }
  'move' {Assert-Cursor ([int]$r.x) ([int]$r.y)}
  'click' {Assert-Cursor ([int]$r.x) ([int]$r.y);[DesktopNative]::Click([string]$r.button)}
  'double_click' {Assert-Cursor ([int]$r.x) ([int]$r.y);[DesktopNative]::Click([string]$r.button);Start-Sleep -Milliseconds 80;[DesktopNative]::Click([string]$r.button)}
  'drag' {
   Assert-Cursor ([int]$r.x) ([int]$r.y)
   [DesktopNative]::Button([string]$r.button,$true)
   try {
    for($i=1;$i -le 10;$i++){
     Assert-Cursor ([int]($r.x+($r.to_x-$r.x)*$i/10)) ([int]($r.y+($r.to_y-$r.y)*$i/10))
     Start-Sleep -Milliseconds 10
    }
   } finally {[DesktopNative]::Button([string]$r.button,$false)}
  }
  'scroll' {
   if ($r.has_point) { Assert-Cursor ([int]$r.x) ([int]$r.y) }
   [DesktopNative]::Scroll([int]$r.amount)
  }
  'type' {[DesktopNative]::Type([string]$r.text)}
  'key' {
   $parts=([string]$r.key).ToUpperInvariant().Split('+')
   $map=@{ENTER=13;TAB=9;ESCAPE=27;SPACE=32;BACKSPACE=8;DELETE=46;UP=38;DOWN=40;LEFT=37;RIGHT=39;HOME=36;END=35;PAGEUP=33;PAGEDOWN=34;CTRL=17;ALT=18;SHIFT=16;META=91}
   $last=$parts[-1];$key=0
   if($map.ContainsKey($last)){$key=$map[$last]}elseif($last -match '^[A-Z0-9]$'){$key=[int][char]$last}else{throw 'Invalid key'}
   $modifiers=@();for($i=0;$i -lt $parts.Length-1;$i++){if($parts[$i] -notin @('CTRL','ALT','SHIFT','META')){throw 'Invalid modifier'};$modifiers+=$map[$parts[$i]]}
   [DesktopNative]::Chord([uint16[]]$modifiers,[uint16]$key)
  }
  'release' {[DesktopNative]::Release()}
  default {throw 'Unsupported action'}
 }
 return $null
}
# Persistent serve mode: one process, one Add-Type, many newline-delimited
# requests. DeepSentry falls back to a fresh single-shot process if this stream
# ever breaks, so a serve-mode failure never blocks desktop control.
if ($env:DEEPSENTRY_DESKTOP_SERVE) {
 while ($true) {
  $line = [Console]::In.ReadLine()
  if ($null -eq $line) { break }
  $line = $line.Trim()
  if ($line -eq '') { continue }
  try {
   $req = $line | ConvertFrom-Json
   $data = Invoke-Desktop $req
   $json = @{ ok = $true; data = $data } | ConvertTo-Json -Compress -Depth 6
  } catch {
   $json = @{ ok = $false; error = [string]$_.Exception.Message } | ConvertTo-Json -Compress -Depth 6
  }
  [Console]::Out.WriteLine($json)
  [Console]::Out.Flush()
 }
 exit 0
}
try {
 $r = [Console]::In.ReadToEnd() | ConvertFrom-Json
 $data = Invoke-Desktop $r
 if ($null -ne $data) { $data | ConvertTo-Json -Compress }
} catch {
 [Console]::Error.WriteLine([string]$_.Exception.Message)
 exit 1
}
