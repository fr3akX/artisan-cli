# Threat model: run only in a trusted, quiescent checkout with no malicious
# same-account process. Builder filesystem/content checks are point-in-time.
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$Version,
    [Parameter(Mandatory = $true)][string]$Commit,
    [string]$Destination = "release"
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest
$root = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot ".."))
$go = if ($env:GO) { $env:GO } else { "go" }
Set-Location $root
& $go run ./internal/releasebuilder/cmd $Version $Commit $Destination
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
