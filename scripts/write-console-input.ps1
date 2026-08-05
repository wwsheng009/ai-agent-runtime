[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [ValidateRange(1, 2147483647)]
    [int]$TargetProcessId,

    [Parameter(Mandatory)]
    [ValidateNotNullOrEmpty()]
    [string]$TextPath
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version 3.0

if ($env:OS -ne "Windows_NT") {
    throw "Console input injection is supported only on Windows."
}
if (-not (Test-Path -LiteralPath $TextPath -PathType Leaf)) {
    throw "Console input file does not exist: $TextPath"
}

Add-Type -TypeDefinition @'
using System;
using System.Collections.Generic;
using System.ComponentModel;
using System.IO;
using System.Runtime.InteropServices;
using System.Text;
using Microsoft.Win32.SafeHandles;

public static class AicliConsoleInputWriter
{
    private const ushort KeyEvent = 0x0001;
    private const ushort EnterKey = 0x000D;
    private const uint GenericRead = 0x80000000;
    private const uint GenericWrite = 0x40000000;
    private const uint ShareRead = 0x00000001;
    private const uint ShareWrite = 0x00000002;
    private const uint OpenExisting = 3;
    private const int ErrorInvalidHandle = 6;

    [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
    private struct KEY_EVENT_RECORD
    {
        public int KeyDown;
        public ushort RepeatCount;
        public ushort VirtualKeyCode;
        public ushort VirtualScanCode;
        public char UnicodeChar;
        public uint ControlKeyState;
    }

    [StructLayout(LayoutKind.Explicit, CharSet = CharSet.Unicode)]
    private struct INPUT_RECORD
    {
        [FieldOffset(0)]
        public ushort EventType;

        [FieldOffset(4)]
        public KEY_EVENT_RECORD KeyEvent;
    }

    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern bool FreeConsole();

    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern bool AttachConsole(uint processId);

    [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    private static extern SafeFileHandle CreateFileW(
        string fileName,
        uint desiredAccess,
        uint shareMode,
        IntPtr securityAttributes,
        uint creationDisposition,
        uint flagsAndAttributes,
        IntPtr templateFile);

    [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    private static extern bool WriteConsoleInputW(
        SafeFileHandle consoleInput,
        INPUT_RECORD[] records,
        uint recordCount,
        out uint recordsWritten);

    private static INPUT_RECORD MakeKey(char value, ushort virtualKey, bool down)
    {
        return new INPUT_RECORD {
            EventType = KeyEvent,
            KeyEvent = new KEY_EVENT_RECORD {
                KeyDown = down ? 1 : 0,
                RepeatCount = 1,
                VirtualKeyCode = virtualKey,
                VirtualScanCode = 0,
                UnicodeChar = value,
                ControlKeyState = 0
            }
        };
    }

    public static void Send(int targetProcessId, string textPath)
    {
        string text = File.ReadAllText(textPath, new UTF8Encoding(false, true));
        if (!FreeConsole()) {
            int error = Marshal.GetLastWin32Error();
            if (error != ErrorInvalidHandle) {
                throw new Win32Exception(error, "FreeConsole failed");
            }
        }
        if (!AttachConsole((uint)targetProcessId)) {
            throw new Win32Exception(Marshal.GetLastWin32Error(), "AttachConsole failed");
        }

        try {
            using (SafeFileHandle input = CreateFileW(
                "CONIN$", GenericRead | GenericWrite, ShareRead | ShareWrite,
                IntPtr.Zero, OpenExisting, 0, IntPtr.Zero))
            {
                if (input == null || input.IsInvalid) {
                    throw new Win32Exception(Marshal.GetLastWin32Error(), "opening CONIN$ failed");
                }
                List<INPUT_RECORD> records = new List<INPUT_RECORD>((text.Length + 1) * 2);
                foreach (char value in text) {
                    records.Add(MakeKey(value, 0, true));
                    records.Add(MakeKey(value, 0, false));
                }
                records.Add(MakeKey('\r', EnterKey, true));
                records.Add(MakeKey('\r', EnterKey, false));
                INPUT_RECORD[] payload = records.ToArray();
                uint written;
                if (!WriteConsoleInputW(input, payload, (uint)payload.Length, out written)) {
                    throw new Win32Exception(Marshal.GetLastWin32Error(), "WriteConsoleInputW failed");
                }
                if (written != (uint)payload.Length) {
                    throw new IOException("WriteConsoleInputW wrote " + written + " of " + payload.Length + " records");
                }
            }
        }
        finally {
            FreeConsole();
        }
    }
}
'@

try {
    [AicliConsoleInputWriter]::Send($TargetProcessId, [IO.Path]::GetFullPath($TextPath))
    exit 0
} catch {
    [Console]::Error.WriteLine($_.Exception.ToString())
    exit 1
}
