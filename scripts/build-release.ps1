[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$Version,
    [Parameter(Mandatory = $true)][string]$Commit,
    [string]$Destination = "dist/release"
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

if ($Version -cnotmatch '^v[0-9A-Za-z][0-9A-Za-z._-]{0,63}$') {
    throw "VERSION must be a safe v-prefixed tag value"
}
if ($Commit -cnotmatch '^[0-9a-fA-F]{40}$') {
    throw "COMMIT must be exactly 40 hexadecimal characters"
}
$normalizedDestination = $Destination.Replace('\', '/')
if ($normalizedDestination -cnotmatch '^dist/[A-Za-z0-9._/-]+$' -or $normalizedDestination.Contains('..') -or $normalizedDestination.Contains('//')) {
    throw "DESTINATION must be a safe repository-relative path below dist/"
}

$root = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot ".."))
Set-Location $root
$sources = @("LICENSE", "THIRD_PARTY_NOTICES.txt", "skills/artisan-inventory/SKILL.md")
foreach ($source in $sources) {
    $item = Get-Item -LiteralPath $source -Force -ErrorAction Stop
    if (-not $item.PSIsContainer -and -not ($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint)) {
        continue
    }
    throw "required regular source file is missing or unsafe: $source"
}
$go = if ($env:GO) { $env:GO } else { "go" }
if (-not (Get-Command $go -ErrorAction SilentlyContinue)) { throw "Go tool is unavailable: $go" }
if (-not (Get-Command tar -ErrorAction SilentlyContinue)) { throw "tar is required" }

$destinationPath = Join-Path $root $normalizedDestination
if (Test-Path -LiteralPath $destinationPath) {
    $destinationItem = Get-Item -LiteralPath $destinationPath -Force
    if (-not $destinationItem.PSIsContainer -or ($destinationItem.Attributes -band [System.IO.FileAttributes]::ReparsePoint)) {
        throw "DESTINATION must be a regular directory, not a symlink"
    }
    Remove-Item -LiteralPath $destinationPath -Recurse -Force
}
New-Item -ItemType Directory -Path $destinationPath | Out-Null
$staging = Join-Path $root ("dist/.release-build-" + [Guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $staging | Out-Null

$ldflags = "-s -w -X github.com/fr3akX/artisan-cli/internal/release.Version=$Version -X github.com/fr3akX/artisan-cli/internal/release.Commit=$Commit"
$hostOS = (& $go env GOHOSTOS).Trim()
$hostArch = (& $go env GOHOSTARCH).Trim()
$archives = [System.Collections.Generic.List[string]]::new()
$archiveEpoch = [DateTimeOffset]::new(2000, 1, 1, 0, 0, 0, [TimeSpan]::Zero)

try {
    foreach ($goos in @("linux", "darwin", "windows")) {
        foreach ($goarch in @("amd64", "arm64")) {
            $top = "artisan-$Version-$goos-$goarch"
            $stage = Join-Path $staging $top
            $skillDirectory = Join-Path $stage "skills/artisan-inventory"
            New-Item -ItemType Directory -Path $skillDirectory -Force | Out-Null
            Copy-Item -LiteralPath "LICENSE" -Destination (Join-Path $stage "LICENSE")
            Copy-Item -LiteralPath "THIRD_PARTY_NOTICES.txt" -Destination (Join-Path $stage "THIRD_PARTY_NOTICES.txt")
            Copy-Item -LiteralPath "skills/artisan-inventory/SKILL.md" -Destination (Join-Path $skillDirectory "SKILL.md")

            $binary = if ($goos -eq "windows") { "artisan.exe" } else { "artisan" }
            $oldCGO = $env:CGO_ENABLED
            $oldGOOS = $env:GOOS
            $oldGOARCH = $env:GOARCH
            try {
                $env:CGO_ENABLED = "0"
                $env:GOOS = $goos
                $env:GOARCH = $goarch
                & $go build -trimpath "-ldflags=$ldflags" -o (Join-Path $stage $binary) ./cmd/artisan
                if ($LASTEXITCODE -ne 0) { throw "Go build failed for $goos/$goarch" }
            } finally {
                $env:CGO_ENABLED = $oldCGO
                $env:GOOS = $oldGOOS
                $env:GOARCH = $oldGOARCH
            }
            $buildInfo = (& $go version -m (Join-Path $stage $binary) 2>&1 | Out-String)
            if ($LASTEXITCODE -ne 0 -or -not $buildInfo.Contains("github.com/fr3akX/artisan-cli")) {
                throw "missing Go build metadata for $top"
            }
            Get-ChildItem -LiteralPath $stage -Recurse -Force | ForEach-Object { $_.LastWriteTimeUtc = [DateTime]::UnixEpoch }
            (Get-Item -LiteralPath $stage).LastWriteTimeUtc = [DateTime]::UnixEpoch

            if ($goos -eq "windows") {
                Add-Type -AssemblyName System.IO.Compression
                Add-Type -AssemblyName System.IO.Compression.FileSystem
                $archiveName = "$top.zip"
                $archivePath = Join-Path $destinationPath $archiveName
                $stream = [System.IO.File]::Open($archivePath, [System.IO.FileMode]::CreateNew)
                try {
                    $zip = [System.IO.Compression.ZipArchive]::new($stream, [System.IO.Compression.ZipArchiveMode]::Create)
                    try {
                        foreach ($directory in @("$top/", "$top/skills/", "$top/skills/artisan-inventory/")) {
                            $entry = $zip.CreateEntry($directory)
                            $entry.LastWriteTime = $archiveEpoch
                        }
                        foreach ($relative in @($binary, "LICENSE", "THIRD_PARTY_NOTICES.txt", "skills/artisan-inventory/SKILL.md")) {
                            $entry = $zip.CreateEntry("$top/$relative", [System.IO.Compression.CompressionLevel]::Optimal)
                            $entry.LastWriteTime = $archiveEpoch
                            $input = [System.IO.File]::OpenRead((Join-Path $stage $relative))
                            $output = $entry.Open()
                            try { $input.CopyTo($output) } finally { $output.Dispose(); $input.Dispose() }
                        }
                    } finally { $zip.Dispose() }
                } finally { $stream.Dispose() }
            } else {
                $archiveName = "$top.tar.gz"
                $archivePath = Join-Path $destinationPath $archiveName
                & tar -czf $archivePath -C $staging $top
                if ($LASTEXITCODE -ne 0) { throw "archive creation failed for $top" }
            }
            $archives.Add($archiveName)

            $entries = if ($archiveName.EndsWith(".zip")) {
                $zip = [System.IO.Compression.ZipFile]::OpenRead($archivePath)
                try { @($zip.Entries | ForEach-Object { $_.FullName }) } finally { $zip.Dispose() }
            } else {
                @(& tar -tzf $archivePath)
            }
            $expected = @("$top/", "$top/$binary", "$top/LICENSE", "$top/THIRD_PARTY_NOTICES.txt", "$top/skills/", "$top/skills/artisan-inventory/", "$top/skills/artisan-inventory/SKILL.md")
            if ((Compare-Object ($entries | Sort-Object) ($expected | Sort-Object))) { throw "archive has unexpected contents: $archiveName" }
            if ($entries | Where-Object { $_.StartsWith('/') -or $_.Contains('../') }) { throw "archive contains an unsafe path" }

            if ($goos -eq $hostOS -and $goarch -eq $hostArch) {
                $versionOutput = (& (Join-Path $stage $binary) --json version | Out-String)
                if ($LASTEXITCODE -ne 0 -or -not $versionOutput.Contains('"version":"' + $Version + '"') -or -not $versionOutput.Contains('"commit":"' + $Commit + '"')) {
                    throw "native target reported unexpected version metadata"
                }
            }
        }
    }

    $checksumPath = Join-Path $destinationPath "checksums.txt"
    $checksumLines = foreach ($archive in $archives) {
        $hash = (Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $destinationPath $archive)).Hash.ToLowerInvariant()
        "$hash  $archive"
    }
    if ($checksumLines.Count -ne 6) { throw "checksum manifest must contain exactly six archives" }
    [System.IO.File]::WriteAllLines($checksumPath, $checksumLines, [System.Text.UTF8Encoding]::new($false))
    foreach ($archive in $archives) {
        if (-not (Test-Path -LiteralPath (Join-Path $destinationPath $archive))) { throw "archive is missing: $archive" }
    }
    Get-ChildItem -LiteralPath $destinationPath -File | Sort-Object Name | Select-Object -ExpandProperty FullName
} finally {
    if (Test-Path -LiteralPath $staging) { Remove-Item -LiteralPath $staging -Recurse -Force }
}
