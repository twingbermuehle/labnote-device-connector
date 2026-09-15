# Removes the LabNote Device Connector service.
# Configuration, certificates and the local outbox in
# %ProgramData%\LabNoteConnector are kept unless -Purge is given.
[CmdletBinding()]
param(
  [string]$ServiceName = "LabNoteConnector",
  [switch]$Purge
)

$ErrorActionPreference = "Stop"

if (Get-Service -Name $ServiceName -ErrorAction SilentlyContinue) {
  Stop-Service $ServiceName -Force
  sc.exe delete $ServiceName | Out-Null
}

Remove-Item "$env:ProgramFiles\LabNote Connector" -Recurse -Force -ErrorAction SilentlyContinue

if ($Purge) {
  Remove-Item "$env:ProgramData\LabNoteConnector" -Recurse -Force -ErrorAction SilentlyContinue
  Write-Host "Removed the connector and all local data."
} else {
  Write-Host "Removed the connector. Local data kept in $env:ProgramData\LabNoteConnector"
}
