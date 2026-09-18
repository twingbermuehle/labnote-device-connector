# Installs the LabNote Device Connector as a Windows service.
# Run in an elevated PowerShell:
#   .\install.ps1 -Binary .\labnote-connector_windows_amd64.exe
[CmdletBinding()]
param(
  [string]$Binary = ".\labnote-connector_windows_amd64.exe",
  [string]$InstallDir = "$env:ProgramFiles\LabNote Connector",
  [string]$ServiceName = "LabNoteConnector"
)

$ErrorActionPreference = "Stop"

$admin = ([Security.Principal.WindowsPrincipal] [Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole(
  [Security.Principal.WindowsBuiltInRole]::Administrator)
if (-not $admin) {
  throw "Run this in an elevated PowerShell (right-click PowerShell > Run as administrator)."
}

if (-not (Test-Path $Binary)) { throw "Binary not found: $Binary" }


New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
New-Item -ItemType Directory -Force -Path "$env:ProgramData\LabNoteConnector" | Out-Null

$target = Join-Path $InstallDir "labnote-connector.exe"
Copy-Item $Binary $target -Force

if (Get-Service -Name $ServiceName -ErrorAction SilentlyContinue) {
  Stop-Service $ServiceName -Force
  sc.exe delete $ServiceName | Out-Null
  Start-Sleep -Seconds 2
}

# LocalSystem so the service starts before any user logs on. The ingest API key
# is stored in the Windows Credential Manager of the service account during
# setup - it is never written to disk or to this script.
New-Service -Name $ServiceName `
  -BinaryPathName "`"$target`"" `
  -DisplayName "LabNote Device Connector" `
  -Description "Reads OPC UA / LADS instrument results and uploads them to LabNote. Outbound HTTPS only." `
  -StartupType Automatic | Out-Null

sc.exe failure $ServiceName reset= 86400 actions= restart/10000/restart/30000/restart/60000 | Out-Null
Start-Service $ServiceName

Write-Host ""
Write-Host "Installed. Finish the setup at http://127.0.0.1:8420"
Write-Host "Logs: $env:ProgramData\LabNoteConnector\logs"
